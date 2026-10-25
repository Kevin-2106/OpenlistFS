"""Render the original SVG to the app, tray, Wails and setup icon resources.

Requires Pillow and Playwright Chromium. The SVG is the editable source of truth.
"""
from pathlib import Path
import os
import shutil
from PIL import Image
from playwright.sync_api import sync_playwright

ROOT = Path(__file__).resolve().parents[1]

def main():
    svg = ROOT / 'assets/icon.svg'
    shutil.copyfile(svg, ROOT / 'frontend/public/favicon.svg')
    with sync_playwright() as p:
        cached = sorted((Path(os.environ['LOCALAPPDATA']) / 'ms-playwright').glob('chromium-*/chrome-win64/chrome.exe'))
        with p.chromium.launch(executable_path=str(cached[-1]) if cached else None) as browser:
            page = browser.new_page(viewport={'width': 256, 'height': 256}, device_scale_factor=4)
            page.set_content('<style>body{margin:0;background:transparent}</style>' + svg.read_text())
            raw = ROOT / 'build/appicon.png'
            page.locator('svg').screenshot(path=str(raw), omit_background=True)
    with Image.open(raw) as image:
        image = image.resize((256, 256), Image.Resampling.LANCZOS)
        image.save(ROOT / 'assets/icon.png')
        image.save(raw)
        image.save(ROOT / 'assets/icon.ico', sizes=[(n, n) for n in (16, 20, 24, 32, 40, 48, 64, 128, 256)])
    shutil.copyfile(ROOT / 'assets/icon.ico', ROOT / 'build/windows/icon.ico')
    print('Rendered SVG, PNG and nine ICO sizes for UI, app, tray and setup.')

if __name__ == '__main__':
    main()
