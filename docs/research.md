# OpenListFS Research

Date: 2026-09-05 · Phase 0 research, verified against the live OpenList server and go-winfsp v1.0.3 source.

## Toolchain

| Item | Value |
|---|---|
| Go | go1.25.0 (auto-downloaded toolchain; host `go` was 1.22.0) |
| Module | `openlistfs`, `go 1.25.0` in go.mod (go-winfsp requires >= 1.25) |
| go-winfsp | `github.com/winfsp/go-winfsp v1.0.3` (latest tag; deps: pkg/errors, golang.org/x/sys) |
| WinFsp | Installed at `C:\Program Files (x86)\WinFsp` (registry `HKLM\SOFTWARE\WOW6432Node\WinFsp\InstallDir`) |

Network note (this dev machine): `proxy.golang.org` unreachable directly; use
`GOPROXY=https://goproxy.cn,direct` for modules and a local proxy
(`HTTPS_PROXY=http://127.0.0.1:7897`) for the Go toolchain download.

## go-winfsp / gofs findings (from module source, v1.0.3)

The repo ships a native binding (`winfsp` package) plus a high-level adapter (`gofs`).
The official `examples/passthrough` mounts like:

```go
fs, err := winfsp.Mount(gofs.New(myFS), "X:", opts...)
defer fs.Unmount()
// wait for Ctrl+C
```

### gofs.FileSystem interface

```go
type FileSystem interface {
    OpenFile(name string, flag int, perm os.FileMode) (File, error)
    Mkdir(name string, perm os.FileMode) error
    Stat(name string) (os.FileInfo, error)
    Rename(source, target string) error
    Remove(name string) error
}
```

### gofs.File interface

```go
type File interface {
    io.ReadWriteCloser      // Read/Write used rarely; ReadAt is the hot path
    io.ReaderAt             // <- WinFsp Read(offset,length) maps DIRECTLY to ReadAt
    io.WriterAt
    io.Seeker
    Readdir(count int) ([]os.FileInfo, error)  // directories; gofs reopens via OpenFile(O_RDONLY) and calls Readdir(-1)
    Stat() (os.FileInfo, error)
    Sync() error
    Truncate(size int64) error
}
```

**Key fact: `gofs` maps WinFsp `Read(buf, offset)` straight to `File.ReadAt(buf, offset)`
(gofs_windows.go:858-873). A `ReadAt`-shaped HTTP Range reader slots in natively — no need
to sink to the native binding for Phase 2.**

### Path format arriving at our FileSystem

