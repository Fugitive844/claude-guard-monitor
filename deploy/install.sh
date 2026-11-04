#!/bin/sh
set -eu
# Bootstrap the monitor only. Guard installation is performed in its web UI.
[ "$(id -u)" = 0 ] || { echo 'Run as root'; exit 1; }
[ -f /etc/rc.common ] && [ -d /overlay ] || { echo 'Requires OpenWrt/iStoreOS with persistent /overlay'; exit 1; }
[ "$(uname -m)" = x86_64 ] || { echo 'This package is for x86_64 only'; exit 1; }
[ "$#" = 2 ] || { echo 'Usage: sh install.sh LAN_IP:9088 MANAGEMENT_CIDR[,CIDR]'; exit 1; }
base=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
binary="$base/claude-guard-monitor-linux-amd64"
[ -f "$binary" ] || binary="$base/../dist/claude-guard-monitor-linux-amd64"
[ -f "$binary" ] && [ -f "$base/claude-guard-monitor.init" ] || { echo 'Missing package files'; exit 1; }
if [ -e /usr/bin/claude-guard-monitor ] || [ -e /etc/claude-guard-monitor/config.json ]; then
    echo 'Existing installation found; use the documented update procedure.'; exit 1
fi
cp "$binary" /usr/bin/claude-guard-monitor.next
chmod 700 /usr/bin/claude-guard-monitor.next
/usr/bin/claude-guard-monitor.next -init -listen "$1" -allow-cidrs "$2"
mv /usr/bin/claude-guard-monitor.next /usr/bin/claude-guard-monitor
cp "$base/claude-guard-monitor.init" /etc/init.d/claude-guard-monitor
chmod 700 /etc/init.d/claude-guard-monitor
/etc/init.d/claude-guard-monitor enable
/etc/init.d/claude-guard-monitor start
echo "Open https://$1 ; credentials: /etc/claude-guard-monitor/credentials.txt"
echo 'Use Deployment & Management to check dependencies and install the guard.'
