#!/usr/bin/env python3
"""Paired, order-balanced benchmark of ytd vs yt-dlp with a network probe.

Each round runs both tools back to back (order alternates ABBA-style so linear
network drift cancels). The main metric is the per-round speed ratio
t_ytdlp / t_ytd, summarised as a geometric mean with a 95% t-interval on the
log ratios. Right before every run, gvprobe downloads 80 MiB of a stream that
is not under test from googlevideo, so absolute times can also be normalised
to the mean line speed. Probe readings outside [0.5x, 2x] of the median are
treated as probe errors and replaced by the median.

Build the binaries first (from the repo root):
    go build -o bench/ytd . && go build -o bench/gvprobe-bin ./bench/gvprobe
Then, e.g.:
    python3 bench/bench.py --name vp9 --url 'https://www.youtube.com/watch?v=b8wsjx7dFwM' \
        --itags 315+251 --format webm --rounds 7

YouTube flags an IP as a bot after a few dozen full downloads in a short
time ("Sign in to confirm you're not a bot", for yt-dlp too); space runs out.
"""
import argparse
import json
import math
import os
import re
import shutil
import statistics as st
import subprocess
import sys
import time

T975 = {1: 12.706, 2: 4.303, 3: 3.182, 4: 2.776, 5: 2.571, 6: 2.447, 7: 2.365,
        8: 2.306, 9: 2.262, 10: 2.228, 11: 2.201, 12: 2.179, 13: 2.160, 14: 2.145}


PROBE = {"bin": os.path.join(os.path.dirname(os.path.abspath(__file__)), "gvprobe-bin"), "id": None, "itag": "303", "bytes": str(80 << 20), "conns": "4"}


def probe():
    """googlevideo throughput in MB/s: first 80 MiB of a stream that is not
    under test, over 4 connections. NaN if it fails."""
    try:
        p = subprocess.run([PROBE["bin"], PROBE["id"], PROBE["itag"], PROBE["bytes"], PROBE["conns"]],
                           capture_output=True, text=True, timeout=90)
        return float(p.stdout.strip())
    except (subprocess.TimeoutExpired, ValueError):
        return float("nan")


def clean_probes(values):
    """Replace probe readings outside [0.5x, 2x] of the median (or NaN) with
    the median; returns (cleaned, number replaced)."""
    good = [v for v in values if not math.isnan(v)]
    if not good:
        return values, len(values)
    med = st.median(good)
    out, n = [], 0
    for v in values:
        if math.isnan(v) or v < 0.5 * med or v > 2 * med:
            out.append(med)
            n += 1
        else:
            out.append(v)
    return out, n


def command(tool, a, d):
    if tool == "ytd":
        return [a.ytd, "down", a.url, "--itag", a.itags, "--format", a.format, "-o", d]
    return ["yt-dlp", "--ignore-config", "--no-warnings", "--newline", "-f", a.itags,
            "--merge-output-format", a.format, "-o", os.path.join(d, "out.%(ext)s"), a.url]


def run(tool, rnd, a, attempt=1):
    d = os.path.join(a.workdir, f"{tool}-r{rnd}-a{attempt}")
    os.makedirs(d)  # fails if it exists: never reuse a directory
    log = d + ".log"
    with open(log, "w") as lf:
        t0 = time.monotonic()
        p = subprocess.run(command(tool, a, d), stdout=lf, stderr=subprocess.STDOUT)
        dt = time.monotonic() - t0
    text = open(log, errors="replace").read()
    sleep = sum(float(x) for x in re.findall(r"Sleeping (\d+(?:\.\d+)?) seconds", text))
    files = [f for f in os.listdir(d) if not f.endswith((".part", ".ytdl")) and f.endswith("." + a.format)]
    ok = p.returncode == 0 and len(files) == 1
    if not ok:
        tail = "\n".join(text.strip().splitlines()[-5:])
        print(f"  !! {tool} r{rnd} attempt {attempt} failed (exit {p.returncode}):\n{tail}", flush=True)
        if attempt == 1:
            shutil.rmtree(d, ignore_errors=True)
            return run(tool, rnd, a, attempt=2)
        return {"tool": tool, "round": rnd, "ok": False, "attempts": attempt}
    path = os.path.join(d, files[0])
    return {"tool": tool, "round": rnd, "ok": True, "attempts": attempt, "time": dt, "sleep": sleep,
            "size": os.path.getsize(path), "path": path, "dir": d}


def ci_ratio(ratios):
    logs = [math.log(r) for r in ratios]
    n = len(logs)
    m = st.mean(logs)
    if n < 2:
        return math.exp(m), float("nan"), float("nan")
    half = T975[min(n - 1, 14)] * st.stdev(logs) / math.sqrt(n)
    return math.exp(m), math.exp(m - half), math.exp(m + half)


