#!/bin/sh
set -eu
umask 077
fifo=/var/run/claude-guard/dns-capture.pipe
mkdir -p /var/run/claude-guard/events
[ -p "$fifo" ] || mkfifo "$fifo"
cap_pid=''
obs_pid=''
cleanup() {
  trap - EXIT INT TERM
  [ -z "$cap_pid" ] || kill "$cap_pid" 2>/dev/null || true
  [ -z "$obs_pid" ] || kill "$obs_pid" 2>/dev/null || true
  wait 2>/dev/null || true
}
trap cleanup EXIT INT TERM
/usr/bin/tcpdump -i any -n -U -s 65535 -w "$fifo" '(udp and (src port 53 or src port 11400 or src port 15353 or src port 15355)) or (tcp dst port 443 and (src net 192.168.10.0/24 or src net 192.168.20.0/24 or src net fd00:1234:5678::/48) and not src host 192.168.10.1 and not src host 192.168.20.1)' 2>/dev/null &
cap_pid=$!
/usr/bin/lua /usr/libexec/claude-guard/observer.lua < "$fifo" &
obs_pid=$!
wait "$obs_pid"