`pathlock` normalizes every name before it reaches the inner filesystem:
`path.Clean(path.Join("/", filepath.ToSlash(p)))`, then back via `filepath.FromSlash`
(`Lock.FilePath()`), so on Windows we receive **backslash paths with a leading backslash**:
root = `\`, file = `\Movies\A.mkv`. We must convert centrally to OpenList `/Movies/A.mkv`.

### Error → NTSTATUS mapping (host_windows.go:69-118)

Recognized: `syscall.ENOENT→OBJECT_NAME_NOT_FOUND`, `syscall.EEXIST`, `syscall.EPERM` /
`os.ErrPermission→ACCESS_DENIED`, `syscall.ENOTDIR`, `syscall.EISDIR`, `syscall.EINVAL`,
plus `io.EOF→END_OF_FILE`. Anything else → `STATUS_INTERNAL_ERROR`. Our read-only FS returns
`syscall.EPERM` for all mutations and `syscall.ENOENT` for missing paths.

### Read-only support

`gofs.NewOptions(fs, gofs.WithAttribReadOnlyTransMode(gofs.AttribReadOnlyAlways))` sets
`FILE_ATTRIBUTE_READONLY` on all files. We additionally reject any `OpenFile` carrying
write/create flags and implement `Mkdir/Remove/Rename` as `syscall.EPERM`.

### Mount options

`winfsp.CaseSensitive(bool)`, `winfsp.Debug(bool)`, `winfsp.VolumePrefix(string)`,
`winfsp.FileSystemName(string)`, `winfsp.SectorSize(sector, perUnit uint16)`,
`winfsp.Attributes(uint32)`, `winfsp.PassPattern(bool)`, `winfsp.CreationTime(time)`.
Default sector 512/1. `Mount` starts the dispatcher internally; unmount via `(*FileSystem).Unmount()`.
go-winfsp is MIT licensed.

### Limitations / notes

- `File.Stat()` is called on every `GetFileInfo`; it must answer from cached metadata
  (no network call) or Windows will stall on it.
- `FileSystem.Stat(name)` is called by `GetSecurityByName` on every open/create —
  one network metadata call per open is acceptable.
- Directory enumeration: gofs calls `OpenFile(dir, O_RDONLY)` then `Readdir(-1)` once per
  enumeration and caches into a WinFsp DirBuffer.
- No case-insensitive name resolution is provided by gofs; we keep exact-name matching
  (OpenList backends are case-sensitive). Mount flag stays default (case-insensitive flag
  off is a *hint* to Windows; our lookup is exact).

## OpenList API (verified live against alist.nekomata.serika.site, v4.2.x)

Envelope for every endpoint: `{"code": 200, "message": "success", "data": <…>}` —
**logical errors return HTTP 200 with `code != 200` in the body** (e.g. `code=403`,
`"You are not an admin"`).

Authentication: `POST /api/auth/login` with JSON `{"username","password"}` →
`data.token` (JWT, ~204 chars). Send as header `Authorization: <token>` (no `Bearer` prefix).
`GET /api/public/settings` is anonymous and usable for `probe`.

### /api/fs/list — POST JSON `{path, password?, page, per_page, refresh}`

`data`: `{content: [...], total, readme, header, provider, write, write_content_bypass}`.
Content item: `{name, size, is_dir, modified, created, sign, thumb, type, hash_info, hashinfo}`.
`size`/`modified` are present for files; paginate until `total` reached.

### /api/fs/get — POST JSON `{path, password?}`

`data`: `{name, size, is_dir, modified, created, sign, thumb, type, hash_info, hashinfo,
raw_url, header, provider, related, readme}`.
`raw_url` is the resolved direct URL; `header` is *usually* an object of extra request
headers but this server returns `""` (empty string) for the Local driver — parse both.
`provider` names the storage driver ("Local" here).

### /api/fs/link — **admin-only on current OpenList**

`POST {path}` returned `code=403, "You are not an admin"` for our minimal-privilege user.
(AList v3 allowed any authenticated user; OpenList restricts it.) **Consequence: link
resolution uses `/api/fs/get`'s `raw_url`.** The client still tries `/api/fs/link` first
and permanently falls back to `fs/get` when the server denies it, so an admin account
would get the cheaper path.

### Direct URL behavior (Local driver via `/p/<user>/<path>?sign=…` proxy URL)

Verified with live Range requests against a 1.19 GB MKV:

| Test | Result |
|---|---|
| `Range: bytes=1G-…`, no auth header | **206**, correct `Content-Range: bytes a-b/total`, `Accept-Ranges: bytes` |
| end beyond EOF (`bytes=(size-16)-(size+100000)`) | 206, truncated to 16 bytes |
| offset beyond EOF | **416** `invalid range: failed to overlap` |
| HEAD + Range | 206 (works, but we don't rely on HEAD) |

The `sign` query parameter (`…:0` = no expiry) authenticates the URL; no extra headers needed.
raw_url must be used as-is (already percent-encoded).

### WinFsp read-pattern trace (Phase 2 baseline, 2026-09-05)

Traced with `--debug` while ffmpeg decoded 5 s of a 1080p MKV through the mount:

- **358 HTTP Range requests for ~5 MB of data**; 61 % were exactly 32 KiB
  (WinFsp cache-manager granularity), the rest odd sizes from the MKV
  demuxer (index probing, interleaved A/V).
- Every request pays a full HTTP round trip (~60–150 ms on this link), so
  playback workloads collapse to **~0.2–0.5 MiB/s** — below the ~1 MiB/s a
  1080p BluRay stream needs in real time.
- With 1 MiB reads the same mount sustains **3.66 MiB/s** — identical to the
  direct-HTTP ceiling. The gofs/WinFsp layers add no measurable throughput
  overhead; **request latency x request count is the only enemy**.
- Deeper trace finding: the MKV demuxer does not read sequentially at all —
  it **alternates between the first-cluster region (offset ~0) and the Cues
  index (~1.1 GB away) with 32 KiB reads** while opening, then interleaves
  A/V clusters. Any design that only accelerates one sequential stream will
  fail this workload.
- Mount lifecycle: killing the backing process auto-unmounts the drive
  (no ghost mounts observed); Ctrl+C path cancels context then Unmounts.

### Phase 3 results (read-ahead + RAM block cache, 2026-09-05)

Design that survived contact with the workload:

1. Cache-populating foreground: a missed read loads its **whole 4 MiB block**
   synchronously (singleflight — concurrent/prefetch loads are shared), so
   repeated small reads in a region cost one block fetch total.
2. **Cache hits never touch seek detection**: once both the cluster and Cues
   regions are cached, the 1.1 GB-apart alternation produces zero network
   traffic and does not thrash the prefetch stream.
3. Seek-aware prefetch stream: fills [cursor, cursor+64 MiB) ahead with 3
   workers; re-anchors on misses that land far from the cursor; waits on a
   cursor-advance signal when caught up (no polling).
4. Foreground is never queued behind a prefetch: it always has its own load.

Same ffmpeg simulation, 1080p MKV, same server (~3.6 MiB/s hard ceiling):

| Workload | Phase 2 baseline | Phase 3 |
|---|---|---|
| cold open + decode 10 s | ~33 s / 601 requests | **10.9 s / 11 requests** |
| re-decode 10 s (warm) | 34.6 s / +597 requests | **0.77 s / 0 requests** |
| seek to 300 s + decode 10 s | 14.7 s | **3.5 s / 3 requests** |
| seek to 1400 s + decode 10 s | 13.5 s | **3.5 s** |
| realtime-paced playback 45 s | impossible (0.3 MiB/s) | **46.7 s wall — holds** |
| 1 MiB sequential (16 MiB) | 3.66 MiB/s | RAM-speed when cached; ceiling when cold |

Data integrity: ranges at block boundaries, cross-block spans, the Cues
region, and the EOF tail hash-match direct HTTP byte-for-byte.

### Browsing / metadata phase (post-Phase-3 fix, 2026-09-05)

First real-world use exposed what the benchmarks missed: **all user-visible
slowness was in metadata, not content** (a session of actual browsing showed
zero ReadAt calls). Causes, all fixed:

- Every directory open cost 2 sequential API calls (fs/get + fs/list) and
  every file open 2 more (Stat + GetWithLink) — with no caching, on a link
  with ~0.3–1.5 s RTT and frequent connection resets.
- Fix: TTL metadata cache in the source layer (listings, stat, direct links;
  `--metadata-ttl`, default 30 s; expired links stay safe because the range
  reader re-resolves on 403/410).
- Fix: transport-level retry (3 attempts, backoff) + 502/503/504 retry in
  the API client, plus per-call timing logs — the 8.7 s invisible stall in
  the open() timeline is now measurable.
- Fix: TLS-handshake / response-header timeouts on both HTTP transports to
  bound pathological hangs.

Measured before → after (same mount, same server):

| Operation | before | after |
|---|---|---|
| listdir (cold) | 1100–1400 ms | 110–250 ms |
| listdir (repeat) | 1100–1400 ms | ~1 ms |
| stat file | 2000 ms | 400–700 ms |
| open + first read | **14 000–19 500 ms** | **400–630 ms** |

Server phase caveat: seek/playback absolute numbers fluctuate with the
server's condition (direct-URL controls move identically); the mount tracks
direct-URL parity in every phase we measured.

### Second round of fixes (same evening, degraded server phase ~1.1 MiB/s)

Real Explorer-pattern simulation (listdir + stat all + shell miss-probes +
first 64 KiB of every file) drove three more changes:

1. **Sub-block foreground granularity** (`--sub-block-size`, default 512 KiB):
   a first-touch read used to pull a whole 4 MiB block; thumbnail-style
   access across many files pulled one block per file. First-touch now pays
   512 KiB; the prefetch stream still fills 4 MiB blocks underneath
   (two-granularity L1/L2 cache).
2. **Two prefetch streams**: the MKV pattern alternates between two hot
   regions (clusters ↔ Cues). A single stream got cancelled by every
   alternation; the Reader now keeps up to two streams and only replaces
   the weakest on a third far region. Cold-mount first playback at the
   degraded phase: **102 s → 37 s** for 30 s of content (bandwidth floor
   alone is ~23 s); steady state 31 s; seek 5.9 s vs 9.9 s on the direct
   URL measured the same minute.
3. Explorer browse totals: cold first round 30 s → 14 s (dominated by
   first-touch content at degraded bandwidth), warm round 29 ms.

Why the cold 10 s decode is 10.9 s and not ~3 s: the stress test decodes as
fast as data arrives (no pacing) and pays a ~1.2 s block-load stall on each
region switch; real players pace at 1x and the prefetcher stays ahead of the
playhead (0.83 MB/s demand vs 3.6 MB/s supply), which is why the
realtime-paced test holds with ease.

### Measured baseline (Phase 0 probe, 2026-09-05, from dev machine)

- TTFB per Range request: **0.3–1.5 s** on a fresh connection, **~150 ms**
  with keep-alive (measured again by `readtest` in Phase 1) — connection
  reuse matters a lot on this link.
- Throughput, sequential keep-alive 4–8 MiB requests: **~3.6–3.8 MiB/s**
- Throughput, 4 workers × disjoint ranges: **~3.5 MiB/s** (no aggregate gain)
- Conclusion: the server uplink is the ceiling (~30 Mbps); 1 GiB is fetched in ~5 min.
  The PoC's win condition is therefore **latency behavior** (TTFB, request sizing,
  cancel-stale-prefetch-on-seek), not raw throughput.

### Other notes

- Error mapping (body `code`): 401/403 → auth/permission (token may have expired → re-login
  once when username/password are configured), messages containing "not found" → ENOENT.
- We never log the token, `Authorization`, or the `sign` query value; URLs are logged with
  query redacted by default.

## Conclusion: is gofs sufficient for the PoC?

**Yes.** WinFsp reads map 1:1 to `ReadAt`, metadata caching is under our control, the
mount lifecycle (`Mount`/`Unmount`/signal) is complete, and read-only enforcement is a
mode flag plus error returns. Dropping to the native binding would only be warranted for
controlling WinFsp-level read chunking / buffer reuse, which gofs does not expose — we
revisit only if Phase 2/3 measurements show the gofs layer itself is a bottleneck.