def describe(xs):
    xs = [x for x in xs if not math.isnan(x)]
    if not xs:
        return None
    return {"median": st.median(xs), "mean": st.mean(xs), "sd": st.stdev(xs) if len(xs) > 1 else 0.0,
            "cv": (st.stdev(xs) / st.mean(xs)) if len(xs) > 1 else 0.0, "min": min(xs), "max": max(xs)}


def sh(cmd):
    return subprocess.run(cmd, capture_output=True, text=True).stdout


def validate(ytd_path, ytdlp_path):
    out = {}
    fields = ("stream=codec_name,profile,width,height,r_frame_rate,pix_fmt,color_range,color_space,"
              "color_transfer,color_primaries,channels,sample_rate:stream_side_data_list:format=duration")
    pa = sh(["ffprobe", "-v", "error", "-show_entries", fields, "-of", "compact", ytd_path]).splitlines()
    pb = sh(["ffprobe", "-v", "error", "-show_entries", fields, "-of", "compact", ytdlp_path]).splitlines()
    out["ffprobe_ytd"] = pa
    out["ffprobe_ytdlp"] = pb
    out["ffprobe_equal"] = pa == pb

    ha = sh(["ffmpeg", "-v", "error", "-i", ytd_path, "-map", "0", "-c", "copy", "-f", "streamhash", "-hash", "md5", "-"])
    hb = sh(["ffmpeg", "-v", "error", "-i", ytdlp_path, "-map", "0", "-c", "copy", "-f", "streamhash", "-hash", "md5", "-"])
    out["streamhash_ytd"], out["streamhash_ytdlp"] = ha.split(), hb.split()
    out["streamhash_equal"] = ha == hb

    def crc(p):
        lines = sh(["ffmpeg", "-v", "error", "-i", p, "-map", "0", "-c", "copy", "-f", "framecrc", "-"]).splitlines()
        rows = []
        for l in lines:
            if l.startswith("#"):
                continue
            c = [x.strip() for x in l.split(",")]
            rows.append((c[0], c[1], c[2], c[4]))  # stream, dts, pts, size
        return rows
    ca, cb = crc(ytd_path), crc(ytdlp_path)
    diffs = [(x, y) for x, y in zip(ca, cb) if x != y]
    out["packets"] = [len(ca), len(cb)]
    out["timestamp_diffs"] = len(diffs) + abs(len(ca) - len(cb))
    out["timestamp_diff_examples"] = diffs[:3]

    dec = {}
    for label, args in (("first10s", ["-t", "10"]), ("seek_mid", ["-ss", "150", "-t", "5"]), ("last5s", ["-sseof", "-5"])):
        pre = args if label != "first10s" else []
        post = args if label == "first10s" else []
        cmd = ["ffmpeg", "-v", "error", "-xerror"] + pre + ["-i", ytd_path] + post + ["-f", "null", "-"]
        p = subprocess.run(cmd, capture_output=True, text=True)
        dec[label] = "ok" if p.returncode == 0 and not p.stderr.strip() else (p.stderr.strip()[-300:] or f"exit {p.returncode}")
    out["decode_ytd"] = dec
    with open(ytd_path, "rb") as f:
        head = f.read(64)
    out["head_bytes"] = head[:40].hex()
    return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--name", required=True)
    ap.add_argument("--url", required=True)
    ap.add_argument("--itags", required=True)
    ap.add_argument("--format", required=True)
    ap.add_argument("--rounds", type=int, default=7)
    ap.add_argument("--ytd", default=os.path.join(os.path.dirname(os.path.abspath(__file__)), "ytd"))
    ap.add_argument("--root", default=os.path.join(os.path.dirname(os.path.abspath(__file__)), "runs"))
    ap.add_argument("--probe-itag", default="303")
    a = ap.parse_args()
    PROBE["id"] = re.search(r"v=([\w-]{11})", a.url).group(1)
    PROBE["itag"] = a.probe_itag
    a.workdir = os.path.join(a.root, a.name)
    if os.path.exists(a.workdir):
        sys.exit(f"{a.workdir} exists; remove it first")
    os.makedirs(a.workdir)

    results, keep = [], {}
    for rnd in range(1, a.rounds + 1):
        order = ["ytd", "ytdlp"] if rnd % 2 == 1 else ["ytdlp", "ytd"]
        for tool in order:
            try:
                sp = probe()
            except Exception as e:  # never let the probe kill the benchmark
                print(f"  probe failed: {e}", flush=True)
                sp = float("nan")
            r = run(tool, rnd, a)
            r["probe_mbps"] = sp
            results.append(r)
            if r["ok"]:
                print(f"r{rnd} {tool:6s} {r['time']:7.2f}s  sleep {r['sleep']:.0f}s  line {sp:6.1f} MB/s  "
                      f"{r['size'] / 2**20:8.1f} MiB", flush=True)
                if rnd == 1:
                    keep[tool] = r["path"]
                else:
                    shutil.rmtree(r["dir"], ignore_errors=True)

    ok = {t: {r["round"]: r for r in results if r["tool"] == t and r["ok"]} for t in ("ytd", "ytdlp")}
    rounds = sorted(set(ok["ytd"]) & set(ok["ytdlp"]))
    okruns = [r for r in results if r["ok"]]
    cleaned, replaced = clean_probes([r["probe_mbps"] for r in okruns])
    for r, c in zip(okruns, cleaned):
        r["probe_clean"] = c
    probes = cleaned
    pmean = st.mean(probes) if probes else float("nan")

    def norm(r):
        if math.isnan(pmean):
            return float("nan")
        return r["time"] * r["probe_clean"] / pmean

    summary = {"name": a.name, "url": a.url, "itags": a.itags, "format": a.format, "rounds_ok": len(rounds),
               "failures": [r for r in results if not r["ok"]],
               "retries": sum(1 for r in results if r.get("attempts", 1) > 1),
               "line_speed": describe(probes) if len(probes) > 1 else None,
               "probes_replaced": replaced}
    for t in ("ytd", "ytdlp"):
        rs = [ok[t][i] for i in rounds]
        summary[t] = {
            "raw": describe([r["time"] for r in rs]),
            "normalized": describe([norm(r) for r in rs]),
            "no_sleep": describe([r["time"] - r["sleep"] for r in rs]),
            "sleep": [r["sleep"] for r in rs],
            "throughput_mibs": describe([r["size"] / 2**20 / r["time"] for r in rs]),
            "size": rs[0]["size"] if rs else None,
        }
    pairs = [(ok["ytdlp"][i], ok["ytd"][i]) for i in rounds]
    summary["ratio_raw"] = ci_ratio([b["time"] / y["time"] for b, y in pairs])
    summary["ratio_normalized"] = ci_ratio([norm(b) / norm(y) for b, y in pairs]) if not math.isnan(pmean) else (float("nan"),) * 3
    summary["ratio_no_sleep"] = ci_ratio([(b["time"] - b["sleep"]) / y["time"] for b, y in pairs])
    summary["per_round"] = [{"round": i, "ytd": ok["ytd"][i]["time"], "ytdlp": ok["ytdlp"][i]["time"],
                             "ytdlp_sleep": ok["ytdlp"][i]["sleep"],
                             "probe_before_ytd": ok["ytd"][i]["probe_mbps"],
                             "probe_before_ytdlp": ok["ytdlp"][i]["probe_mbps"]} for i in rounds]
    if "ytd" in keep and "ytdlp" in keep:
        print("validating round-1 outputs...", flush=True)
        summary["validation"] = validate(keep["ytd"], keep["ytdlp"])

    path = os.path.join(a.root, a.name + ".json")
    with open(path, "w") as f:
        json.dump(summary, f, indent=1, default=str)

    def fmt(c):
        return f"{c[0]:.2f}x (95% CI {c[1]:.2f}-{c[2]:.2f})"
    print(f"\n== {a.name}: {len(rounds)} paired rounds")
    for t in ("ytd", "ytdlp"):
        s = summary[t]
        print(f"{t:6s} raw median {s['raw']['median']:.2f}s (mean {s['raw']['mean']:.2f} ± {s['raw']['sd']:.2f}, CV {100 * s['raw']['cv']:.1f}%)"
              f"  normalized median {(s['normalized'] or {}).get('median', float('nan')):.2f}s  no-sleep median {s['no_sleep']['median']:.2f}s"
              f"  {s['throughput_mibs']['median']:.1f} MiB/s")
    if summary["line_speed"]:
        ls = summary["line_speed"]
        print(f"line   {ls['mean']:.1f} MB/s mean, CV {100 * ls['cv']:.1f}% (min {ls['min']:.1f}, max {ls['max']:.1f}), "
              f"{replaced} probe readings replaced as outliers")
    print(f"speedup raw        {fmt(summary['ratio_raw'])}")
    print(f"speedup normalized {fmt(summary['ratio_normalized'])}")
    print(f"speedup no-sleep   {fmt(summary['ratio_no_sleep'])}")
    v = summary.get("validation")
    if v:
        print(f"validation: ffprobe equal={v['ffprobe_equal']} streamhash equal={v['streamhash_equal']} "
              f"packets={v['packets']} timestamp diffs={v['timestamp_diffs']} decode={v['decode_ytd']}")
    print(f"json: {path}")


if __name__ == "__main__":
    main()
