#!/usr/bin/env bash
# Fetch a fixed official historical release and verify each GitHub asset digest.
set -euo pipefail
version=${1:?historical version required}
destination=${2:?destination directory required}
[[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9][A-Za-z0-9.-]*)?$ ]] || exit 1
mkdir -p "$destination"
catalog=$(mktemp)
trap 'rm -f -- "$catalog"' EXIT
gh api "repos/YongshengWin/VpsCT/releases/tags/$version" > "$catalog"
for name in install.sh install-agent.sh uninstall.sh SHA256SUMS "ctlvps-$version-linux-amd64.tar.gz" "ctlvps-$version-linux-arm64.tar.gz" ctlvps-agent-linux-amd64 ctlvps-agent-linux-arm64 ctlvps-verify-linux-amd64 ctlvps-verify-linux-arm64; do
 curl -fLsS --connect-timeout 15 --max-time 180 --proto '=https' --proto-redir '=https' "https://github.com/YongshengWin/VpsCT/releases/download/$version/$name" -o "$destination/$name"
done
python3 - "$catalog" "$destination" <<'PY'
import hashlib,json,pathlib,sys
catalog=json.loads(pathlib.Path(sys.argv[1]).read_text())
destination=pathlib.Path(sys.argv[2])
assets={a['name']:a for a in catalog['assets']}
for path in destination.iterdir():
    if not path.is_file(): continue
    digest=assets.get(path.name,{}).get('digest','')
    if not digest.startswith('sha256:') or hashlib.file_digest(path.open('rb'),'sha256').hexdigest()!=digest[7:]:
        raise SystemExit('historical artifact digest mismatch: '+path.name)
print('Verified historical release artifacts:',catalog['tag_name'])
PY
