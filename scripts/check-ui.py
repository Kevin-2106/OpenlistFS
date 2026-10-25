"""Exercise the built Vue UI with a fake Wails bridge, never real credentials/drives.

Requires Python Playwright and an installed Chromium browser. This checks DOM,
layout and frontend interactions; it does not prove native WebView2/WinFsp behavior.
Run after npm run build: python scripts/check-ui.py
"""
import argparse
import functools
import http.server
import json
import os
from pathlib import Path
import threading

from playwright.sync_api import expect, sync_playwright

ROOT = Path(__file__).resolve().parents[1]
MOCK = r"""(() => {
  const options = {drive:'O:',network:true,readAheadMiB:64,chunkKiB:512,cacheMiB:256,workers:3,ttlMinutes:2,writable:false,proxy:'none',backend:'api',ipFamily:'auto'};
  const fakeProfile = (url, backend='api', drive='O:') => ({url, username:'demo-user', password:'test-password', token:'', options:{...options,backend,drive}});
  const state = window.__ui = {
    active:'家庭云盘', profiles:{'家庭云盘':fakeProfile('https://cloud.example.test'), '工作文件':fakeProfile('https://files.example.test/dav','webdav','P:'), '归档存储':fakeProfile('https://s3.example.test/archive','s3','Q:')},
    options:{...options}, sessions:[], autoMount:false, autostart:false, mounts:0, installs:0, failSave:false, failStatus:false, clipboardOK:true, driverReady:true,
  };
  const clone = value => JSON.parse(JSON.stringify(value));
  const status = () => ({ready:state.driverReady, installed:state.driverReady, version:state.driverReady?'2.1.25156.0':'',architecture:'amd64',dllPath:'C:\\Program Files (x86)\\WinFsp\\bin\\winfsp-x64.dll',code:state.driverReady?'ready':'missing_installation',message:state.driverReady?'WinFsp is ready':'WinFsp is not installed'});
  const App = {
    GetProfiles:async()=>({names:Object.keys(state.profiles),active:state.active,autoMount:state.autoMount}),
    GetProfile:async name=>clone(state.profiles[name]), GetOptions:async()=>clone(state.options), GetAutostart:async()=>state.autostart,
    GetActiveConfig:async()=>({url:'',username:'',password:'',token:''}),
    SaveProfile:async(name,p)=> {if(state.failSave)return '模拟保存失败';state.profiles[name]=clone(p);state.active=name;state.options=clone(p.options);return ''},
    SetActiveProfile:async name=>{state.active=name;state.options=clone(state.profiles[name].options);return ''},
    DeleteProfile:async name=>{state.sessions=state.sessions.filter(s=>s.profile!==name);if(state.failDelete)return '模拟删除保存失败';delete state.profiles[name];state.active=Object.keys(state.profiles)[0]||'';if(state.active)state.options=clone(state.profiles[state.active].options);return ''},
    SetAutostart:async value=>{state.autostart=value;return ''}, SetAutoMount:async value=>{state.autoMount=value;return ''},
    GetStatuses:async()=>{
      if(state.failStatus)throw new Error('offline');
      for(const s of state.sessions){s.bytesFetched+=524288;s.httpReqs+=1;s.uptimeSec+=1}
      return {error:'',sessions:clone(state.sessions)};
    },
    Mount:async o=>{state.mounts++;state.sessions.push({mounted:true,mountpoint:o.drive,profile:state.active,server:state.profiles[state.active].url,uptimeSec:12,readAtCalls:932,httpReqs:44,retries:0,bytesFetched:184549376,avgTtfbMs:28,hitBytes:943718400,missBytes:52428800,hitRatio:94.7,ramInUse:100663296,prefetchBytes:125829120,prefetchWaste:10485760,seekCancels:3});return ''},
    Unmount:async drive=>{state.sessions=state.sessions.filter(s=>s.mountpoint!==drive);return ''},
    GetWinFspStatus:async()=>status(), GetWinFspInstallerStatus:async()=>({available:true,message:'Verified bundled MSI'}),
    InstallWinFsp:async()=>{state.installs++;state.driverReady=true;return ''}, OpenWinFspDownload:async()=>{}, OpenWinFspProject:async()=>{},
    ExportProfileCode:async name=>({name,code:'openlistfs:v1:'+btoa(String.fromCharCode(...new TextEncoder().encode(JSON.stringify({name,profile:state.profiles[name]})))),error:''}),
    ImportProfileCode:async code=>{
      if(!code.startsWith('openlistfs:v1:'))return {error:'配置码格式无效'};
      let name='家庭云盘 (2)';state.profiles[name]=fakeProfile('https://imported.example.test');state.active=name;state.options=clone(state.profiles[name].options);return {name,error:''};
    },
  };
  window.go={main:{App}}; window.runtime={
    WindowIsMaximised:async()=>!!state.maximised,
    WindowMinimise:()=>{state.minimised=true},
    WindowToggleMaximise:()=>{state.maximised=!state.maximised;window.dispatchEvent(new Event('resize'))},
    Quit:()=>{state.hidden=true},
    ClipboardSetText:async text=>{state.clipboard=text;return state.clipboardOK},
    WindowSetSystemDefaultTheme:()=>{state.nativeTheme='system'},
    WindowSetDarkTheme:()=>{state.nativeTheme='dark'},
    WindowSetLightTheme:()=>{state.nativeTheme='light'},
  };
})()
"""


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--browser', help='Optional Chromium executable path')
    parser.add_argument('--output', default=str(ROOT / 'build' / 'ui-verification'))
    args = parser.parse_args()
    output = Path(args.output)
    output.mkdir(parents=True, exist_ok=True)
    handler = functools.partial(http.server.SimpleHTTPRequestHandler, directory=str(ROOT / 'frontend' / 'dist'))
    server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), handler)
    worker = threading.Thread(target=server.serve_forever, daemon=True)
    worker.start()
    checks = []
    try:
        with sync_playwright() as playwright:
            browser_path = args.browser
            if not browser_path:
                cache = Path(os.environ.get('LOCALAPPDATA', '')) / 'ms-playwright'
                candidates = sorted(cache.glob('chromium-*/chrome-win64/chrome.exe'))
                if candidates:
                    browser_path = str(candidates[-1])
            browser = playwright.chromium.launch(headless=True, executable_path=browser_path)
            context = browser.new_context(viewport={'width': 1100, 'height': 820}, device_scale_factor=1, color_scheme='dark')
            context.add_init_script(MOCK)
            page = context.new_page()
            page.set_default_timeout(8000)
            errors = []
            page.on('pageerror', lambda error: errors.append(str(error)))
            page.on('console', lambda message: errors.append(message.text) if message.type == 'error' else None)
            page.goto(f'http://127.0.0.1:{server.server_port}')
            expect(page.locator('.window-brand')).to_be_visible()
            assert page.locator('.window-brand').evaluate('(el)=>{const r=el.getBoundingClientRect();return el.contains(document.elementFromPoint(r.x+12,r.y+12))}')
            expect(page.get_by_role('heading', name='家庭云盘', exact=True)).to_be_visible()
            expect(page.get_by_role('button', name='挂载磁盘', exact=True)).to_be_enabled()
            page.screenshot(path=str(output / 'connections.png'))
            checks.append('Initial saved connection and Endfield desktop workspace')

            # Dirty credentials must never silently mount the old saved server.
            page.locator('#server-url').fill('https://changed.example.test')
            expect(page.get_by_role('button', name='挂载磁盘', exact=True)).to_be_disabled()
            page.locator('.profile-item').filter(has_text='工作文件').click()
            expect(page.get_by_role('dialog')).to_be_visible()
            page.get_by_role('button', name='继续编辑', exact=True).click()
            expect(page.locator('#server-url')).to_have_value('https://changed.example.test')
            page.locator('.profile-item').filter(has_text='工作文件').click()
            page.get_by_role('button', name='放弃修改', exact=True).click()
            expect(page.get_by_role('heading', name='工作文件', exact=True)).to_be_visible()
            expect(page.locator('#server-url')).to_have_value('https://files.example.test/dav')
            checks.append('Unsaved edits guard mounting and switching; cancel preserves draft')

            page.get_by_role('tab', name='挂载选项', exact=True).click()
            expect(page.locator('select[data-for="drive-letter"]')).to_have_value('P:')
            page.get_by_role('combobox', name='网络代理', exact=True).click()
            page.get_by_role('option', name='自定义代理', exact=True).click()
            page.get_by_label('代理地址', exact=True).fill('socks5://127.0.0.1:1080')
            page.get_by_role('button', name='高级选项', exact=False).click()
            page.get_by_label('内存缓存', exact=True).fill('384')
            page.get_by_role('combobox', name='连接地址族', exact=True).click()
            page.get_by_role('option', name='仅使用 IPv6', exact=True).click()
            page.get_by_role('button', name='保存连接', exact=True).click()
            expect(page.get_by_role('button', name='挂载磁盘', exact=True)).to_be_enabled()
            assert page.evaluate('window.__ui.profiles["工作文件"].options.proxy') == 'socks5://127.0.0.1:1080'
            assert page.evaluate('window.__ui.profiles["工作文件"].options.cacheMiB') == 384
            assert page.evaluate('window.__ui.profiles["工作文件"].options.ipFamily') == 'ipv6'
            checks.append('Backend-specific options, proxy, IP family and cache are saved together')

            page.get_by_role('button', name='导出配置码', exact=True).click()
            expect(page.get_by_role('dialog')).to_be_visible()
            expect(page.locator('#export-code')).to_have_value(__import__('re').compile('^openlistfs:v1:'))
            page.get_by_role('button', name='复制配置码', exact=True).click()
            expect(page.get_by_text('配置码已复制。', exact=True)).to_be_visible()
            assert page.evaluate('window.__ui.clipboard.startsWith("openlistfs:v1:")')
            page.evaluate('window.__ui.clipboardOK=false')
            page.get_by_role('button', name='复制配置码', exact=True).click()
            expect(page.get_by_text('复制失败，请在下方选择并手动复制。', exact=True)).to_be_visible()
            page.keyboard.press('Escape')
            expect(page.get_by_role('dialog')).to_have_count(0)
            assert page.locator('#export-code').count() == 0
            checks.append('Export, clipboard success/fallback, Escape and cleared code display')

            page.get_by_role('button', name='导入配置码', exact=True).click()
            page.locator('#import-code').fill('invalid')
            page.get_by_role('button', name='导入为新连接', exact=True).click()
            expect(page.get_by_text('配置码格式无效', exact=True)).to_be_visible()
            page.locator('#import-code').fill('openlistfs:v1:fake-test-data')
            page.get_by_role('button', name='导入为新连接', exact=True).click()
            expect(page.get_by_role('dialog')).to_have_count(0)
            expect(page.get_by_role('heading', name='家庭云盘 (2)', exact=True)).to_be_visible()
            assert page.evaluate('window.__ui.mounts') == 0
            checks.append('Invalid code feedback and duplicate-name import with no auto-mount')

            page.get_by_role('button', name='挂载磁盘', exact=True).click()
            expect(page.get_by_role('button', name='卸载 O:', exact=True).first).to_be_visible()
            page.get_by_role('button', name='活动监控', exact=False).first.click()
            expect(page.get_by_role('heading', name='活动监控', exact=True)).to_be_visible()
            expect(page.locator('.session-panel')).to_have_count(1)
            expect(page.get_by_role('img', name='实际下载速率曲线')).to_be_visible(timeout=6000)
            page.screenshot(path=str(output / 'activity.png'))
            checks.append('Mounted-drive list, live samples and per-drive metrics')

            page.get_by_role('button', name='应用设置', exact=True).click()
            expect(page.get_by_role('button', name='修复 WinFsp', exact=True)).to_be_disabled()
            page.get_by_role('switch', name='开机启动', exact=False).check()
            assert page.evaluate('window.__ui.autostart') is True
            page.screenshot(path=str(output / 'settings.png'))
            checks.append('Settings toggles and disabled repair while a drive is mounted')

            # The app's actual minimum window size must remain usable.
            for size in [(720, 560), (960, 760), (1100, 820)]:
                page.set_viewport_size({'width': size[0], 'height': size[1]})
                for nav_name in ['连接与挂载', '活动监控', '应用设置']:
                    page.locator('.main-nav').get_by_role('button', name=nav_name, exact=False).click()
                    assert page.evaluate('document.documentElement.scrollWidth <= innerWidth'), (size, nav_name, 'body horizontal overflow')
                    assert page.locator('.main-content').evaluate('(el) => el.scrollWidth <= el.clientWidth + 1'), (size, nav_name, 'content horizontal overflow')
                if size == (720, 560):
                    page.locator('.main-nav').get_by_role('button', name='连接与挂载', exact=True).click()
                    page.screenshot(path=str(output / 'minimum-window.png'))
            checks.append('No horizontal overflow at minimum, original and new window sizes')

            page.locator('.main-nav').get_by_role('button', name='连接与挂载', exact=True).click()
            page.get_by_role('button', name='卸载 O:', exact=True).first.click()
            expect(page.locator('.mounted-row')).to_have_count(0)
            page.evaluate('window.__ui.driverReady=false')
            page.locator('.main-nav').get_by_role('button', name='应用设置', exact=True).click()
            page.get_by_role('button', name='重新检测', exact=True).click()
            expect(page.get_by_role('button', name='安装 WinFsp', exact=True)).to_be_enabled()
            page.locator('.main-nav').get_by_role('button', name='连接与挂载', exact=True).click()
            expect(page.get_by_role('button', name='挂载磁盘', exact=True)).to_be_disabled()
            expect(page.get_by_role('button', name='处理环境', exact=False)).to_be_visible()
            page.screenshot(path=str(output / 'missing-driver.png'))
            checks.append('Unmount and missing-driver recovery route')

            page.get_by_role('button', name='删除连接', exact=True).click()
            expect(page.get_by_role('dialog')).to_be_visible()
            page.get_by_role('button', name='取消', exact=True).click()
            assert page.evaluate('Boolean(window.__ui.profiles["家庭云盘 (2)"])')
            checks.append('Delete requires an explicit in-app confirmation')

            # Deletion refreshes all mount statuses, including after a save error.
            page.evaluate("window.__ui.sessions=[{mountpoint:'O:',profile:'家庭云盘 (2)',bytesFetched:0},{mountpoint:'P:',profile:'工作文件',bytesFetched:0}];window.__ui.failDelete=true")
            page.get_by_role('button', name='删除连接', exact=True).click()
            expect(page.get_by_text('将先卸载此连接的所有已挂载磁盘，再删除配置。删除后无法恢复此配置。', exact=True)).to_be_visible()
            page.get_by_role('dialog').get_by_role('button', name='删除连接', exact=True).click()
            expect(page.get_by_role('alert').filter(has_text='模拟删除保存失败')).to_be_visible()
            assert page.evaluate('Boolean(window.__ui.profiles["家庭云盘 (2)"])')
            assert page.evaluate('window.__ui.sessions.length===1 && window.__ui.sessions[0].profile==="工作文件"')
            page.evaluate('window.__ui.failDelete=false')
            page.get_by_role('button', name='删除连接', exact=True).click()
            page.get_by_role('dialog').get_by_role('button', name='删除连接', exact=True).click()
            expect(page.get_by_role('status').filter(has_text='连接已卸载并删除。')).to_be_visible()
            assert page.evaluate('!window.__ui.profiles["家庭云盘 (2)"]')
            checks.append('Deletion refreshes disks, preserves other connections and exposes failed-save retry')

            page.get_by_role('button', name='最小化', exact=True).click()
            page.get_by_role('button', name='最大化', exact=True).click()
            page.get_by_role('button', name='关闭到托盘', exact=True).click()
            assert page.evaluate('window.__ui.minimised && window.__ui.maximised && window.__ui.hidden')
            assert page.locator('.window-bar').evaluate("el=>getComputedStyle(el).getPropertyValue('--wails-draggable').trim()==='drag'")
            assert page.locator('.window-controls').evaluate("el=>getComputedStyle(el).getPropertyValue('--wails-draggable').trim()==='no-drag'")
            checks.append('Custom 38px window bar forwards controls and separates drag targets')

            page.locator('.page-heading').get_by_role('button', name='新建连接', exact=True).click()
            page.get_by_role('button', name='保存连接', exact=True).click()
            expect(page.get_by_text('给连接起一个名称。', exact=True)).to_be_visible()
            page.get_by_label('连接名称', exact=False).fill('测试连接')
            page.get_by_label('服务器地址', exact=False).fill('https://new.example.test')
            page.evaluate('window.__ui.failSave=true')
            page.get_by_role('button', name='保存连接', exact=True).click()
            expect(page.get_by_role('alert').filter(has_text='模拟保存失败')).to_be_visible()
            expect(page.get_by_label('连接名称', exact=False)).to_have_value('测试连接')
            page.evaluate('window.__ui.failSave=false')
            page.get_by_role('button', name='保存连接', exact=True).click()
            expect(page.get_by_role('heading', name='测试连接', exact=True)).to_be_visible()
            checks.append('New connection validation, failed-save draft preservation, successful retry')

            # Each palette is a real rendered theme; persist explicit preference,
            # then respond to OS changes only while using the system preference.
            page.locator('.main-nav').get_by_role('button', name='应用设置', exact=True).click()
            page.locator('.theme-picker label').filter(has_text='浅色').click()
            expect(page.locator('html')).to_have_attribute('data-theme', 'light')
            assert page.evaluate('localStorage.getItem("openlistfs.appearance")') == 'light'
            page.screenshot(path=str(output / 'settings-light.png'))
            page.locator('.main-nav').get_by_role('button', name='连接与挂载', exact=True).click()
            page.reload()
            expect(page.get_by_role('heading', name='家庭云盘', exact=True)).to_be_visible()
            expect(page.locator('html')).to_have_attribute('data-theme', 'light')
            assert page.evaluate('window.__ui.nativeTheme') == 'light'
            page.screenshot(path=str(output / 'connections-light.png'))
            page.emulate_media(color_scheme='dark')
            expect(page.locator('html')).to_have_attribute('data-theme', 'light')
            page.locator('.main-nav').get_by_role('button', name='应用设置', exact=True).click()
            page.locator('.theme-picker label').filter(has_text='跟随系统').click()
            expect(page.locator('html')).to_have_attribute('data-theme', 'dark')
            assert page.evaluate('window.__ui.nativeTheme') == 'system'
            page.emulate_media(color_scheme='light')
            expect(page.locator('html')).to_have_attribute('data-theme', 'light')
            page.locator('.theme-picker label').filter(has_text='深色').click()
            expect(page.locator('html')).to_have_attribute('data-theme', 'dark')
            assert page.evaluate('window.__ui.nativeTheme') == 'dark'
            page.emulate_media(color_scheme='light')
            expect(page.locator('html')).to_have_attribute('data-theme', 'dark')
            page.screenshot(path=str(output / 'settings-dark.png'))
            checks.append('Light/dark palettes, persisted preference, live OS following and manual override')

            assert not errors, errors
            checks.append('No uncaught browser errors')
            browser.close()
    finally:
        server.shutdown()
        server.server_close()
    (output / 'results.json').write_text(json.dumps({'checks': checks, 'count': len(checks), 'bridge': 'fake Wails API; no native installs or mounts'}, ensure_ascii=False, indent=2), encoding='utf-8')
    print(f'Passed {len(checks)} UI checks; screenshots: {output}')


if __name__ == '__main__':
    main()
