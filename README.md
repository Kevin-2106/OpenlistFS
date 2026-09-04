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
```

Note for Git Bash users: MSYS rewrites leading-slash arguments into Windows
paths (`/` → `C:/Program Files/Git`). Set `MSYS_NO_PATHCONV=1` or pass paths
without the leading slash (`openlistfs ls Movies`).

## Status

- Phase 0 (done): research, OpenList API client, `probe`/`ls`/`stat`.
- Phase 1 (next): `readtest` — HTTP Range benchmark against the resolved
  direct URL.
- Phase 2 (planned): minimal WinFsp read-only mount (`openlistfs mount O:`).
- Phase 3 (planned): RAM cache + seek-aware read-ahead.
