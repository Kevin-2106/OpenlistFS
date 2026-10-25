# Windows installer

The OpenListFS setup includes the unmodified official WinFsp MSI. Users do
not need to download the driver separately. OpenListFS is installed for the
current Windows account, with `%LOCALAPPDATA%\Programs\OpenListFS` as the
default. The directory page allows a custom path on first installation,
including paths containing spaces or Unicode. Only WinFsp's installer
requests administrator approval.

New releases reuse the registered installation directory and overwrite the
application files in place. `.env`, `profiles.json`, and other user files
are retained. Upgrading does not require uninstalling the old release.
The directory page preselects the registered path; changing the path during
an upgrade is refused to avoid leaving two copies and stranding the portable
configuration. To relocate, back up those files, uninstall the app, install
at the new path, and restore the files. Silent first installs accept NSIS's
last, unquoted `/D=C:\custom path\OpenListFS` argument; silent upgrades reuse
the registered path and reject a conflicting `/D=` with exit code 2.

Before copying application files, setup runs `winfsp-check.exe
--install-plan`:

| Existing WinFsp | Setup behavior |
| --- | --- |
| Compatible and usable | Reuse it; do not invoke the MSI |
| Missing | Install the bundled official MSI |
| Older than the minimum supported version | Upgrade with the bundled MSI |
| Damaged, and the bundled MSI can safely repair it | Repair it |
| Damaged newer installation or ambiguous installed version | Stop; ask the user to repair their installed version; never downgrade |
| Unsupported architecture | Stop with an explanation |

The helper rechecks the installation plan and bundled MSI SHA-256 before
elevating `msiexec`. Setup waits for its result. Cancellation and failures
stop setup; MSI exit code `3010` is accepted but requires a Windows restart.
Setup verifies WinFsp again afterward. MSI logs are written to
`%TEMP%\OpenListFS-WinFsp-install.log`.

The installed `dependencies\winfsp.msi` is also available to the app's
dependency repair flow, so repair does not require another download.
Uninstall removes the app and its bundled copy of the MSI, while retaining
shared WinFsp and the user's `.env`, `profiles.json`, and other user files.
Setup and uninstall stop if existing app binaries are in use or cannot be
opened for replacement. File extraction cannot be skipped and reported as
a successful upgrade; close the GUI and unmount drives before upgrading.

## Build

Requirements: Windows, Go, Node/npm, this project's Wails CLI, and the
official NSIS 3.11 portable compiler. Extract the NSIS ZIP under
`build\tools\nsis`, or pass its `makensis.exe` path explicitly. The script
does not install the compiler, driver, or app on the build machine.

Download the [official NSIS 3.11 portable ZIP](https://sourceforge.net/projects/nsis/files/NSIS%203/3.11/nsis-3.11.zip/download)
and verify SHA-256 before extraction. The hash published by the
[official NSIS download listing](https://sourceforge.net/projects/nsis/files/NSIS%203/3.11/)
and verified for the compiler used here is:

```text
c7d27f780ddb6cffb4730138cd1591e841f4b7edb155856901cdf5f214394fa1
```

```powershell
$compilerZip = '.\build\tools\nsis-3.11.zip'
$expectedHash = 'c7d27f780ddb6cffb4730138cd1591e841f4b7edb155856901cdf5f214394fa1'
if ((Get-FileHash -LiteralPath $compilerZip -Algorithm SHA256).Hash.ToLowerInvariant() -ne $expectedHash) {
    throw 'NSIS archive hash mismatch.'
}
Expand-Archive -LiteralPath $compilerZip -DestinationPath '.\build\tools\nsis'
```

The build script detects `build\tools\nsis\nsis-3.11\makensis.exe` after
extraction. It uses `wails build -trimpath -m -nosyncgomod` to avoid
rewriting module files or repeating Go module preparation during packaging.

```powershell
.\scripts\build-installer.ps1 -Version 0.2.0 -MakensisPath C:\tools\nsis-3.11\makensis.exe
```

To package already built GUI and CLI binaries without rebuilding them:

```powershell
.\scripts\build-installer.ps1 -Version 0.2.0 -SkipAppBuild -MakensisPath C:\tools\nsis-3.11\makensis.exe
```

`-SkipAppBuild` still rebuilds the dependency checker, so its embedded
manifest always matches the installer. The GUI and CLI inputs must be
`build\bin\openlistfs-gui.exe` and `build\bin\openlistfs.exe`.

If the development GUI is running and its EXE is locked, build a separate
output without interrupting existing mounts:

```powershell
.\scripts\build-installer.ps1 -GuiFilename openlistfs-gui-next.exe
```

Setup still installs the canonical filename `openlistfs-gui.exe`.

The build script reads the pinned release URL and hash from
`internal\winfspinstall\winfsp.json`. It downloads the MSI only if missing
and verifies both SHA-256 and its Windows Authenticode signature, including
when using a cached MSI. Network or signature verification failures stop
the build. Updating WinFsp requires reviewing and updating the manifest and
the upstream license together.

Outputs:

- `build\bin\OpenListFS-<version>-windows-amd64-setup.exe`
- `build\bin\OpenListFS-<version>-windows-amd64-setup.exe.sha256`

The application setup is unsigned until a release signing step is added.
The embedded WinFsp MSI retains its upstream signature. NSIS packages only
an explicit list of binaries, dependency files, and third-party notices;
developer `.env`, saved profiles, and unrelated `build\bin` files are
excluded.

## Release verification

Installer compilation and read-only checks on the development machine do
not prove installation, UAC, or driver behavior. Before publishing a
release, exercise these flows in disposable Windows VMs:

1. Clean machine: install the bundled WinFsp, accept UAC, mount a drive,
   read a known file, unmount, and confirm its hash.
2. Existing compatible WinFsp: setup succeeds without invoking the MSI or
   requesting driver elevation.
3. Older and damaged WinFsp: verify upgrade/repair; verify newer damaged
   versions are refused rather than downgraded.
4. Reject UAC or cancel MSI: setup stops clearly and does not replace an
   existing OpenListFS installation.
5. Reboot-required installation: setup reports the restart requirement;
   mounting becomes available after restart.
6. Upgrade and uninstall: existing profiles are preserved; shared WinFsp
   remains usable by other applications.
