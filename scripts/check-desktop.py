"""Exercise real Wails/WebView2 in an isolated build, profile and directory.

Run scripts/build-desktop-test.py first. Its private dependency copy enables
loopback CDP for this driver only; release builds do not expose debugging.
"""
import argparse
import ctypes
from ctypes import wintypes
import json
import os
from pathlib import Path
import runpy
import shutil
import socket
import subprocess
import tempfile
import time
import urllib.request
from playwright.sync_api import sync_playwright

ROOT = Path(__file__).resolve().parents[1]
NATIVE = runpy.run_path(str(ROOT / 'scripts/check-tray.py'))
u, windows, wait_for = (NATIVE[k] for k in ('u', 'windows', 'wait_for'))
u.IsZoomed.argtypes = [wintypes.HWND]
u.GetWindowLongW.argtypes = [wintypes.HWND, ctypes.c_int]

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--exe', default=str(ROOT / 'build/bin/openlistfs-gui-desktop-test.exe'))
    args = parser.parse_args()
    output = ROOT / 'build/desktop-refresh-verification/native'
    output.mkdir(parents=True, exist_ok=True)
    report = {'checks': [], 'bridge': 'actual Wails/WebView2 with a test-only CDP driver', 'mounts': 'none', 'foregroundObserved': []}
    with tempfile.TemporaryDirectory(prefix='openlistfs-desktop-') as directory:
        directory = Path(directory)
        exe = directory / 'openlistfs-desktop-test.exe'
        shutil.copy2(args.exe, exe)
        with socket.socket() as s:
            s.bind(('127.0.0.1', 0)); port = s.getsockname()[1]
        env = {**os.environ, 'OPENLISTFS_TEST_CDP': str(port), 'OPENLISTFS_TEST_WEBVIEW': str(directory / 'webview')}
        child = subprocess.Popen([str(exe), '--minimized'], cwd=directory, env=env, creationflags=subprocess.CREATE_NO_WINDOW)
        try:
            gui = wait_for(lambda: [r['hwnd'] for r in windows(child.pid) if r['class'] == 'wailsWindow'])[0]
            tray = wait_for(lambda: [r['hwnd'] for r in windows(child.pid) if r['class'] == 'SystrayClass'])[0]
            def debugger_ready():
                try:
                    with urllib.request.urlopen(f'http://127.0.0.1:{port}/json/version', timeout=.3) as response:
                        return response.status == 200
                except OSError: return False
            wait_for(debugger_ready, seconds=15)
            with sync_playwright() as p:
                browser = p.chromium.connect_over_cdp(f'http://127.0.0.1:{port}', timeout=15000)
                page = browser.contexts[0].pages[0]
                page.wait_for_function('!!window.go?.main?.App && !!document.querySelector(".window-bar")')
                time.sleep(.5)
                assert not u.IsWindowVisible(gui), 'initial --minimized was ignored'
                def duplicate(flags=()):
                    dup = subprocess.Popen([str(exe), *flags], cwd=directory, env=env, creationflags=subprocess.CREATE_NO_WINDOW)
                    assert dup.wait(timeout=8) == 0
                    wait_for(lambda: u.IsWindowVisible(gui) and not u.IsIconic(gui))
                    time.sleep(.2)
                    report['foregroundObserved'].append(u.GetForegroundWindow() == gui)
                    assert not u.GetWindowLongW(gui, -20) & 8, 'activation left permanent topmost style'
                duplicate(('--minimized',))
                report['checks'].append('Repeat launch exits and raises the hidden primary, including --minimized')
                outer = wintypes.RECT(); client = wintypes.RECT()
                u.GetWindowRect(gui, ctypes.byref(outer)); u.GetClientRect(gui, ctypes.byref(client))
                assert outer.bottom-outer.top-client.bottom <= 16*u.GetDpiForWindow(gui)/96
                assert page.locator('.window-bar').evaluate('el=>el.offsetHeight') == 38
                assert page.locator('.window-brand').evaluate('(el)=>{let r=el.getBoundingClientRect();return el.contains(document.elementFromPoint(r.x+10,r.y+10))}')
                report['checks'].append('Native caption removed; visible custom bar is 38 CSS pixels')
                page.get_by_role('button', name='最大化', exact=True).click()
                wait_for(lambda: u.IsZoomed(gui))
                page.get_by_role('button', name='还原窗口', exact=True).click()
                wait_for(lambda: not u.IsZoomed(gui))
                report['checks'].append('Custom maximise/restore buttons change actual HWND state')
                page.locator('.window-caption').dispatch_event('dblclick')
                wait_for(lambda: u.IsZoomed(gui))
                page.locator('.window-caption').dispatch_event('dblclick')
                wait_for(lambda: not u.IsZoomed(gui))
                report['checks'].append('Title-bar double-click handler maximises and restores the HWND')
                page.get_by_role('button', name='最小化', exact=True).click()
                wait_for(lambda: u.IsIconic(gui))
                duplicate()
                report['checks'].append('Repeat launch restores a truly minimised window')
                page.get_by_role('button', name='关闭到托盘', exact=True).click()
                wait_for(lambda: not u.IsWindowVisible(gui))
                assert child.poll() is None
                duplicates = [subprocess.Popen([str(exe)], cwd=directory, env=env, creationflags=subprocess.CREATE_NO_WINDOW) for _ in range(8)]
                assert all(proc.wait(timeout=10) == 0 for proc in duplicates)
                wait_for(lambda: u.IsWindowVisible(gui))
                assert len([r for r in windows(child.pid) if r['class'] == 'wailsWindow']) == 1
                report['checks'].append('Eight simultaneous repeat launches keep one primary and one window')
                page.wait_for_function('document.fonts.status === "loaded"')
                page.screenshot(path=str(output / 'native-window.png'))
                assert page.evaluate('window.go.main.App.GetProfiles()').get('names') == []
                u.PostMessageW(tray, 0x111, 5, 0)
                assert child.wait(timeout=10) == 0
                report['checks'].append('Tray quit exits cleanly after activation and window-control checks')
                browser.close()
            child = subprocess.Popen([str(exe), '--minimized'], cwd=directory, env=env, creationflags=subprocess.CREATE_NO_WINDOW)
            tray = wait_for(lambda: [r['hwnd'] for r in windows(child.pid) if r['class'] == 'SystrayClass'])[0]
            time.sleep(.5)
            u.PostMessageW(tray, 0x111, 5, 0)
            assert child.wait(timeout=10) == 0
            report['checks'].append('Clean restart reacquires the single-instance lock')
        finally:
            if child.poll() is None: child.terminate(); child.wait(timeout=5)
            (output / 'results.json').write_text(json.dumps(report, indent=2), encoding='utf-8')
            time.sleep(.5)
    print(f"Passed {len(report['checks'])} native desktop checks; foreground observations: {report['foregroundObserved']}")

if __name__ == '__main__': main()
