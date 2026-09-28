#!/usr/bin/env bash
set -euo pipefail
umask 077
usage() { echo 'Usage: sudo bash install.sh --host remote.example.com'; }
HOST=''
while (($#)); do
 case "$1" in
  --host) [[ $# -ge 2 ]] || { usage; exit 2; }; HOST=$2; shift 2;;

  --help|-h) usage; exit 0;;
  *) usage; exit 2;;
 esac
done
[[ $EUID == 0 ]] || { echo 'Use sudo to install a system service.' >&2; exit 1; }
[[ $(uname -m) == x86_64 ]] || { echo 'This package is for Linux amd64.' >&2; exit 1; }
[[ $HOST =~ ^[a-zA-Z0-9][a-zA-Z0-9.-]*$ ]] || { echo 'A DNS hostname or IPv4 address is required.' >&2; exit 2; }
ROOT=$(cd "$(dirname "$0")" && pwd)
command -v systemctl >/dev/null; command -v runuser >/dev/null
[[ ! -e /opt/remotedesk/server/bin/remote-server && ! -e /var/lib/remotedesk/server/server.json ]] || { echo 'An existing installation/state was found; no binaries, keys, or configuration were overwritten.' >&2; exit 1; }
(cd "$ROOT" && sha256sum -c SHA256SUMS)
if ! id remotedesk >/dev/null 2>&1; then useradd --system --home-dir /var/lib/remotedesk --shell /usr/sbin/nologin remotedesk; fi
install -d -m 755 /opt/remotedesk/server/bin
install -m 755 "$ROOT/bin/remote-server" /opt/remotedesk/server/bin/remote-server
install -d -m 700 -o remotedesk -g remotedesk /var/lib/remotedesk /var/lib/remotedesk/server
runuser -u remotedesk -- /opt/remotedesk/server/bin/remote-server init \
 --dir /var/lib/remotedesk/server --listen 0.0.0.0:8443 --stun 0.0.0.0:3478 \
 --hosts "$HOST"
install -m 644 "$ROOT/remotedesk-server.service" /etc/systemd/system/remotedesk-server.service
systemctl daemon-reload
systemctl enable --now remotedesk-server
systemctl is-active --quiet remotedesk-server
printf '\nAdmin URL: https://%s:8443/admin/\nAccount: admin\n' "$HOST"
echo 'Read your unique admin password: sudo cat /var/lib/remotedesk/server/admin.token'
echo 'Open your cloud/firewall ports: 8443/TCP, 3478/UDP. This script does not disable the firewall.'
echo 'The generated TLS certificate is private/self-signed. Establish certificate trust or install a trusted certificate before use.'
echo 'Never distribute server.key, admin.token, enrollment.token, or server.json publicly.'
