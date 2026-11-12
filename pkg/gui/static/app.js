"use strict";

// The token is in the URL ytd opened; every API call must carry it.
const token = new URLSearchParams(location.search).get("token") || "";
const $ = (id) => document.getElementById(id);

const state = {
  info: null,          // last /api/info result
  url: "",
  format: "mp4",
  videoContainers: [],
  audioContainers: [],
  ffmpeg: true,
  ffmpegHint: "",
  jobs: new Map(),     // id -> job
};

function store(key, value) {
  try { localStorage.setItem("ytd." + key, value); } catch { /* storage may be blocked */ }
}
function recall(key) {
  try { return localStorage.getItem("ytd." + key); } catch { return null; }
}

async function api(path, body) {
  const opts = { headers: { "X-YTD-Token": token } };
  if (body !== undefined) {
    opts.method = "POST";
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(body);
  }
  let res;
  try {
    res = await fetch(path, opts);
  } catch {
    throw new Error("Could not reach ytd. Is it still running?");
  }
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || `Request failed (HTTP ${res.status})`);
  return data;
}

function clock(secs) {
  if (!secs) return "";
  const h = Math.floor(secs / 3600), m = Math.floor(secs / 60) % 60, s = secs % 60;
  const pad = (n) => String(n).padStart(2, "0");
  return h ? `${h}:${pad(m)}:${pad(s)}` : `${m}:${pad(s)}`;
}

function show(el, on) { el.hidden = !on; }

function setError(el, msg) {
  el.textContent = msg || "";
  show(el, !!msg);
}

// ---- Fetch -----------------------------------------------------------------

async function fetchInfo(url, playlist) {
  const btn = $("fetch");
  btn.disabled = true;
  btn.textContent = "Fetching…";
  setError($("fetch-error"), "");
  try {
    const info = await api("/api/info", { url, playlist });
    state.info = info;
    state.url = url;
    renderResult();
  } catch (e) {
    setError($("fetch-error"), e.message);
  } finally {
    btn.disabled = false;
    btn.textContent = "Fetch";
  }
}

$("fetch-form").addEventListener("submit", (e) => {
  e.preventDefault();
  const url = $("url").value.trim();
  if (url) fetchInfo(url, false);
});

$("use-list").addEventListener("click", () => fetchInfo(state.url, true));

// ---- Result ----------------------------------------------------------------

function isAudio(c) { return state.audioContainers.includes(c); }

function renderResult() {
  const info = state.info;
  show($("result"), !!info);
  if (!info) return;
  const isList = info.kind === "playlist";

  show($("video-head"), !isList);
  show($("list-box"), isList);
  show($("quality"), !isList);
  show($("subfolder-row"), isList);

  if (isList) {
    $("list-title").textContent = info.title || "Playlist";
    let meta = `${info.entries.length} videos`;
    if (info.skipped) meta += ` · ${info.skipped} private or deleted left out`;
    $("list-meta").textContent = meta;
    const ol = $("entries");
    ol.replaceChildren();
    info.entries.forEach((e, i) => {
      const li = document.createElement("li");
      const label = document.createElement("label");
      const box = document.createElement("input");
      box.type = "checkbox";
      box.checked = true;
      box.dataset.index = i;
      box.addEventListener("change", updateSummary);
      const n = document.createElement("span");
      n.className = "n";
      n.textContent = i + 1;
      const t = document.createElement("span");
      t.className = "t";
      t.textContent = e.title;
      t.title = e.title;
      const d = document.createElement("span");
      d.className = "d";
      d.textContent = clock(e.duration);
      label.append(box, n, t, d);
      li.append(label);
      ol.append(li);
    });
  } else {
    $("thumb").src = info.thumbnail;
    $("video-title").textContent = info.title;
    $("video-meta").textContent = [info.author, clock(info.duration)].filter(Boolean).join(" · ");
    show($("use-list"), !!info.inList);
  }
  renderFormats();
  updateSummary();
}

