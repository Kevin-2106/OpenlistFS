"""Exercise a newly built GUI in an isolated directory without any mount/config.

Post native tray messages only to this child process. Never restart existing apps.
Use --diagnose-pid for read-only WM_NULL diagnostics of a running instance.
"""
import argparse
import ctypes
from ctypes import wintypes
import json
from pathlib import Path
import shutil
import subprocess
import tempfile
import time

ROOT = Path(__file__).resolve().parents[1]
u = ctypes.WinDLL('user32', use_last_error=True)
# GetWindowRect and the screenshot must use the same physical-pixel coordinate
# system on this host's scaled monitors; otherwise cropping can include neighbors.
u.SetProcessDpiAwarenessContext.argtypes = [ctypes.c_void_p]
u.SetProcessDpiAwarenessContext(ctypes.c_void_p(-4))
CALLBACK = ctypes.WINFUNCTYPE(wintypes.BOOL, wintypes.HWND, wintypes.LPARAM)
u.GetWindowThreadProcessId.argtypes = [wintypes.HWND, ctypes.POINTER(wintypes.DWORD)]
u.GetClassNameW.argtypes = [wintypes.HWND, wintypes.LPWSTR, ctypes.c_int]
u.SendMessageTimeoutW.argtypes = [wintypes.HWND, wintypes.UINT, wintypes.WPARAM, wintypes.LPARAM, wintypes.UINT, wintypes.UINT, ctypes.POINTER(ctypes.c_size_t)]
u.PostMessageW.argtypes = [wintypes.HWND, wintypes.UINT, wintypes.WPARAM, wintypes.LPARAM]
u.ShowWindowAsync.argtypes = [wintypes.HWND, ctypes.c_int]
u.IsWindowVisible.argtypes = [wintypes.HWND]
u.IsIconic.argtypes = [wintypes.HWND]
u.SetForegroundWindow.argtypes = [wintypes.HWND]
u.GetForegroundWindow.restype = wintypes.HWND
u.GetWindowRect.argtypes = [wintypes.HWND, ctypes.POINTER(wintypes.RECT)]
u.GetClientRect.argtypes = [wintypes.HWND, ctypes.POINTER(wintypes.RECT)]
u.ClientToScreen.argtypes = [wintypes.HWND, ctypes.POINTER(wintypes.POINT)]
class GUIThreadInfo(ctypes.Structure):
    _fields_ = [('cbSize', wintypes.DWORD), ('flags', wintypes.DWORD), ('hwndActive', wintypes.HWND), ('hwndFocus', wintypes.HWND), ('hwndCapture', wintypes.HWND), ('hwndMenuOwner', wintypes.HWND), ('hwndMoveSize', wintypes.HWND), ('hwndCaret', wintypes.HWND), ('rcCaret', wintypes.RECT)]
u.GetGUIThreadInfo.argtypes = [wintypes.DWORD, ctypes.POINTER(GUIThreadInfo)]


def menu_state(thread):
    state = GUIThreadInfo()
    state.cbSize = ctypes.sizeof(state)
    assert u.GetGUIThreadInfo(thread, ctypes.byref(state))
    return {'flags': state.flags, 'owner': state.hwndMenuOwner}


def windows(pid):
    rows = []
    @CALLBACK
    def callback(hwnd, _):
        owner = wintypes.DWORD()
        tid = u.GetWindowThreadProcessId(hwnd, ctypes.byref(owner))
        if owner.value == pid:
            cls = ctypes.create_unicode_buffer(256)
            u.GetClassNameW(hwnd, cls, 256)
            rows.append({'hwnd': hwnd, 'class': cls.value, 'thread': tid, 'visible': bool(u.IsWindowVisible(hwnd))})
        return True
    u.EnumWindows(callback, 0)
    return rows


def ping(hwnd):
    result = ctypes.c_size_t()
    ctypes.set_last_error(0)
    ok = u.SendMessageTimeoutW(hwnd, 0, 0, 0, 2, 500, ctypes.byref(result))
    return {'responsive': bool(ok), 'error': ctypes.get_last_error()}


