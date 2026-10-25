"""Build an isolated native acceptance binary with CDP using a module copy.

The pinned Wails loader deliberately removes external WebView2 debugging
arguments. A test-only module copy adds a loopback port and disposable cache
only to this test build. Neither dependency sources nor release code are edited.
"""
import json
from pathlib import Path
import subprocess
import shutil
import stat

ROOT = Path(__file__).resolve().parents[1]
OUTPUT = ROOT / 'build/desktop-refresh-verification/driver'
OUTPUT.mkdir(parents=True, exist_ok=True)
module = subprocess.check_output(['go', 'list', '-m', '-f', '{{.Dir}}', 'github.com/wailsapp/wails/v2'], text=True, cwd=ROOT).strip()
local_module = OUTPUT / 'wails'
shutil.copytree(module, local_module, dirs_exist_ok=True)
source = local_module / 'internal/frontend/desktop/windows/frontend.go'
original = source.read_text(encoding='utf-8')
marker = '\tif len(disableFeatues) > 0 {'
assert original.count(marker) == 1
source.chmod(stat.S_IWRITE | stat.S_IREAD)
source.write_text(original.replace(marker, '\tchromium.AdditionalBrowserArgs = append(chromium.AdditionalBrowserArgs, "--remote-debugging-port="+os.Getenv("OPENLISTFS_TEST_CDP"))\n\tchromium.DataPath = os.Getenv("OPENLISTFS_TEST_WEBVIEW")\n' + marker), encoding='utf-8')
modfile = OUTPUT / 'test.mod'
modfile.write_text((ROOT / 'go.mod').read_text() + '\nreplace github.com/wailsapp/wails/v2 => ' + json.dumps(local_module.as_posix()) + '\n', encoding='utf-8')
shutil.copyfile(ROOT / 'go.sum', OUTPUT / 'test.sum')
subprocess.run(['go', 'build', '-trimpath', '-tags', 'production', '-modfile', str(modfile), '-ldflags', '-H windowsgui -X main.instanceID=OpenListFS-desktop-test', '-o', str(ROOT / 'build/bin/openlistfs-gui-desktop-test.exe'), '.'], cwd=ROOT, check=True)
print('Built isolated CDP test driver; production dependencies unchanged')
