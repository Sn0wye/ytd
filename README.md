# ytd

Download YouTube videos and playlists from the terminal or from a simple
graphical interface. You choose the video quality and the audio quality from a
list, and ytd hands you back one file in the format you asked for.

- **No login, no API key, no cookies.**
- **No other programs needed.** Extraction, parallel download and muxing are
  plain Go. ffmpeg is only used for the two cases that need re-encoding (mp3
  and `--h264`).
- **Lossless.** Streams are copied as YouTube serves them, never re-encoded.
- **Fast.** About twice as fast as yt-dlp on the same streams (see below).

## Install

```sh
brew tap sn0wye/tap
brew trust sn0wye/tap   # Homebrew 7+ refuses casks from untrusted taps
brew install ytd
```

Or with curl (macOS and Linux):

```sh
curl -fsSL https://raw.githubusercontent.com/Sn0wye/ytd/main/install.sh | sh
```

Or with Go:

```sh
go install github.com/Sn0wye/ytd@latest
```

Windows: download the zip from [Releases](https://github.com/Sn0wye/ytd/releases).
Double-click `ytd.exe` to open the [graphical interface](#graphical-interface),
or run it from a terminal.

## Usage

```sh
ytd down 'https://www.youtube.com/watch?v=aqz-KE-bpKQ'    # pick quality, get an mp4
ytd down aqz-KE-bpKQ                                      # same, video ID only
ytd down https://youtu.be/aqz-KE-bpKQ --best              # no prompts, best quality
ytd down <url> --format webm                              # VP9/AV1 + Opus
ytd down <url> --format mkv                               # any codec combination
ytd down <url> --audio-only                               # m4a (AAC)
ytd down <url> --format opus                              # Opus audio
ytd down <url> --format mp3                               # needs ffmpeg
ytd down <url> --h264                                     # VP9/AV1 -> H.264, needs ffmpeg
ytd down 'https://www.youtube.com/playlist?list=PL...' --format mp3   # a whole playlist
ytd gui                                                   # graphical interface
```

Accepted inputs: `watch?v=`, `youtu.be/`, `shorts/`, `embed/`, `live/`,
`music.youtube.com` links, or a bare 11-character video ID. For playlists:
`playlist?list=` links (also on `music.youtube.com`) or a bare playlist ID.

Quote URLs that contain `?` or `&`. Without quotes, zsh treats `?` as a
filename pattern and stops with `no matches found` before ytd runs, and any
shell treats `&` as "run in background". To paste `watch?v=` links unquoted
in zsh, add this to `~/.zshrc`:

```sh
alias ytd='noglob ytd'
```

| Flag | Default | |
|---|---|---|
| `--format` | `mp4` (`m4a` with `-a`) | `mp4`, `mkv`, `webm`, `m4a`, `opus`, `mp3` |
| `-a, --audio-only` | | audio only |
| `-b, --best` | | skip the prompts, pick the best quality |
| `-i, --itag` | | skip the prompts, pick streams by itag: `VIDEO+AUDIO` (e.g. `337+251`), or `AUDIO` with `-a`. Itags are shown in the selector |
| `-o, --output-dir` | `~/Downloads` | where the file goes |
| `-f, --filename` | slugified title | output name; a known extension also sets `--format` |
| `-c, --connections` | `8` | parallel connections |
| `--h264` | | re-encode to H.264 for old players |
| `-t, --tmp-dir` | system temp | where streams are stored before muxing |
| `--playlist` | | with a `watch?v=…&list=…` URL, download the whole playlist instead of the one video |
| `--items` | all | playlist positions to download, e.g. `1-10,15` or `20-` |
| `--no-playlist-dir` | | save playlist videos directly in `--output-dir` instead of a sub-folder |

The selectors only list streams that fit the chosen format without
re-encoding:

| Format | Video | Audio |
|---|---|---|
| mp4 | H.264, AV1 | AAC |
| webm | VP9, AV1 | Opus |
| mkv | H.264, AV1, VP9 | AAC, Opus |
| m4a | | AAC |
| opus | | Opus |

Want 1440p/4K VP9 or Opus audio? Use `--format webm` or `--format mkv`.

Output files get title and artist metadata and never overwrite an existing file.

### Playlists

```sh
ytd down 'https://www.youtube.com/playlist?list=PL_Q15fKxrBb4pxTp2zQm6zZU3-h4dlm05' --format mp3
ytd down 'https://www.youtube.com/watch?v=LYsaKn8FRhc&list=PL_Q15fKxrBb4pxTp2zQm6zZU3-h4dlm05' --playlist --items 1-10
```

- Every video gets the best quality that fits the format, without prompts
  (`--itag` and `--filename` cannot be used, since they are per video).
- Files go into a folder named after the playlist inside `--output-dir`.
- Videos already saved in that folder are skipped, so running the same
  command again resumes an interrupted playlist.
- A video that fails does not stop the others. A stream URL that expires
  (HTTP 403) is retried once with fresh URLs. At the end ytd prints how many
  were saved, skipped and failed, lists the failed ones and exits with 1 if
  any failed.
- A `watch?v=…&list=…` link still downloads just that video unless you add
  `--playlist`. Mixes (`list=RD…`) are generated per viewer and cannot be
  downloaded as a playlist.

## Graphical interface

```sh
ytd gui                      # opens the interface in your browser
ytd gui --port 8765 --no-open
```

`ytd gui` starts a small web page served by ytd itself and opens it in your
default browser. Paste a video or playlist link, pick the format (and, for a
single video, the video and audio quality), choose the folder and follow the
downloads in the queue, with cancel and "show in folder" buttons. On Windows,
double-clicking `ytd.exe` does the same.

It needs no extra program or library: the page is built into the ytd binary.
ytd stops when you press Ctrl+C, click Quit, or about 30 seconds after the
last ytd tab is closed with nothing left to download.

The server only listens on `127.0.0.1`. Every request must carry the random
token that is part of the address ytd opens, and requests with another
`Host` or `Origin` are refused, so other websites cannot use it to download
files to your computer.

## How it works

1. **Extraction** (`pkg/youtube`), ported from yt-dlp's YouTube extractor.
   ytd gets an anonymous visitor ID from `youtube.com/sw.js_data` (3 KB, where
   yt-dlp downloads the 1 MB+ watch page) and calls the InnerTube `player`
   API as the visionOS client. yt-dlp ships that client as its "no JS runtime"
   default: it needs no PO token and returns stream URLs without signature or
   `n` challenges, so there is no player JavaScript to download and run.
2. **Download**: both streams are fetched at once, split into 10 MB ranges
   (yt-dlp's chunk size for YouTube) over 8 HTTP/1.1 connections, written in
   place with retries.
3. **Muxing** (`pkg/media`): fragmented MP4 and WebM demuxers, and writers for
   progressive MP4/M4A (moov first, edit lists kept for B-frames, iTunes
   tags), Matroska/WebM (with Cues for seeking) and Ogg Opus. AV1 temporal
   delimiters are dropped like ffmpeg does. The packets and timestamps match
   what `yt-dlp` + ffmpeg produce for the same streams.

## Benchmark

Method (`bench/bench.py`): both tools get the same streams (`ytd --itag V+A`
vs `yt-dlp --ignore-config -f V+A --merge-output-format …`) and run strictly
one at a time. Each round runs both back to back, alternating which goes
first, and the result is the per-round ratio `t_yt-dlp / t_ytd`: network
drift hits both runs of a pair alike, so the ratio cancels most of it. The
speedup is the geometric mean of 7 ratios with a 95% confidence interval. As a
cross-check, a googlevideo probe (80 MiB, 4 connections, a stream not under
test) before every run normalises each time to the mean line speed.
yt-dlp 2026.10.26 waits 4–6 s per run ("Sleeping … as required by the
site"), so the speedup is also shown with that wait removed. Times are wall
clock, including extraction and merging.

[b8wsjx7dFwM](https://www.youtube.com/watch?v=b8wsjx7dFwM) (6:00, 4K60 SDR,
3840x1600), best stream per codec plus the best audio for the container:

| Codec → container | Streams | ytd median | yt-dlp median | Speedup (95% CI) | Normalised | Without yt-dlp's sleep |
|---|---|---|---|---|---|---|
| H.264 1080p60 → mp4 | 299 + 140 AAC, 179 MiB | 3.6 s | 12.2 s | **3.46x** (3.19–3.75) | 3.33x (2.98–3.72) | 2.14x (1.93–2.36) |
| VP9 2160p60 → webm | 315 + 251 Opus, 603 MiB | 10.2 s | 23.9 s | **2.34x** (2.04–2.67) | – | 1.90x (1.63–2.23) |
| AV1 2160p60 → mp4 | 401 + 140 AAC, 627 MiB | 14.0 s | 30.0 s | **2.10x** (1.90–2.31) | 2.00x (1.61–2.49) | 1.79x (1.62–1.97) |

[LXb3EKWsInQ](https://www.youtube.com/watch?v=LXb3EKWsInQ) (5:14, 4K60 HDR10),
3 rounds, before the paired method:

| Codec → container | Streams | ytd median | yt-dlp median |
|---|---|---|---|
| H.264 1080p60 → mp4 | 299 + 140 AAC | 3.6 s | 12.4 s |
| VP9 2160p60 HDR → webm | 337 + 251 Opus | 18.1 s | 37.8 s |
| AV1 2160p60 HDR → webm | 701 + 251 Opus | 14.8 s | 37.6 s |

Every interval excludes 1.0. ytd's run-to-run variation (CV) was 4–11%,
yt-dlp's 7–11%. In every case the packets and timestamps of both outputs are
identical (`ffmpeg -f streamhash`, `-f framecrc`), and so are codec, colour
and HDR10 metadata (`ffprobe`). `yt-dlp -N 8` changed little (1–5%) because
it parallelises fragmented streams, not these plain HTTPS ones.

The VP9 normalised figure is missing because the line probe failed during
that run (rate limited). Heavy benchmarking eventually gets the IP flagged by
YouTube ("Sign in to confirm you're not a bot") for both tools; space the runs out.

## Limitations

- No 5.1 audio (those streams come from YouTube clients that need the player
  JavaScript) and no live streams.
- Age-restricted and members-only videos need a login, which ytd does not do.
- "Made for kids" videos are not served to the visionOS client.
- Private, deleted and members-only videos of a playlist are left out.
- YouTube changes its clients from time to time. If downloads start failing,
  compare `pkg/youtube/client.go` with `INNERTUBE_CLIENTS` in yt-dlp's
  `yt_dlp/extractor/youtube/_base.py`.
