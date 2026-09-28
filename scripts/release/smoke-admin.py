#!/usr/bin/env python3
"""Test the real embedded Admin in an isolated local server and Chromium.
Only non-sensitive screenshots are included in the server product.
"""
from __future__ import annotations
import hashlib
import json
import os
from pathlib import Path
import socket
import ssl
import subprocess
import tarfile
import tempfile
import time
import urllib.request
import urllib.error
from playwright.sync_api import sync_playwright, expect

ROOT = Path(__file__).resolve().parents[2]
ARCHIVE = ROOT / 'release/RemoteDesk-Server-linux-amd64.tar.gz'

def port(kind: int) -> int:
    with socket.socket(socket.AF_INET, kind) as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]

def main() -> None:
    with tempfile.TemporaryDirectory(prefix='rd-admin-test-') as temp:
        work = Path(temp)
        with tarfile.open(ARCHIVE, 'r:gz') as archive:
            archive.extractall(work, filter='data')
        package = work / 'RemoteDesk-Server'
        binary = package / 'bin/remote-server'
        state = work / 'private-state'
        https_port, udp_port = port(socket.SOCK_STREAM), port(socket.SOCK_DGRAM)
        url = f'https://127.0.0.1:{https_port}'
        subprocess.run([str(binary), 'init', '--dir', str(state), '--hosts', '127.0.0.1',
                        '--listen', f'127.0.0.1:{https_port}', '--stun', f'127.0.0.1:{udp_port}'],
                       check=True, stdout=subprocess.DEVNULL)
        tls = ssl.create_default_context(cafile=str(state / 'server.crt'))
        evidence = package / 'docs/browser-verification'
        evidence.mkdir()
        with (work / 'server.log').open('w') as log:
            server = subprocess.Popen([str(binary), 'run', '--config', str(state / 'server.json')],
                                      stdout=log, stderr=log)
            try:
                for attempt in range(100):
                    if server.poll() is not None:
                        raise RuntimeError('test server exited before readiness')
                    try:
                        with urllib.request.urlopen(url + '/admin/', context=tls, timeout=1) as response:
                            if response.status == 200:
                                break
                    except (OSError, urllib.error.URLError):
                        time.sleep(0.1)
                else:
                    raise RuntimeError('test server did not become ready')
                token = (state / 'admin.token').read_text().strip()
                with sync_playwright() as playwright:
                    browser = playwright.chromium.launch(headless=True)
                    context = browser.new_context(ignore_https_errors=True, viewport={'width':1440,'height':1000})
                    page = context.new_page()
                    errors: list[str] = []
                    page.on('pageerror', lambda err: errors.append(str(err)))
                    page.goto(url + '/admin/', wait_until='networkidle')
                    page.locator('input[type="password"]').fill(token)
                    page.get_by_role('button', name='登录控制台', exact=True).click()
                    expect(page.locator('h1')).to_have_text('运行总览')
                    expect(page.locator('.header-right .el-tag')).to_have_text('服务端在线')
                    names = ['服务器节点','设备管理','会话与中继','性能监控','用户与权限','操作审计','版本管理','系统设置','安全中心']
                    for name in names:
                        page.locator(f'nav button[title="{name}"]').click()
                        expect(page.locator('h1')).to_have_text(name)
                        page.wait_for_timeout(150)
                    page.locator('nav button[title="系统设置"]').click()
                    page.locator('.settings-card input').first.fill('本地验收节点（非生产数据）')
                    page.get_by_role('button', name='保存配置', exact=True).click()
                    expect(page.get_by_text('系统配置已保存', exact=True)).to_be_visible()
                    page.locator('nav button[title="运行总览"]').click()
                    page.get_by_role('button', name='刷新', exact=True).click()
                    expect(page.get_by_text('本地验收节点（非生产数据）', exact=True)).to_be_visible()
                    page.screenshot(path=str(evidence / 'overview-light.png'), full_page=True)
                    page.get_by_role('button', name='切换主题').click()
                    page.screenshot(path=str(evidence / 'overview-dark.png'), full_page=True)
                    page.locator('nav button[title="服务器节点"]').click()
                    page.screenshot(path=str(evidence / 'server-node.png'), full_page=True)
                    page.set_viewport_size({'width':390, 'height':844})
                    page.screenshot(path=str(evidence / 'mobile.png'), full_page=True)
                    page.set_viewport_size({'width':1440, 'height':1000})
                    server.terminate()
                    server.wait(timeout=15)
                    expect(page.locator('.header-right .el-tag')).to_have_text('服务端失联', timeout=20000)
                    assert page.locator('.header-right .status-dot.good').count() == 0
                    page.screenshot(path=str(evidence / 'server-unreachable.png'), full_page=True)
                    assert not errors, f'Uncaught browser errors: {errors}'
                    context.close()
                    browser.close()
                (evidence / 'RESULT.json').write_text(json.dumps({
                    'result':'passed', 'scope':'isolated real server binary + real Chromium',
                    'checks':['login', 'ten navigation pages', 'live server status', 'settings persistence',
                              'light/dark themes', 'mobile render', 'server stop changes status to unreachable',
                              'no uncaught page errors'],
                    'not_tested':['public network reachability', 'Windows remote desktop GPU performance'],
                }, ensure_ascii=False, indent=2), encoding='utf-8')
            finally:
                if server.poll() is None:
                    server.terminate()
                    try:
                        server.wait(timeout=15)
                    except subprocess.TimeoutExpired:
                        server.kill()
                        server.wait()
        files = sorted(p for p in package.rglob('*') if p.is_file() and p.name != 'SHA256SUMS')
        (package/'SHA256SUMS').write_text(''.join(f'{hashlib.sha256(p.read_bytes()).hexdigest()}  ./{p.relative_to(package).as_posix()}\n' for p in files))
        output = ARCHIVE.with_name(ARCHIVE.name + '.verified')
        with tarfile.open(output, 'w:gz') as archive:
            archive.add(package, arcname='RemoteDesk-Server')
        os.replace(output, ARCHIVE)
        print('Admin browser checks passed; non-sensitive screenshots included inside the server product.')

if __name__ == '__main__':
    main()
