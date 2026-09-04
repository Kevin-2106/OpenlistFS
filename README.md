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
```

`readtest` resolves the direct link, then issues HTTP Range requests and
discards the data: one bare probe printing HTTP status / `Content-Range` /
TTFB, followed by `--count` reads through `rangeio.Reader.ReadAt` — the exact
code path the WinFsp mount uses. Sizes accept `KiB`/`MiB`/`GiB` suffixes.

Note for Git Bash users: MSYS rewrites leading-slash arguments into Windows
paths (`/` → `C:/Program Files/Git`). Set `MSYS_NO_PATHCONV=1` or pass paths
without the leading slash (`openlistfs ls Movies`).

## Status

- Phase 0 (done): research, OpenList API client, `probe`/`ls`/`stat`.
- Phase 1 (done): `rangeio` (strict HTTP Range ReaderAt: 206/Content-Range
  validation, 200-at-offset>0 abort, EOF clamping, expired-link re-resolve,
  retry with logs) + `readtest` benchmark.
- Phase 2 (next): minimal WinFsp read-only mount (`openlistfs mount O:`).
- Phase 3 (planned): RAM cache + seek-aware read-ahead.
