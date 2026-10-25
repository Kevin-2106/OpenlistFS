"""Install/upgrade/uninstall using disposable registry and Start-menu identities.

Uses the real NSIS script and WinFsp checker, and refuses to run if shared
WinFsp is not already compatible. It never installs or repairs the driver.
"""
import ctypes
from ctypes import wintypes
import hashlib
import json
import os
from pathlib import Path
import runpy
import subprocess
import tempfile
import time
import uuid
import winreg

ROOT = Path(__file__).resolve().parents[1]
OUTPUT = ROOT / 'build/desktop-refresh-verification/installer'
NATIVE = runpy.run_path(str(ROOT / 'scripts/check-tray.py'))
u, windows, wait_for = (NATIVE[k] for k in ('u', 'windows', 'wait_for'))
u.GetWindowTextW.argtypes = [wintypes.HWND, wintypes.LPWSTR, ctypes.c_int]
u.GetDlgItem.argtypes = [wintypes.HWND, ctypes.c_int]
u.GetDlgItem.restype = wintypes.HWND
CALLBACK = NATIVE['CALLBACK']

def text(hwnd):
    buf = ctypes.create_unicode_buffer(2048)
    result = ctypes.c_size_t()
    # GetWindowText cannot read edit controls belonging to another process.
    u.SendMessageTimeoutW(hwnd, 0xd, len(buf), ctypes.addressof(buf), 2, 500, ctypes.byref(result))
    return buf.value

def children(hwnd):
    rows = []
    @CALLBACK
    def callback(child, _):
        rows.append((child, text(child))); return True
    u.EnumChildWindows(hwnd, callback, 0)
    return rows

def sha(path): return hashlib.sha256(path.read_bytes()).hexdigest()

def registry_value(key, name):
    try:
        with winreg.OpenKey(winreg.HKEY_CURRENT_USER, key) as h:
            return winreg.QueryValueEx(h, name)[0]
    except FileNotFoundError:
        return None

