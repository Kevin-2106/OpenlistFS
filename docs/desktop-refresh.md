# Desktop refresh 0.2.0

The app now uses an original yellow/ink drive icon, a 38px custom title bar,
a session-wide single-instance guard, unmount-before-delete behavior, and
an installer directory page with in-place upgrades.

## Implementation

- `assets/icon.svg` is the editable source. `scripts/build-icons.py` renders
  PNG/ICO resources for the app, tray and NSIS; Wails uses `build/appicon.png`.
  Original resources from this work are retained in the ignored
  `build/desktop-refresh-verification/original-icons` folder.
- `WindowBar.vue` uses Wails frameless dragging and runtime window controls.
  The drag surface excludes the controls; closing still hides to tray.
  The original side brand row was removed and heading spacing tightened.
- `internal/singleinstance` holds a named Windows mutex before Wails startup.
  A named auto-reset event retains an activation request until the DOM is
  ready. The identity is stable across filenames, paths and future releases.
  The second launcher transfers foreground permission and exits. Activation
  raises/restores the original window, briefly raises its Z order, then
  clears topmost. Windows may restrict focus if another app receives input
  during activation; permanent topmost is never enabled.
- `DeleteProfile` blocks mounts during deletion, synchronously tears down all
  matching sessions and saves a staged configuration. A save failure leaves
  the profile available for retry, with mount status refreshed in the UI.
  A pending mount prevents deletion until it finishes. Teardown operations
  are serialized to prevent concurrent unmount/delete from bypassing order.
- The NSIS installer permits a custom path on first installation and uses
  the registered path for upgrades. It replaces only application payloads
  and retains user files. Running files still block replacement: exit using
  the tray's quit command, which unmounts first, before running setup.

Single-instance exclusion requires the upgraded binary. Releases predating
this change do not participate in the lock and must be exited before first
using 0.2.0. CLI mount commands retain their existing independent behavior.

## Verification

Evidence is stored under `build/desktop-refresh-verification` (ignored).

- Go suite: `go test ./... -count=1 -timeout=90s`.
- Race checks: `go test -race ./internal/singleinstance . -count=1 -timeout=60s`.
  The lock test launches 12 concurrent contenders, checks startup activation
  retention and verifies the lock can be reacquired after exit. App tests
  verify two matching drives are unmounted before saving, other profiles
  survive, pending mounts block deletion and failed saves retain config.
- `npm run build`, then `python scripts/check-ui.py --output
  build/desktop-refresh-verification/ui`: 15 browser checks with simulated
  backend calls, including deletion failure/retry, themes, narrow layouts,
  controls and visible title-bar stacking.
- `python scripts/build-desktop-test.py`, then `python scripts/check-desktop.py`:
  eight checks against actual Wails/WebView2 and HWND states. A disposable
  local copy of the pinned Wails dependency enables loopback CDP and a
  temporary cache for the test driver only; release builds and module-cache
  sources are unchanged. Tests cover hidden/minimised activation, eight
  simultaneous repeat launches, title-bar controls/double-click, tray quit
  and clean restart. Both measured activations reached the foreground.
- `python scripts/check-installer.py`: real NSIS installs and upgrades using
  isolated registry, Start menu and Unicode paths. It refuses to run unless
  shared WinFsp is already compatible. Checks preserve `.env`, profiles and
  arbitrary user files by SHA-256 and verify shared WinFsp remains usable.
  Test fixtures use zlib for faster builds; the release uses solid LZMA.

Deletion driver ordering uses fake session objects in Go tests; no live
remote filesystem or writable upload was exercised during this change.
Clean-machine driver installation, UAC cancellation and reboot-required
flows remain the VM release checks in `windows-installer.md`.

Test development exposed two harness limits: the upstream loader ignores
external CDP environment flags, and desktop mouse tests can lose foreground
to other applications. The final native harness uses the isolated CDP
driver and verifies actual HWND states. An early screenshot also exposed
the background covering the title bar; the stacking order was fixed and
both browser and native screenshots were checked afterward. Installer UI
path assertions normalize its displayed trailing backslash.

Framework references: [Wails window options](https://v2.wails.io/docs/reference/options/)
and [Windows foreground activation rules](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-setforegroundwindow).
