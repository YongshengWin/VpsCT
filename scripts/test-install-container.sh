#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
assets_dir=$(cd "${1:?usage: test-install-container.sh RELEASE_DIRECTORY}" && pwd)
image="ctlvps-installer-test:local"
docker build -f scripts/installer-test/Dockerfile -t "$image" .
docker run --rm --mount "type=bind,src=$PWD,dst=/src,readonly" \
  --mount "type=bind,src=$assets_dir,dst=/assets,readonly" \
  "$image" bash /src/scripts/installer-test/run.sh
docker run --rm --mount "type=bind,src=$PWD,dst=/src,readonly" \
  --mount "type=bind,src=$assets_dir,dst=/assets,readonly" \
  "$image" bash /src/scripts/installer-test/caddy.sh
