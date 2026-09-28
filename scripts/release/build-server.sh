#!/usr/bin/env bash
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
cd "$ROOT"
[[ $(uname -s) == Linux && $(uname -m) == x86_64 ]] || { echo 'Build this release on Linux amd64.' >&2; exit 1; }
command -v node >/dev/null; command -v npm >/dev/null; command -v go >/dev/null
(
 cd apps/admin
 # Direct dependencies are pinned. The resolved lock is included in the server package.
 if [[ ! -f package-lock.json ]]; then npm install --package-lock-only --ignore-scripts; fi
 npm ci
 npm test
 npm run build
)
python3 scripts/release/verify-assets.py
# Never build a server release with the legacy management page or placeholder assets.
go test -count=1 ./...
go vet ./...
mkdir -p release
STAGE=$(mktemp -d)
trap 'rm -rf "$STAGE"' EXIT
PACKAGE="$STAGE/RemoteDesk-Server"
mkdir -p "$PACKAGE/bin" "$PACKAGE/licenses" "$PACKAGE/docs"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o "$PACKAGE/bin/remote-server" ./cmd/remote-server
cp scripts/release/install-server.sh "$PACKAGE/install.sh"
cp deploy/remotedesk-server.service "$PACKAGE/remotedesk-server.service"
cp docs/TWO_PACKAGES.md "$PACKAGE/docs/DEPLOYMENT.md"
cp apps/admin/package-lock.json "$PACKAGE/docs/admin-package-lock.json"
cp licenses/Go-LICENSE.txt "$PACKAGE/licenses/Go-LICENSE.txt"
for entry in vue element-plus '@element-plus/icons-vue'; do
 for license in LICENSE LICENSE.md LICENSE.txt; do
  if [[ -f "apps/admin/node_modules/$entry/$license" ]]; then
   name=${entry//\//-};cp "apps/admin/node_modules/$entry/$license" "$PACKAGE/licenses/$name-LICENSE.txt";break
  fi
 done
done
printf 'version=0.3.0\nbuild_commit=%s\n' "$(git rev-parse HEAD 2>/dev/null || printf 'local-source')" > "$PACKAGE/BUILD-INFO.txt"
go version >> "$PACKAGE/BUILD-INFO.txt"
chmod 755 "$PACKAGE/bin/remote-server" "$PACKAGE/install.sh"
(cd "$PACKAGE" && find . -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 sha256sum > SHA256SUMS)
tar -czf release/RemoteDesk-Server-linux-amd64.tar.gz -C "$STAGE" RemoteDesk-Server
printf '\nServer release: %s/release/RemoteDesk-Server-linux-amd64.tar.gz\n' "$ROOT"
