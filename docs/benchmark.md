# Benchmark: direct HTTP vs OpenListFS mount vs rclone mount

Date: 2026-09-05 · Machine: Windows 11, same network/server throughout.
Media: `[POPGO] Stand_Alone_Complex 01`, 1080p BluRay x264 MKV, 1.19 GB,
~24 min (avg bitrate ≈ 0.83 MB/s — comfortably below the server's
~3.6 MiB/s ceiling; 4K remuxes would saturate it).

Server: OpenList v4.2.1, Local storage driver, direct link = `/p/…?sign=…`
proxy URL (fully Range-capable, see research.md).

Workloads run through ffmpeg's demux+decode pipeline (`libav*` — the same
pipeline mpv uses; mpv itself is not installed on this machine, so these are
automated stand-ins, and subjective testing remains open):

- **cold**: fresh mount/process, open + decode 10 s
- **warm**: same decode immediately again
- **seek**: open with `-ss 300` (or 1400) + decode 10 s ≈ jump several GB
  and measure time-to-first-frame + decode
- **realtime**: `-re` paced playback of 45 s of content — wall time must be
  ≈ 45 s; anything higher stalls

rclone used v1.75.0 over WebDAV (`vendor = alist`) with **default mount
settings** (128 MiB read chunks, no VFS disk cache) — i.e. what a user gets
out of the box. OpenListFS used defaults (4 MiB blocks / 64 MiB window /
256 MiB cache / 3 workers).

## Results

| Workload | A: direct HTTP | B: OpenListFS | C: rclone mount | Phase-2 baseline |
|---|---|---|---|---|
| cold: open + decode 10 s | **7.4 s** | 10.9 s (11 req) | 9.6 s | ~33 s / 601 req |
| warm: re-decode 10 s | — | **0.8 s / 0 req** | 10.7 s | 34.6 s |
| seek 300 s + decode 10 s | 3.6 s | **3.5 s** | 8.2 s | 14.7 s |
| seek 1400 s + decode 10 s | 3.9 s | **3.5 s** | 7.5 s | 13.5 s |
| realtime 45 s of content | 47.2 s | **46.7 s** | 50.1 s | impossible |

Sequential throughput ceiling for every path: ~3.6 MiB/s = the server's
uplink; no mount adds measurable overhead to large reads.

## Reading

- **Seek behavior — the original pain point — is at direct-HTTP parity**
  (3.5 s vs 3.6/3.9 s) and ~2.2× faster than default rclone, because 4 MiB
  blocks re-anchor on seek while rclone's 128 MiB chunk per position switch
  grossly overfetches before first frame.
- **Realtime playback holds** as well as the direct link on this link class.
- **Warm re-read is unique to OpenListFS** (RAM cache survives handle
  close): replaying the same content is effectively free.
- Cold open trails direct HTTP by ~3.5 s: one 4 MiB block-load stall
  per region switch (cluster ↔ Cues regions in MKV). If that matters, a
  smaller foreground granularity for the *first* load of a region is the
  obvious lever — deliberately left out of the PoC.

## Verdict against the stop condition

The PoC's question — *can native API + Range + WinFsp + read-ahead play and
seek clearly better than rclone mount and close to direct HTTP?* — is
answered **yes** on this workload (seek parity with direct HTTP; realtime
hold; every mount layer overhead measured at ~zero for large reads).

Per the project rules this is the point where scope expansion (GUI, disk
cache, writes, product polish) is discussed and decided by the user — not
built speculatively.
