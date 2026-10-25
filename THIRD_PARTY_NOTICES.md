# Third-party notices

## WinFsp

WinFsp - Windows File System Proxy, Copyright (C) Bill Zissimopoulos

Project: https://github.com/winfsp/winfsp

The Windows setup bundles the **unmodified official WinFsp 2.1.25156 MSI**.
Its URL and SHA-256 are pinned in `internal/winfspinstall/winfsp.json`.
The corresponding license is included as `dependencies/WinFsp-License.txt`.
WinFsp is licensed under GPLv3 with a special exception for qualifying Free/Libre
and Open Source Software. That exception permits linking with WinFsp and
redistribution of its unmodified official installer, subject to its stated
conditions, including attribution and an eligible application license.

OpenListFS currently has no declared project license. This local setup build
does not establish permission for public redistribution; declare a qualifying
project license and comply with the bundled WinFsp license before publishing,
or obtain the applicable commercial license. No license is selected on the
project owner's behalf.

License source: https://github.com/winfsp/winfsp/blob/v2.1/License.txt
Commercial licensing: https://winfsp.dev/com/

## go-winfsp

The Go binding `github.com/winfsp/go-winfsp` is licensed under MIT.
Its license does not replace the separate WinFsp driver/DLL license above.

## Interface fonts and original artwork

Noto Sans SC, Space Grotesk, Michroma and IBM Plex Mono are bundled as unmodified
open font files under the SIL Open Font License 1.1. Each font's original
`OFL.txt` and metadata are embedded alongside it in `frontend/public/endfield/fonts/`.
Noto Sans SC provides the complete functional Chinese glyph set; Space Grotesk
provides ordinary Latin and numeric metadata; Michroma is limited to display
headings; IBM Plex Mono is used for paths.

The original topographic and registration-grid SVGs from the local ReEndfield
library carry its CC0 1.0 dedication, included under `endfield/original/LICENSE.txt`.
The interface is an independent adaptation, not an official game product.
ReEndfield semantic tokens and implementation structure are adapted under its
MIT license, Copyright (c) 2026 ReEndfield contributors. The full notice is
embedded at `endfield/ReEndfield-LICENSE.md`.

## Excluded research artwork

The local desktop prototype used two official website textures. This public source snapshot excludes both files and uses the CC0 original topographic and registration-grid SVGs instead. Research website assets and proprietary reference fonts are not published.

## Public source snapshot

This repository publishes source code only. No OpenListFS executable, Windows setup, WinFsp MSI, personal configuration or runtime data is included. The project owner has not selected a project license; public source availability does not itself grant redistribution rights. Before distributing built binaries that link or bundle WinFsp, select and comply with an eligible license or obtain the appropriate commercial license.
