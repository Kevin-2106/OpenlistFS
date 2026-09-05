# OpenListFS

OpenListFS is currently a **read-only performance proof-of-concept** — a native
Windows virtual filesystem client for OpenList, optimized for large-file /
video playback scenarios.

**NOT SUPPORTED:**

- writes, uploads, rename, delete
- production use of any kind

The only goal of the current phase of this project is to verify whether the path

```
OpenList native API → direct URL / HTTP Range → custom read strategy → WinFsp → drive letter → mpv
```

can beat `rclone mount` and approach RaiDrive for video sequential reads and
seeks. If it cannot, the project stops — it is deliberately not growing into a
product. See [docs/research.md](docs/research.md) for measured numbers.

## Prerequisites

- Windows 11 x64
- [WinFsp](https://winfsp.dev/) installed (needed for `mount`; not needed for
  `probe`/`ls`/`stat`/`readtest`)
- Go 1.25+ (only to build from source)

## Configuration

Credentials come from the environment (or a `.env` file in the working
directory — see [.env.example](.env.example), never committed):

| Variable | Meaning |
|---|---|
| `OPENLISTFS_URL` | Base URL, e.g. `https://example.com` |
| `OPENLISTFS_TOKEN` | An API token, **or** |
| `OPENLISTFS_USERNAME` / `OPENLISTFS_PASSWORD` | Credentials; a token is obtained via `/api/auth/login` and refreshed automatically |

Tokens and signed URLs are never logged; direct-link query strings are
redacted in all diagnostics.

## Usage

```text
openlistfs probe          # server reachability + auth check
openlistfs ls /Movies     # list a directory
openlistfs stat /Movies/A.mkv
openlistfs readtest /Movies/A.mkv --offset 1GiB --length 8MiB --count 4
openlistfs mount O:       # mount the read-only view at a drive letter
```

`mount` flags:

| Flag | Default | Meaning |
|---|---|---|
| `--read-ahead` | `64MiB` | per-file prefetch window; `0` disables caching (Phase 2 baseline) |
| `--chunk-size` | `512KiB` | fetch/cache chunk granularity (foreground and prefetch share it, so work is never duplicated) |
| `--cache-size` | `256MiB` | total RAM cache cap (LRU) |
| `--prefetch-concurrency` | `3` | parallel prefetch fetches per file |
| `--metadata-ttl` | `2m` | directory listing / stat / direct-link cache lifetime |
| `--network-mode` | `true` | present the drive as a **network drive** (RaiDrive-style): Explorer uses its remote-share pipeline — lazy thumbnails, no timestamp writes, no eager scanning. `--network-mode=false` restores a fixed disk |
| `--debug` | off | per-request trace on stderr |

`mount` requires [WinFsp](https://winfsp.dev/). Press Ctrl+C to unmount
cleanly; if the process dies otherwise, the drive disappears with it (WinFsp
tears down mounts whose backing process is gone). On exit it prints session
statistics (reads, requests, TTFB, cache hit ratio, prefetch waste).

Note for Git Bash users: MSYS rewrites leading-slash arguments into Windows
paths (`/` → `C:/Program Files/Git`). Set `MSYS_NO_PATHCONV=1` or pass paths
without the leading slash (`openlistfs ls Movies`).

## Status

- Phase 0 (done): research, OpenList API client, `probe`/`ls`/`stat`.
- Phase 1 (done): `rangeio` (strict HTTP Range ReaderAt: 206/Content-Range
  validation, 200-at-offset>0 abort, EOF clamping, expired-link re-resolve,
  retry with logs) + `readtest` benchmark.
- Phase 2 (done): minimal read-only WinFsp mount via gofs. Content verified
  byte-identical to direct HTTP; writes rejected. Baseline showed WinFsp's
  32 KiB reads are latency-bound (~0.3 MiB/s playback) — see docs/research.md.
- Phase 3 (done): RAM block cache + seek-aware read-ahead. Same ffmpeg
  playback simulation: **decode-rate 0.3 → ~4 MiB/s equivalent; seek+decode
  14.7 s → 3.5 s; warm re-read 0 requests; realtime playback holds** on the
  ~3.6 MiB/s (server-capped) link. Cache-populating foreground loads +
  hit-reads-don't-disturb-the-stream were the two design decisions that
  mattered (details in docs/research.md).
- Next: the project's stop condition has been evaluated — see
  [docs/benchmark.md](docs/benchmark.md): seek parity with direct HTTP,
  ~2.2× faster than default rclone mount, realtime playback holds. Scope
  expansion is a user decision from here.
- Phase 4 (never, by design): writes, GUI, disk cache, product polish —
  unless the Phase 3 numbers justify continuing at all.
