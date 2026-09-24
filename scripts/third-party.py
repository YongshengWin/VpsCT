#!/usr/bin/env python3
"""Collect original dependency notices without recording machine-local paths."""
import json
import pathlib
import re
import shutil
import subprocess

ROOT = pathlib.Path(__file__).resolve().parents[1]
OUT = ROOT / 'third_party'

def go_modules():
    result = subprocess.run(['go', 'list', '-m', '-json', 'all'], cwd=ROOT, check=True, capture_output=True, text=True)
    decoder = json.JSONDecoder()
    text = result.stdout
    while text.strip():
        text = text.lstrip()
        item, end = decoder.raw_decode(text)
        text = text[end:]
        if not item.get('Main'):
            yield item

def notice_files(directory):
    for path in sorted(directory.rglob('*')):
        rel = path.relative_to(directory)
        if 'node_modules' in rel.parts or not path.is_file() or path.suffix.lower() in {'.go', '.py', '.js', '.ts', '.c', '.h', '.cc', '.cpp'}:
            continue
        if re.match(r'^(licen[cs]e|copying|notice|copyright)(?:[._-].*|$)', path.name, re.I):
            yield path

def collect(kind, name, version, directory, license_id='See license text'):
    safe = re.sub(r'[^A-Za-z0-9_.-]', '_', name + '@' + version)
    target = OUT / kind / safe
    notices = []
    for path in notice_files(directory):
        rel = path.relative_to(directory)
        dest = target / rel
        dest.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(path, dest)
        notices.append(dest.relative_to(ROOT).as_posix())
    if not notices:
        raise RuntimeError(f'No license found for {kind} {name}@{version}; inspect upstream before release')
    return {'ecosystem': kind, 'name': name, 'version': version, 'license': license_id, 'notices': notices}

def main():
    if OUT.exists():
        shutil.rmtree(OUT)
    items = []
    goroot = pathlib.Path(subprocess.check_output(['go', 'env', 'GOROOT'], cwd=ROOT, text=True).strip())
    goversion = subprocess.check_output(['go', 'env', 'GOVERSION'], cwd=ROOT, text=True).strip()
    runtime_notices = []
    for filename in ('LICENSE', 'PATENTS'):
        source = goroot / filename
        if source.is_file():
            dest = OUT / 'go-runtime' / goversion / filename
            dest.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(source, dest)
            runtime_notices.append(dest.relative_to(ROOT).as_posix())
    if not runtime_notices:
        raise RuntimeError('Go runtime license missing')
    items.append({'ecosystem': 'go-runtime', 'name': 'Go standard library and runtime',
                  'version': goversion, 'license': 'BSD-3-Clause', 'notices': runtime_notices})
    for mod in go_modules():
        if 'Dir' not in mod:
            raise RuntimeError(f'Module not downloaded: {mod["Path"]}; run go mod download all')
        items.append(collect('go', mod['Path'], mod['Version'], pathlib.Path(mod['Dir'])))
    lock = json.loads((ROOT / 'web/package-lock.json').read_text())
    for path, item in sorted(lock['packages'].items()):
        if not path or item.get('dev'):
            continue
        directory = ROOT / 'web' / path
        name = path.rsplit('node_modules/', 1)[1]
        items.append(collect('npm', name, item['version'], directory, item.get('license', 'See license text')))
    OUT.mkdir(exist_ok=True)
    (OUT / 'manifest.json').write_text(json.dumps(items, indent=2, ensure_ascii=False) + '\n')
    lines = ['# 第三方声明', '',
        'VpsCT 的许可证适用于本项目原创代码，不替代第三方组件各自的许可证。',
        '下表按锁定的依赖版本生成，原始版权和许可文本位于 `third_party/`，随发行包和容器一起提供。',
        '清单覆盖 Go 模块图及 npm 非开发依赖；部分类型包或命令行依赖不会进入最终浏览器产物。', '',
        '更新依赖后运行 `go mod download all`、`cd web && npm ci`，然后 `python3 scripts/third-party.py`。', '',
        '## 1. 运行时外部组件与数据', '',
        '- [sing-box](https://github.com/SagerNet/sing-box)：由 agent 下载管理员明确选定的官方发行包；VpsCT 不修改、重新编译或分发该内核。遵循上游 GPL-3.0 许可。',
        '- [Snell Server](https://manual.nssurge.com/others/snell.html)：agent 从上游下载指定版本，使用和分发应遵循其自身条款。',
        '- [mieru / mita](https://github.com/enfein/mieru)：agent 从上游下载锁定版本的 mita，遵循上游 GPL-3.0 许可；本仓库不分发该二进制。',
        '- [Caddy](https://caddyserver.com/docs/install)：可选 HTTPS 代理，由安装器从官方软件源安装。',
        '- [ip2region 数据](https://github.com/lionsoul2014/ip2region)：控制端运行时下载 XDB 数据，数据来源与许可见上游。',
        '- 在线 IP 查询服务及连接日志行为见 [隐私与数据说明](docs/privacy.md)。', '',
        '## 2. 构建依赖', '', '| 生态 | 组件 | 版本 | 许可与原始声明 |', '|---|---|---|---|']
    for item in items:
        links = ', '.join(f'[{pathlib.PurePosixPath(p).name}]({p})' for p in item['notices'])
        lines.append(f'| {item["ecosystem"]} | {item["name"]} | {item["version"]} | {item["license"]}; {links} |')
    (ROOT / 'THIRD_PARTY_NOTICES.md').write_text('\n'.join(lines) + '\n')
    print(f'Collected notices for {len(items)} dependencies')

if __name__ == '__main__':
    main()