function renderFormats() {
  const box = $("formats");
  box.replaceChildren();
  const add = (c) => {
    const b = document.createElement("button");
    b.type = "button";
    b.textContent = c;
    b.setAttribute("role", "radio");
    b.setAttribute("aria-checked", String(c === state.format));
    b.title = isAudio(c) ? "Audio only" : "Video with audio";
    b.addEventListener("click", () => {
      state.format = c;
      store("format", c);
      renderFormats();
    });
    box.append(b);
  };
  state.videoContainers.forEach(add);
  const sep = document.createElement("span");
  sep.className = "sep";
  box.append(sep);
  state.audioContainers.forEach(add);

  const audio = isAudio(state.format);
  show($("h264-row"), !audio && state.format !== "webm");
  if (audio || state.format === "webm") $("h264").checked = false;
  renderQualities();
  renderFFmpegWarning();
}

function renderQualities() {
  const info = state.info;
  if (!info || info.kind !== "video") return;
  const c = state.format;
  const h264 = $("h264").checked;
  const fill = (select, list, keep) => {
    const prev = select.value;
    select.replaceChildren(new Option("Best available", "0"));
    for (const f of list) {
      if (keep(f)) select.append(new Option(f.label, String(f.itag)));
    }
    if ([...select.options].some((o) => o.value === prev)) select.value = prev;
  };
  show($("video-q-field"), !isAudio(c));
  fill($("video-q"), info.video, (f) => h264 || f.fits.includes(c));
  // mp3 re-encodes any audio; everything else copies the stream as is.
  fill($("audio-q"), info.audio, (f) => c === "mp3" || f.fits.includes(c));
}

$("h264").addEventListener("change", () => { renderQualities(); renderFFmpegWarning(); });

function needsFFmpeg() { return state.format === "mp3" || $("h264").checked; }

function renderFFmpegWarning() {
  const warn = $("ffmpeg-warn");
  if (needsFFmpeg() && !state.ffmpeg) {
    warn.textContent = `${state.format === "mp3" ? "MP3" : "H.264"} needs ffmpeg, which was not found. Install it with “${state.ffmpegHint}”, then restart ytd.`;
    show(warn, true);
  } else {
    show(warn, false);
  }
}

function selectedEntries() {
  const info = state.info;
  if (info.kind !== "playlist") return [{ id: info.id, title: info.title }];
  return [...$("entries").querySelectorAll("input:checked")].map((b) => info.entries[+b.dataset.index]);
}

function updateSummary() {
  if (!state.info) return;
  const n = selectedEntries().length;
  $("dl-summary").textContent = state.info.kind === "playlist" ? `${n} of ${state.info.entries.length} selected` : "";
  $("download").disabled = n === 0;
}

function setAll(on) {
  $("entries").querySelectorAll("input").forEach((b) => { b.checked = on; });
  updateSummary();
}
$("select-all").addEventListener("click", () => setAll(true));
$("select-none").addEventListener("click", () => setAll(false));

$("outdir").addEventListener("change", () => store("outdir", $("outdir").value.trim()));

$("download").addEventListener("click", async () => {
  const info = state.info;
  const items = selectedEntries();
  const btn = $("download");
  btn.disabled = true;
  setError($("dl-error"), "");
  const body = {
    items,
    format: state.format,
    h264: $("h264").checked,
    outputDir: $("outdir").value.trim(),
    folder: info.kind === "playlist" && $("subfolder").checked ? info.title : "",
  };
  if (info.kind === "video") {
    body.videoItag = isAudio(state.format) ? 0 : +$("video-q").value;
    body.audioItag = +$("audio-q").value;
  }
  try {
    const res = await api("/api/download", body);
    $("dl-summary").textContent = `Added ${res.jobs.length} to the queue · ${res.folder}`;
  } catch (e) {
    setError($("dl-error"), e.message);
  } finally {
    btn.disabled = false;
  }
});

// ---- Queue -----------------------------------------------------------------

const statusText = {
  queued: "Queued",
  downloading: "Downloading",
  converting: "Converting",
  saving: "Saving",
  saved: "Saved",
  failed: "Failed",
  cancelled: "Cancelled",
};
const finished = (j) => j.status === "saved" || j.status === "failed" || j.status === "cancelled";