def wait_for(check, seconds=8):
    end = time.monotonic() + seconds
    while time.monotonic() < end:
        result = check()
        if result:
            return result
        time.sleep(.05)
    raise AssertionError('Timed out waiting for native state')


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--diagnose-pid', type=int)
    parser.add_argument('--exe', default=str(ROOT / 'build/bin/openlistfs-gui-endfield.exe'))
    args = parser.parse_args()
    output = ROOT / 'build/tray-verification'
    output.mkdir(parents=True, exist_ok=True)
    if args.diagnose_pid:
        rows = windows(args.diagnose_pid)
        for row in rows:
            row.update(ping(row['hwnd']))
        (output / 'running-instance.json').write_text(json.dumps({'pid': args.diagnose_pid, 'readOnly': True, 'windows': rows}, indent=2))
        print(json.dumps(rows, indent=2))
        return
    report = {'isolated': True, 'mounts': 'No profiles, no automount flag, no credentials', 'checks': [], 'limits': ['Injected native event messages; physical taskbar mouse input not exercised', 'Existing mounted instance preserved']}
    with tempfile.TemporaryDirectory(prefix='openlistfs-tray-') as directory:
        executable = Path(directory) / 'openlistfs-gui.exe'
        shutil.copy2(args.exe, executable)
        process = subprocess.Popen([str(executable), '--minimized'], cwd=directory, creationflags=subprocess.CREATE_NO_WINDOW)
        try:
            found = wait_for(lambda: [r for r in windows(process.pid) if r['class'] == 'SystrayClass'])
            tray = found[0]['hwnd']
            gui = wait_for(lambda: [r for r in windows(process.pid) if r['class'] == 'wailsWindow'])[0]['hwnd']
            report['threads'] = {'tray': found[0]['thread'], 'wails': [r for r in windows(process.pid) if r['class'] == 'wailsWindow'][0]['thread']}
            time.sleep(2)
            for _ in range(100):
                assert ping(tray)['responsive'], 'tray message loop stopped responding'
                time.sleep(.01)
            report['checks'].append('100 responsive WM_NULL probes of the actual tray window')
            for _ in range(10):
                u.PostMessageW(gui, 0x10, 0, 0)  # Wails close-to-tray, never quit.
                wait_for(lambda: not u.IsWindowVisible(gui))
                u.PostMessageW(tray, 0x401, 0, 0x203)  # WM_LBUTTONDBLCLK
                wait_for(lambda: bool(u.IsWindowVisible(gui)))
                assert ping(tray)['responsive']
            report['checks'].append('10 close-to-tray/double-click restore cycles')
            # Capture this child HWND only, never unrelated desktop windows.
            from PIL import ImageGrab
            u.SetForegroundWindow(gui)
            wait_for(lambda: u.GetForegroundWindow() == gui)
            time.sleep(1)
            rect = wintypes.RECT()
            assert u.GetClientRect(gui, ctypes.byref(rect))
            origin = wintypes.POINT(0, 0)
            assert u.ClientToScreen(gui, ctypes.byref(origin))
            bounds = (origin.x, origin.y, origin.x + rect.right, origin.y + rect.bottom)
            assert u.GetForegroundWindow() == gui, 'child must remain foreground before capture'
            capture = ImageGrab.grab(bbox=bounds, all_screens=True)
            capture.save(output / 'native-window.png')
            # PrintWindow may return a blank hardware compositor; use the visible
            # foreground child's rectangle, with no other desktop area captured.
            assert len(capture.getcolors(capture.width * capture.height) or []) > 20, 'native capture is blank'
            report['screenshot'] = 'native-window.png: foreground native child client area, empty isolated configuration'
            report['captureBoundsPhysicalPixels'] = list(bounds)
            u.ShowWindowAsync(gui, 6)  # Minimize only the isolated child.
            wait_for(lambda: u.IsIconic(gui))
            u.PostMessageW(tray, 0x401, 0, 0x203)
            wait_for(lambda: not u.IsIconic(gui))
            # Wails restores asynchronously. Wait for its foreground work to
            # finish before opening a menu that foreground changes would close.
            wait_for(lambda: u.GetForegroundWindow() == gui)
            assert ping(gui)['responsive']
            time.sleep(.2)
            report['checks'].append('Double-click restores an actually minimized native window')
            for _ in range(5):
                u.PostMessageW(tray, 0x401, 0, 0x205)  # WM_RBUTTONUP
                wait_for(lambda: menu_state(found[0]['thread'])['owner'] == tray)
                u.PostMessageW(tray, 0x1f, 0, 0)  # WM_CANCELMODE closes its popup.
                wait_for(lambda: not menu_state(found[0]['thread'])['owner'])
                assert ping(tray)['responsive']
            report['checks'].append('5 real native right-click popup open/cancel cycles')
            u.PostMessageW(tray, 0x111, 1, 0)  # Show menu item, actual registered command ID.
            wait_for(lambda: bool(u.IsWindowVisible(gui)))
            report['checks'].append('Native Show menu command responds')
            u.PostMessageW(tray, 0x111, 5, 0)  # Quit follows separator ID 4.
            assert process.wait(timeout=8) == 0
            report['checks'].append('Native Quit menu command cleanly exits the isolated app')
        finally:
            if process.poll() is None:
                process.terminate(); process.wait(timeout=5)
            (output / 'results.json').write_text(json.dumps(report, indent=2))
    print(f"Passed {len(report['checks'])} native tray checks")


if __name__ == '__main__':
    main()
