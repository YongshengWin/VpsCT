#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
# shellcheck source=install.sh
source ./install.sh
reject() {
  if "$@"; then printf "Invalid argument accepted by %s\n" "$1" >&2; return 1; fi
}
valid_repo 'example/ctlvps'
reject valid_repo 'example/../../elsewhere'
valid_version v1.2.3
valid_version v1.2.3-rc.1
[[ "$(version_order v1.2.3-rc.1)" == '1.2.3~rc.1' ]]
reject valid_version '../../tmp'
valid_domain panel.example.com
valid_domain a-b.example.com
reject valid_domain 'panel.example.com;id'
reject valid_domain 'a..example.com'
reject valid_domain 'https://example.com'
reject valid_domain 'example.com.'
reject valid_domain '127.0.0.1'
reject valid_domain '-bad.example.com'
valid_site_url https://panel.example.com:8443
reject valid_site_url https://panel.example.com:65536
reject valid_site_url https://panel.example.com:0
reject valid_site_url http://panel.example.com
reject valid_site_url 'https://user:password@example.com'
reject valid_site_url $'https://example.com\nEVIL=1'
bash install.sh --help >/dev/null
if bash install.sh --repo >/dev/null 2>&1; then
  printf 'missing argument accepted\n' >&2; exit 1
fi
printf 'Installer argument checks passed\n'
