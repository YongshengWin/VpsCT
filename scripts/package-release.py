#!/usr/bin/env python3
"""Build self-contained release attachments from make release's binaries."""
import argparse
import hashlib
import json
import pathlib
import re
import shutil
import tarfile
import tempfile

ROOT = pathlib.Path(__file__).resolve().parents[1]
ROOT_DOCUMENTS = (
    'README.md', 'AGENTS.md', 'CHANGELOG.md', 'CONTRIBUTING.md',
    'CODE_OF_CONDUCT.md', 'SECURITY.md', 'LICENSE', 'THIRD_PARTY_NOTICES.md',
)

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--version', required=True)
    parser.add_argument('--repository', required=True)
    parser.add_argument('--security-epoch', type=int, default=1)
    args = parser.parse_args()
    if args.security_epoch < 1: parser.error('security epoch must be positive')
    if not re.fullmatch(r'v\d+\.\d+\.\d+(?:-[A-Za-z0-9][A-Za-z0-9.-]*)?', args.version):
        parser.error('version must be vX.Y.Z, optionally with a prerelease suffix')
    if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*', args.repository):
        parser.error('repository must be OWNER/REPO')
    required = [*ROOT_DOCUMENTS, 'third_party/manifest.json', 'docs/operations.md', 'uninstall.sh']
    required += [f'bin/{name}-linux-{arch}' for name in ('ctlvpsd', 'ctlvps-agent') for arch in ('amd64', 'arm64')]
    for name in required:
        if not (ROOT / name).is_file():
            parser.error(f'missing release input: {name}')
    out = ROOT / 'release' / args.version
    if out.exists():
        shutil.rmtree(out)
    out.mkdir(parents=True)
    installer = (ROOT / 'install.sh').read_text().replace("REPOSITORY='__REPOSITORY__'", f"REPOSITORY='{args.repository}'").replace("VERSION='__VERSION__'", f"VERSION='{args.version}'")
    (out / 'install.sh').write_text(installer)
    (out / 'install.sh').chmod(0o755)
    shutil.copy2(ROOT / 'uninstall.sh', out / 'uninstall.sh')
    (out / 'uninstall.sh').chmod(0o755)
    agent_installer = (ROOT / 'internal/assets/install-agent.sh').read_text().replace("RELEASE_VERSION='__VERSION__'", f"RELEASE_VERSION='{args.version}'")
    (out / 'install-agent.sh').write_text(agent_installer)
    for arch in ('amd64', 'arm64'):
        with tempfile.TemporaryDirectory() as tmp:
            package = pathlib.Path(tmp)
            shutil.copy2(ROOT / f'bin/ctlvpsd-linux-{arch}', package / 'ctlvpsd')
            shutil.copy2(ROOT / f'bin/ctlvps-verify-linux-{arch}', package / 'ctlvps-verify')
            shutil.copy2(out / 'install.sh', package / 'install.sh')
            shutil.copy2(out / 'install-agent.sh', package / 'install-agent.sh')
            (package / 'agents').mkdir()
            for agent_arch in ('amd64', 'arm64'):
                name = f'ctlvps-agent-linux-{agent_arch}'
                shutil.copy2(ROOT / 'bin' / name, package / 'agents' / name)
            shutil.copy2(ROOT / 'deploy/ctlvpsd.service', package / 'ctlvpsd.service')
            shutil.copy2(ROOT / 'deploy/ctlvps-maintenance.service', package / 'ctlvps-maintenance.service')
            shutil.copy2(ROOT / 'deploy/ctlvpsd.env.example', package / 'ctlvpsd.env.example')
            shutil.copy2(ROOT / 'uninstall.sh', package / 'uninstall.sh')
            for name in ROOT_DOCUMENTS:
                shutil.copy2(ROOT / name, package / name)
            shutil.copytree(ROOT / 'third_party', package / 'third_party')
            shutil.copytree(ROOT / 'docs', package / 'docs')
            (package / 'VERSION').write_text(args.version + '\n')
            (package / 'REPOSITORY').write_text(args.repository + '\n')
            def metadata(info):
                info.uid = info.gid = 0
                info.uname = info.gname = 'root'
                info.mtime = 0
                info.mode = 0o755 if info.isdir() or info.name == 'ctlvpsd' or info.name.startswith('agents/') else 0o644
                return info
            with tarfile.open(out / f'ctlvps-{args.version}-linux-{arch}.tar.gz', 'w:gz') as archive:
                for path in sorted(package.iterdir()):
                    archive.add(path, arcname=path.name, filter=metadata)
    for arch in ('amd64','arm64'):
        shutil.copy2(ROOT / f'bin/ctlvps-agent-linux-{arch}', out / f'ctlvps-agent-linux-{arch}')
        shutil.copy2(ROOT / f'bin/ctlvps-verify-linux-{arch}', out / f'ctlvps-verify-linux-{arch}')
    for name in ('LICENSE', 'THIRD_PARTY_NOTICES.md'):
        shutil.copy2(ROOT / name, out / name)
    manifest = []
    def signed(path, component, version, arch):
        manifest.append({'path': str(path.relative_to(ROOT)), 'identity': {
            'product': 'VpsCT', 'component': component, 'version': version,
            'arch': arch, 'epoch': args.security_epoch}})
    signed(out / 'install.sh', 'installer', args.version, 'all')
    for arch in ('amd64', 'arm64'):
        signed(out / f'ctlvps-{args.version}-linux-{arch}.tar.gz', 'controller', args.version, arch)
        signed(out / f'ctlvps-agent-linux-{arch}', 'agent', args.version, arch)
        signed(out / f'ctlvps-verify-linux-{arch}', 'verifier', args.version, arch)
    (out / 'signing-manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')
    with (out / 'SHA256SUMS').open('w') as sums:
        for path in sorted(out.iterdir()):
            if path.name != 'SHA256SUMS':
                sums.write(f'{hashlib.file_digest(path.open("rb"), "sha256").hexdigest()}  {path.name}\n')
    print(f'Release attachments: {out.relative_to(ROOT)}')

if __name__ == '__main__':
    main()
