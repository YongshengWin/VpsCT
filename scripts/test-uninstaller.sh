#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
# shellcheck source=uninstall.sh
source ./uninstall.sh
reject_args() {
  if (ROLE=''; parse_args "$@") >/dev/null 2>&1; then
    printf 'Invalid uninstall arguments accepted\n' >&2; exit 1
  fi
}
reject_args
reject_args --agent --controller
reject_args --all --agent
reject_args --agent --remove-caddy
reject_args --controller --remove-caddy
reject_args --purge
reject_args --controller --unknown
(ROLE=''; parse_args --all --purge --remove-caddy --dry-run --yes)
bash uninstall.sh --help >/dev/null
printf 'Uninstaller argument checks passed\n'
