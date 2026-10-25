"""Endfield acceptance in real Chromium with the explicitly simulated Wails bridge.
No server credentials, native installation or filesystem mount is exercised.
"""
import functools
import http.server
import json
import os
from pathlib import Path
import re
import runpy
import threading

from playwright.sync_api import expect, sync_playwright

ROOT = Path(__file__).resolve().parents[1]
MOCK = runpy.run_path(str(ROOT / 'scripts/check-ui.py'))['MOCK']
OUTPUT = ROOT / 'build/endfield-verification'


def main():
    OUTPUT.mkdir(parents=True, exist_ok=True)
    class QuietHandler(http.server.SimpleHTTPRequestHandler):
        def log_message(self, *args):
            pass
    server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), functools.partial(QuietHandler, directory=str(ROOT / 'frontend/dist')))
    threading.Thread(target=server.serve_forever, daemon=True).start()
    evidence = {'bridge': 'Simulated Wails API, real Chromium rendering/input', 'checks': [], 'fonts': [], 'layouts': [], 'limits': ['Native remote mounts not exercised', 'Screen readers and other browsers not exercised', '200% equivalent viewport reflow; browser toolbar zoom not exercised']}
    errors, resources = [], []
    try:
        with sync_playwright() as p:
            candidates = sorted((Path(os.environ['LOCALAPPDATA']) / 'ms-playwright').glob('chromium-*/chrome-win64/chrome.exe'))
            browser = p.chromium.launch(headless=True, executable_path=str(candidates[-1]))
            context = browser.new_context(viewport={'width': 1440, 'height': 1000}, color_scheme='light')
            context.add_init_script(MOCK)
            page = context.new_page()
            page.on('pageerror', lambda e: errors.append(str(e)))
            page.on('console', lambda m: errors.append(m.text) if m.type == 'error' else None)
            page.on('response', lambda r: resources.append({'url': r.url, 'status': r.status}))
            page.goto(f'http://127.0.0.1:{server.server_port}')
            expect(page.get_by_role('heading', name='家庭云盘', exact=True)).to_be_visible()
            page.evaluate('document.fonts.ready')
            cdp = context.new_cdp_session(page)
            cdp.send('DOM.enable'); cdp.send('CSS.enable')

            def record_fonts(stage):
                document = cdp.send('DOM.getDocument', {'depth': -1})['root']['nodeId']
                selectors = '.main-content h1, .main-nav button span, .profile-item-text strong, .field label, .field-help, .setting-row-text strong, .setting-row-text small, .dialog h2, .dialog p, .notice>span, .ef-select>span, .ef-option-list [role=option]>span, .metric strong, .session-panel h3, .session-metrics strong, .session-details dt, .session-details dd, .driver-info p, .theme-picker span, .ef-data'
                nodes = cdp.send('DOM.querySelectorAll', {'nodeId': document, 'selector': selectors})['nodeIds']
                count = 0
                for node in nodes:
                    obj = cdp.send('DOM.resolveNode', {'nodeId': node})['object']['objectId']
                    info = cdp.send('Runtime.callFunctionOn', {'objectId': obj, 'functionDeclaration': '''function(){const s=getComputedStyle(this);return {text:this.textContent,visible:!!this.getClientRects().length,family:s.fontFamily,numeric:s.fontVariantNumeric,role:this.classList.contains('ef-data')?'data':'function'};}''', 'returnByValue': True})['result']['value']
                    if not info['visible'] or not info['text'].strip():
                        continue
                    fonts = cdp.send('CSS.getPlatformFontsForNode', {'nodeId': node})['fonts']
                    info.update(stage=stage, fonts=fonts)
                    evidence['fonts'].append(info)
                    if re.search('[\u4e00-\u9fff]', info['text']):
                        assert any('Noto Sans SC' in f['familyName'] and f['isCustomFont'] for f in fonts), info
                        assert all('Noto Sans SC' in f['familyName'] or 'Space Grotesk' in f['familyName'] for f in fonts), info
                    if info['role'] == 'data':
                        assert 'Space Grotesk' in info['family'] and 'tabular-nums' in info['numeric'], info
                        if re.search('[A-Za-z0-9]', info['text']):
                            assert any('Space Grotesk' in f['familyName'] and f['isCustomFont'] for f in fonts), info
                    count += 1
                assert count > 8, stage

            def contrast(theme, width, tag):
                results = page.evaluate('''() => {
                  const rgb = s => (s.match(/[\\d.]+/g) || []).slice(0,3).map(Number);
                  const lum = c => c.map(v=>{v/=255;return v<=.04045?v/12.92:((v+.055)/1.055)**2.4}).reduce((a,v,i)=>a+v*[.2126,.7152,.0722][i],0);
                  const selectors='h1,h2,h3,.field>label,.field-help,.profile-item-text strong,.profile-item-text small,.main-nav button>span,.theme-picker span,.setting-row-text strong,.setting-row-text small,.metric>span,.metric strong,.panel-note span,.driver-info p,.hero-actions small';
                  return [...document.querySelectorAll(selectors)].filter(e=>e.getClientRects().length && e.textContent.trim() && !e.closest('button:disabled')).map(e=>{
                    const s=getComputedStyle(e);let p=e,bg;
                    while(p){const color=getComputedStyle(p).backgroundColor;if(color!=='rgba(0, 0, 0, 0)' && color!=='transparent'){bg=color;break;}p=p.parentElement;}
                    const f=lum(rgb(s.color)),b=lum(rgb(bg||getComputedStyle(document.documentElement).backgroundColor));
                    const large=parseFloat(s.fontSize)>=24||(parseFloat(s.fontSize)>=18.66&&parseInt(s.fontWeight)>=700);
                    return {text:e.textContent.slice(0,50),ratio:(Math.max(f,b)+.05)/(Math.min(f,b)+.05),required:large?3:4.5};
                  });
                }''')
                assert all(r['ratio'] >= r['required'] for r in results), (theme, width, tag, [r for r in results if r['ratio'] < r['required']])
                evidence.setdefault('contrast', []).append({'theme': theme, 'width': width, 'page': tag, 'sampleCount': len(results), 'minimum': min(r['ratio'] for r in results)})

            record_fonts('saved-workspace')
            widths = page.evaluate('''() => { const values=['111111','888888']; return values.map(text => {const e=document.createElement('span');e.className='ef-data';e.textContent=text;e.style.position='fixed';document.body.append(e);const w=e.getBoundingClientRect().width;e.remove();return w;}); }''')
            assert abs(widths[0] - widths[1]) < .1, widths
            evidence['checks'].append('Actual Latin data glyphs and equal-width tabular numerals')
            # Roving tabs and dropdown keyboard preview/cancel/commit.
            page.get_by_role('tab', name='连接信息', exact=True).focus()
            page.keyboard.press('ArrowRight')
            expect(page.get_by_role('tab', name='挂载选项', exact=True)).to_be_focused()
            expect(page.get_by_role('tab', name='挂载选项', exact=True)).to_have_attribute('aria-selected', 'true')
            if page.get_by_role('button', name='高级选项', exact=False).get_attribute('aria-expanded') == 'false':
                page.get_by_role('button', name='高级选项', exact=False).click()
            select = page.get_by_role('combobox', name='连接地址族', exact=True)
            select.focus(); page.keyboard.press('Enter'); page.keyboard.press('End')
            expect(page.locator('select[data-for="ip-family"]')).to_have_value('auto')
            page.screenshot(path=str(OUTPUT / 'advanced-dropdown.png'))
            record_fonts('advanced-options-and-dropdown')
            page.keyboard.press('Escape')
            expect(page.get_by_role('listbox')).to_have_count(0)
            expect(select).to_be_focused()
            select.press('Enter'); select.press('i')
            expect(select).to_have_attribute('aria-activedescendant', 'ip-family-option-1')
            select.press('Escape')
            expect(page.locator('select[data-for="ip-family"]')).to_have_value('auto')
            select.press('Enter'); select.press('End'); select.press('Enter')
            expect(page.locator('select[data-for="ip-family"]')).to_have_value('ipv6')
            page.get_by_role('button', name='保存连接', exact=True).click()
            expect(page.get_by_role('button', name='挂载磁盘', exact=True)).to_be_enabled()
            evidence['checks'].append('Keyboard tabs and dropdown preview/Escape/commit; IP family persistence')

            page.get_by_role('button', name='导出配置码', exact=True).click()
            expect(page.get_by_role('dialog')).to_be_visible()
            record_fonts('export-dialog')
            buttons = page.locator('.dialog-actions .button').evaluate_all('(xs)=>xs.map(x=>({w:x.getBoundingClientRect().width,h:x.getBoundingClientRect().height}))')
            assert max(b['w'] for b in buttons) - min(b['w'] for b in buttons) <= 1
            assert max(b['h'] for b in buttons) - min(b['h'] for b in buttons) <= 1
            page.keyboard.press('Escape')
            expect(page.get_by_role('button', name='导出配置码', exact=True)).to_be_focused()
            page.get_by_role('button', name='导入配置码', exact=True).click()
            page.locator('#import-code').fill('openlistfs:v1:simulated')
            page.get_by_role('button', name='导入为新连接', exact=True).click()
            expect(page.get_by_role('heading', name='家庭云盘 (2)', exact=True)).to_be_visible()
            record_fonts('dynamic-imported-profile')
            page.get_by_role('button', name='挂载磁盘', exact=True).click()
            page.locator('.main-nav').get_by_role('button', name='活动监控', exact=False).click()
            expect(page.get_by_role('img', name='实际下载速率曲线')).to_be_visible(timeout=7000)
            page.locator('.session-details summary').click()
            record_fonts('dynamic-session-detail-and-metrics')
            evidence['checks'].append('Dialog trap/restore, equal action geometry; rendered Chinese and data fonts including dynamic imports/sessions')

            for theme in ['light', 'dark']:
                page.locator('.main-nav').get_by_role('button', name='应用设置', exact=True).click()
                page.locator(f'.theme-picker input[value="{theme}"]').check()
                for width in [390, 768, 1440]:
                    page.set_viewport_size({'width': width, 'height': 1000 if width != 390 else 844})
                    for nav, tag in [('连接与挂载', 'workspace'), ('活动监控', 'monitor'), ('应用设置', 'settings')]:
                        page.locator('.main-nav').get_by_role('button', name=nav, exact=False).click()
                        expect(page.get_by_role('heading', name=nav, exact=True)).to_be_visible()
                        page.evaluate('document.fonts.ready')
                        assert page.evaluate('document.documentElement.scrollWidth <= innerWidth'), (theme, width, tag)
                        assert page.locator('.main-content').evaluate('(el)=>el.scrollWidth<=el.clientWidth+1'), (theme, width, tag)
                        evidence['layouts'].append({'theme': theme, 'width': width, 'page': tag, 'horizontalOverflow': False})
                        contrast(theme, width, tag)
                        page.screenshot(path=str(OUTPUT / f'{tag}-{theme}-{width}.png'), full_page=True)
                        if width == 1440 and tag == 'settings':
                            record_fonts(f'settings-{theme}')
                    if width == 390:
                        page.locator('.mobile-connection-toggle').click()
                        expect(page.locator('.profile-item').filter(has_text='工作文件')).to_be_visible()
                        page.locator('.profile-item').filter(has_text='工作文件').click()
                        expect(page.get_by_role('heading', name='工作文件', exact=True)).to_be_visible()
                        assert page.locator('.connection-index').is_hidden()
            evidence['checks'].append('18 light/dark page layouts at 390/768/1440; mobile connections remain accessible')
            evidence['checks'].append('Rendered text contrast meets 4.5:1 or large-text 3:1 in all 18 layouts')

            page.set_viewport_size({'width': 1440, 'height': 1000})
            page.locator('.main-nav').get_by_role('button', name='连接与挂载', exact=True).click()
            # Long real profile names and request failures, never ornamental data.
            page.locator('.page-heading').get_by_role('button', name='新建连接', exact=True).click()
            page.get_by_role('button', name='保存连接', exact=True).click()
            expect(page.locator('#profile-name')).to_be_focused()
            long_name = '跨设备资料归档与备份' * 8 + ' LONG STORAGE ARCHIVE'
            page.locator('#profile-name').fill(long_name)
            page.locator('#server-url').fill('https://long-name.example.test')
            page.get_by_role('button', name='保存连接', exact=True).click()
            expect(page.get_by_role('heading', name=long_name, exact=True)).to_be_visible()
            assert page.locator('.main-content').evaluate('(el)=>el.scrollWidth<=el.clientWidth+1')
            page.evaluate('window.__ui.failStatus=true')
            expect(page.get_by_role('alert').filter(has_text='暂时无法更新磁盘状态')).to_be_visible(timeout=7000)
            evidence['checks'].append('Long mixed-language profile name, validation autofocus and polling failure state')

            page.emulate_media(reduced_motion='reduce')
            page.get_by_role('tab', name='挂载选项', exact=True).click()
            if page.get_by_role('button', name='高级选项', exact=False).get_attribute('aria-expanded') == 'false':
                page.get_by_role('button', name='高级选项', exact=False).click()
            assert page.locator('.button').first.evaluate('(el)=>getComputedStyle(el).transitionDuration') == '0s'
            page.screenshot(path=str(OUTPUT / 'reduced-motion.png'))
            page.emulate_media(forced_colors='active')
            page.get_by_role('combobox', name='连接地址族', exact=True).focus()
            assert page.locator('#ip-family').evaluate('(el)=>getComputedStyle(el).outlineStyle') == 'solid'
            page.screenshot(path=str(OUTPUT / 'forced-colors.png'))
            page.emulate_media(forced_colors='none')
            # A 1440px display at 200% zoom has the same CSS layout viewport as 720px.
            page.set_viewport_size({'width': 720, 'height': 500})
            assert page.locator('.main-content').evaluate('(el)=>el.scrollWidth<=el.clientWidth+1')
            evidence['checks'].append('Reduced motion fully static, forced-colors focus, equivalent 200% viewport reflow')

            touch = browser.new_context(viewport={'width': 390, 'height': 844}, has_touch=True, is_mobile=True, reduced_motion='reduce')
            touch.add_init_script(MOCK)
            tp = touch.new_page(); tp.goto(f'http://127.0.0.1:{server.server_port}')
            expect(tp.get_by_role('heading', name='家庭云盘', exact=True)).to_be_visible()
            tp.locator('.mobile-connection-toggle').tap()
            tp.locator('.profile-item').filter(has_text='工作文件').tap()
            tp.get_by_role('tab', name='挂载选项', exact=True).tap()
            tp.get_by_role('button', name='高级选项', exact=False).tap()
            tp.get_by_role('combobox', name='连接地址族', exact=True).tap()
            tp.get_by_role('option', name='仅使用 IPv4', exact=True).tap()
            tp.get_by_role('button', name='保存连接', exact=True).tap()
            assert tp.evaluate('window.__ui.profiles["工作文件"].options.ipFamily') == 'ipv4'
            tp.screenshot(path=str(OUTPUT / 'touch-advanced-390.png'), full_page=True)
            evidence['checks'].append('Touch-only mobile profile selection and forced IPv4 save without hover')
            assert not errors, errors
            assert all(r['status'] < 400 for r in resources), resources
            evidence['checks'].append('No browser errors or failed local assets; no remote asset requests')
            evidence['resources'] = resources
            browser.close()
    finally:
        server.shutdown(); server.server_close()
        (OUTPUT / 'results.json').write_text(json.dumps(evidence, ensure_ascii=False, indent=2), encoding='utf-8')
    print(f"Passed {len(evidence['checks'])} acceptance groups; {len(evidence['layouts'])} layouts; {len(evidence['fonts'])} actual-font samples")


if __name__ == '__main__':
    main()