def main():
    OUTPUT.mkdir(parents=True, exist_ok=True)
    assert subprocess.run([str(ROOT / 'build/bin/winfsp-check.exe'), '--quiet'], creationflags=subprocess.CREATE_NO_WINDOW).returncode == 0, 'Compatible WinFsp required; this test never installs drivers'
    token = 'OpenListFS-Test-' + uuid.uuid4().hex
    product = 'Software\\' + token
    uninstall = 'Software\\Microsoft\\Windows\\CurrentVersion\\Uninstall\\' + token
    real_key = 'Software\\OpenListFS'
    real_path = registry_value(real_key, 'InstallDir')
    real_hash = sha(Path(real_path) / 'openlistfs-gui.exe') if real_path else None
    report = {'checks': [], 'scope': 'real NSIS, disposable registry/start menu/path; compatible shared WinFsp reused'}
    def compile_setup(version, gui):
        target = OUTPUT / f'test-{version}-setup.exe'
        defines = {
            'REPO_ROOT': ROOT, 'BIN_DIR': ROOT / 'build/bin',
            'WINFSP_MSI': ROOT / 'build/dependencies/winfsp.msi', 'GUI_BINARY': ROOT / 'build/bin' / gui,
            'WINFSP_MANIFEST': ROOT / 'internal/winfspinstall/winfsp.json',
            'WINFSP_LICENSE': ROOT / 'internal/winfspinstall/WinFsp-License.txt', 'THIRD_PARTY_NOTICES': ROOT / 'THIRD_PARTY_NOTICES.md',
            'PRODUCT_VERSION': version, 'FILE_VERSION': version + '.0', 'OUTPUT_FILE': target,
            'PRODUCT_KEY': product, 'UNINSTALL_KEY': uninstall, 'MENU_FOLDER': token,
        }
        result = subprocess.run([str(ROOT / 'build/tools/nsis/nsis-3.11/makensis.exe'), '/V2', '/NOCD', '/XSetCompressor /FINAL zlib', *[f'/D{k}={v}' for k,v in defines.items()], str(ROOT / 'build/windows/installer/OpenListFS.nsi')], capture_output=True, text=True)
        (OUTPUT / f'compile-{version}.log').write_text(result.stdout + result.stderr, encoding='utf-8')
        assert result.returncode == 0, result.stderr
        return target
    old = compile_setup('0.1.0', 'openlistfs-gui-endfield-compact.exe')
    new = compile_setup('0.2.0', 'openlistfs-gui-v0.2.0.exe')
    with tempfile.TemporaryDirectory(prefix='openlistfs-setup-') as sandbox:
        destination = Path(sandbox) / '自定义 路径' / 'OpenListFS'
        live = None
        try:
            # /D must be the last, unquoted part of NSIS's raw command line.
            result = subprocess.run(f'"{old}" /S /D={destination}', timeout=60, creationflags=subprocess.CREATE_NO_WINDOW)
            assert result.returncode == 0, result.returncode
            assert registry_value(product, 'InstallDir') == str(destination)
            assert sha(destination / 'openlistfs-gui.exe') == sha(ROOT / 'build/bin/openlistfs-gui-endfield-compact.exe')
            report['checks'].append('Custom Unicode path with spaces installs the expected GUI/CLI/driver payload')
            for name in ('profiles.json', '.env', 'user-kept.txt'):
                (destination / name).write_text('isolated retention sentinel: ' + name, encoding='utf-8')
            sentinels = {name: sha(destination / name) for name in ('profiles.json', '.env', 'user-kept.txt')}
            # Real directory page: registered custom path is preselected on upgrade.
            live = subprocess.Popen([str(new)], creationflags=subprocess.CREATE_NO_WINDOW)
            dialog = wait_for(lambda: [r['hwnd'] for r in windows(live.pid) if r['class'] == '#32770' and r['visible']])[0]
            next_button = u.GetDlgItem(dialog, 1)
            wait_for(lambda: 'Next' in text(next_button))
            u.PostMessageW(next_button, 0xf5, 0, 0)
            time.sleep(.5)
            (OUTPUT / 'directory-controls.json').write_text(json.dumps({'expected':str(destination),'windows':[(r['class'],text(r['hwnd'])) for r in windows(live.pid)],'children':children(dialog)}, ensure_ascii=False, indent=2),encoding='utf-8')
            wait_for(lambda: any(value.rstrip('\\') == str(destination) for _, value in children(dialog)))
            from PIL import ImageGrab
            u.SetForegroundWindow(dialog)
            wait_for(lambda: u.GetForegroundWindow() == dialog)
            time.sleep(.3)
            rect = wintypes.RECT(); u.GetWindowRect(dialog, ctypes.byref(rect))
            ImageGrab.grab(bbox=(rect.left,rect.top,rect.right,rect.bottom), all_screens=True).save(OUTPUT / 'directory-page.png')
            report['checks'].append('Real directory selection page preselects the existing custom install path')
            u.PostMessageW(next_button, 0xf5, 0, 0)
            wait_for(lambda: 'Finish' in text(next_button), seconds=30)
            u.PostMessageW(next_button, 0xf5, 0, 0)
            assert live.wait(timeout=10) == 0
            live = None
            assert sha(destination / 'openlistfs-gui.exe') == sha(ROOT / 'build/bin/openlistfs-gui-v0.2.0.exe')
            assert all(sha(destination / name) == digest for name,digest in sentinels.items())
            report['checks'].append('Interactive directory-page upgrade finishes and preserves user files')
            # No /D: the registry must resolve the original path automatically.
            assert subprocess.run([str(new), '/S'], timeout=60, creationflags=subprocess.CREATE_NO_WINDOW).returncode == 0
            assert sha(destination / 'openlistfs-gui.exe') == sha(ROOT / 'build/bin/openlistfs-gui-v0.2.0.exe')
            assert registry_value(uninstall, 'DisplayVersion') == '0.2.0'
            assert all(sha(destination / name) == digest for name,digest in sentinels.items())
            report['checks'].append('0.1.0 to 0.2.0 overwrites the same path, updates version and preserves all user files byte for byte')
            other = Path(sandbox) / 'wrong-upgrade-path'
            assert subprocess.run(f'"{new}" /S /D={other}', timeout=30, creationflags=subprocess.CREATE_NO_WINDOW).returncode == 2
            assert not other.exists()
            report['checks'].append('Upgrade refuses silent relocation that would strand saved configuration')
            assert subprocess.run(f'"{destination / "uninstall.exe"}" /S _?={destination}', timeout=30, creationflags=subprocess.CREATE_NO_WINDOW).returncode == 0
            assert not (destination / 'openlistfs-gui.exe').exists()
            assert registry_value(product, 'InstallDir') is None
            assert all(sha(destination / name) == digest for name,digest in sentinels.items())
            assert subprocess.run([str(ROOT / 'build/bin/winfsp-check.exe'), '--quiet'], creationflags=subprocess.CREATE_NO_WINDOW).returncode == 0
            report['checks'].append('Uninstall removes application and test registration, preserving user files and shared WinFsp')
        finally:
            if live and live.poll() is None: live.terminate(); live.wait(timeout=5)
            uninstaller = destination / 'uninstall.exe'
            if uninstaller.exists(): subprocess.run(f'"{uninstaller}" /S _?={destination}', timeout=30, creationflags=subprocess.CREATE_NO_WINDOW)
            assert registry_value(real_key, 'InstallDir') == real_path
            if real_path: assert sha(Path(real_path) / 'openlistfs-gui.exe') == real_hash
            (OUTPUT / 'results.json').write_text(json.dumps(report, ensure_ascii=False, indent=2), encoding='utf-8')
    print(f"Passed {len(report['checks'])} real installer checks; installed user application preserved")

if __name__ == '__main__': main()
