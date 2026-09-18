# OpenListFS

OpenListFS is a native Windows virtual filesystem client for OpenList,
optimized for large-file / video playback scenarios. It mounts **read-only
by default**; an opt-in writable mode exists (see below).

**Writable mode** (`--writable` / GUI checkbox, off by default):

- New and existing files opened for writing are buffered in a local spool
  file (overlay: written ranges from spool, untouched ranges from remote)
  and uploaded via `PUT /api/fs/put` on close — OpenList only supports
  whole-file upload, so in-place random writes trigger a merged re-upload
  of the entire file at close.
- mkdir / rename / remove map to the OpenList APIs directly.
- Server permissions still apply. Permission bits needed (OpenList user
  model): write content (bit 3) for mkdir/upload, rename (bit 4),
  remove (bit 7). Ops the account cannot perform surface as errors —
  e.g. uploads into freshly created subdirectories may be denied by the
  server depending on its configuration. Writes also become visible to
  re-opens only after OpenList's own metadata cache refreshes (~1 s).

**Proxy support** (`--proxy` / GUI dropdown / `OPENLISTFS_PROXY`): all
traffic to the server — JSON API calls **and** direct-URL file downloads —
can go through `none` (default, direct), `system` (the Windows per-user
system proxy from the WinINET registry, the one Clash/v2rayN set, falling
back to `HTTP(S)_PROXY` env vars) or a specific URL
(`http://127.0.0.1:7897`, `socks5://host:port`; a bare `host:port` means
http). The setting is fixed per mount.

**WebDAV backend** (`--webdav` / GUI dropdown, experimental): instead of
the OpenList native API the mount can speak plain WebDAV — PROPFIND for
metadata, authenticated GET + HTTP Range for content, MKCOL / DELETE /
MOVE / PUT for the writable mode (a true streaming upload, unlike the
native API's whole-file PUT). The same URL/username/password from `.env`
is used; a server URL without a path gets `/dav` appended (OpenList's
convention), a URL with a path is used as-is — so any WebDAV server
(Nextcloud, 坚果云, …) works too. Token-only configurations cannot
authenticate to WebDAV.

**S3 backend** (`--s3` / GUI dropdown, experimental): mount one bucket of
any S3-compatible store (AWS S3, MinIO, Cloudflare R2, …). The server URL
carries endpoint and bucket — `https://s3.us-west-2.amazonaws.com/mybucket`
or `http://127.0.0.1:9000/media`; username/password are the access key /
secret key (empty = anonymous read-only). The region defaults to
`us-east-1`, is inferred from `s3.<region>.amazonaws.com` hosts, and can
be forced with `?region=` on the URL. Metadata comes from
ListObjectsV2/HEAD, content from presigned GETs + HTTP Range, and the
writable mode maps to PUT/DELETE/server-side COPY (renaming directories
is rejected — S3 has no prefix rename). SigV4 is implemented in-package
(no AWS SDK dependency) and the signer is validated against the worked
example in the AWS documentation.

**Still NOT SUPPORTED:** production use, crash consistency (uploads happen
at close; a crash loses the pending changes), multi-writer coordination.

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
| `OPENLISTFS_PROXY` | `none` (default) / `system` / a proxy URL — routes API calls **and** file downloads |

Tokens and signed URLs are never logged; direct-link query strings are
redacted in all diagnostics.

## Usage — GUI

```text
wails build            # requires Node/npm + Wails CLI (go install github.com/wailsapp/wails/v2/cmd/wails@latest)
./build/bin/openlistfs-gui.exe
```

The GUI wraps the same mounter: pick a drive letter, a backend (OpenList
API, WebDAV or S3) and a proxy mode, press Mount. It shows live session
statistics (requests, TTFB, cache hit ratio, prefetch waste) polled from
each running mount.

Features beyond the CLI:
- **Multiple simultaneous mounts**: one session per drive letter — change
  the letter (and/or the active profile) and press Mount again to add
  another drive; each keeps its own cache and statistics card with its
  own Unmount. (The CLI mounts one drive per process — run several
  processes for several drives.)
- **System tray**: closing the window hides to tray; tray menu has
  show, mount/unmount of the configured drive, unmount-all and quit
  (quit unmounts everything first).
- **Multiple profiles**: save several server configurations
  (`profiles.json` next to the exe — plain-text passwords, never commit
  it); the active profile is synced to `.env` for the CLI.
- **Boot auto-start**: per-user `HKCU\...\Run` entry launching
  `--minimized --automount` (starts in tray, mounts the active profile).

## Usage — CLI

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
| `--webdav` | off | speak WebDAV instead of the OpenList native API (`/dav` appended to a path-less server URL; uses username/password) |
| `--s3` | off | mount one S3 bucket instead of the OpenList native API (server URL = `https://endpoint/bucket`; username/password = access key/secret; region default `us-east-1`, override with `?region=`) |
| `--proxy` | `OPENLISTFS_PROXY`, else `none` | `none` / `system` / a proxy URL (`http://127.0.0.1:7897`, `socks5://host:port`) for all server traffic |
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
- Stop condition evaluated — see [docs/benchmark.md](docs/benchmark.md):
  seek parity with direct HTTP, ~2.2× faster than default rclone mount,
  realtime playback holds.
- GUI (user-approved scope expansion): Wails v2 + Vue 3 desktop app
  (mount/unmount, options, live session statistics) sharing the same
  `internal/mounter` core as the CLI.
- Writable mode, proxy support (none/system/URL over both HTTP channels)
  and an experimental WebDAV backend (`--webdav`) — see above. Live-verified
  against the test server: WebDAV PROPFIND/GET-Range/PUT round-trips,
  full-file hash parity between the two backends.
- Experimental S3 backend (`--s3`): in-package SigV4 (validated against
  the AWS documentation's signing example), verified against an in-test
  fake S3 (listing/pagination, unicode and reserved-character keys,
  presigned ranged GET, PUT/COPY/DELETE round-trips, auth rejection);
  not yet live-verified against a real provider.
- Phase 4 (never, by design): writes, GUI, disk cache, product polish —
  unless the Phase 3 numbers justify continuing at all.