function jobElement(job) {
  let li = document.getElementById("job-" + job.id);
  if (li) return li;
  li = document.createElement("li");
  li.id = "job-" + job.id;
  li.className = "job";
  li.innerHTML = `
    <div class="job-top"><span class="job-title"></span><span class="job-status"></span></div>
    <div class="bar"><i></i></div>
    <div class="job-sub"><span class="detail"></span><button class="ghost cancel" type="button">Cancel</button><button class="ghost open" type="button">Show in folder</button></div>
    <p class="err" hidden></p>`;
  li.querySelector(".cancel").addEventListener("click", () => api("/api/cancel", { id: job.id }).catch(() => {}));
  li.querySelector(".open").addEventListener("click", () => api("/api/open", { id: job.id }).catch((e) => alert(e.message)));
  $("queue").append(li);
  return li;
}

function renderJob(job) {
  const li = jobElement(job);
  li.dataset.status = job.status;
  const title = li.querySelector(".job-title");
  title.textContent = job.title;
  title.title = job.group ? `${job.title} — ${job.group}` : job.title;
  li.querySelector(".job-status").textContent = `${statusText[job.status] || job.status} · ${job.format}`;
  li.querySelector(".bar > i").style.width = `${Math.round((job.progress || 0) * 100)}%`;
  li.querySelector(".detail").textContent = job.detail || "";
  show(li.querySelector(".cancel"), !finished(job));
  show(li.querySelector(".open"), job.status === "saved" && !!job.path);
  setError(li.querySelector(".err"), job.status === "failed" ? job.error : "");
}

function renderQueueMeta() {
  const jobs = [...state.jobs.values()];
  show($("queue-empty"), jobs.length === 0);
  const saved = jobs.filter((j) => j.status === "saved").length;
  const failed = jobs.filter((j) => j.status === "failed").length;
  let text = jobs.length ? `${saved} of ${jobs.length} saved` : "";
  if (failed) text += ` · ${failed} failed`;
  $("queue-count").textContent = text;
  show($("cancel-all"), jobs.some((j) => !finished(j)));
  show($("clear"), jobs.some(finished));
}

function resetJobs(list) {
  state.jobs = new Map(list.map((j) => [j.id, j]));
  $("queue").replaceChildren();
  list.forEach(renderJob);
  renderQueueMeta();
}

$("cancel-all").addEventListener("click", () => api("/api/cancel", { id: "" }).catch(() => {}));
$("clear").addEventListener("click", () => api("/api/clear", {}).catch(() => {}));

function connectEvents() {
  const es = new EventSource("/api/events?token=" + encodeURIComponent(token));
  es.addEventListener("open", () => show($("offline"), false));
  es.addEventListener("reset", (e) => resetJobs(JSON.parse(e.data) || []));
  es.addEventListener("job", (e) => {
    const job = JSON.parse(e.data);
    state.jobs.set(job.id, job);
    renderJob(job);
    renderQueueMeta();
  });
  es.addEventListener("error", () => show($("offline"), true));
}

// ---- Start -----------------------------------------------------------------

$("quit").addEventListener("click", async () => {
  if ([...state.jobs.values()].some((j) => !finished(j)) &&
      !confirm("Downloads are still running. Stop ytd anyway?")) return;
  await api("/api/quit", {}).catch(() => {});
  document.body.innerHTML = '<main><section class="card"><h1>ytd stopped</h1><p class="dim">You can close this tab.</p></section></main>';
});

async function init() {
  if (!token) {
    setError($("fetch-error"), "This page must be opened from ytd (run “ytd gui”).");
    return;
  }
  try {
    const s = await api("/api/state");
    $("version").textContent = s.version ? "v" + s.version : "";
    state.videoContainers = s.videoContainers;
    state.audioContainers = s.audioContainers;
    state.ffmpeg = s.ffmpeg;
    state.ffmpegHint = s.ffmpegHint;
    const fmt = recall("format");
    if (fmt && (s.videoContainers.includes(fmt) || s.audioContainers.includes(fmt))) state.format = fmt;
    $("outdir").value = recall("outdir") || s.outputDir;
    resetJobs(s.jobs || []);
  } catch (e) {
    setError($("fetch-error"), e.message);
    return;
  }
  connectEvents();
  $("url").focus();
}

init();
