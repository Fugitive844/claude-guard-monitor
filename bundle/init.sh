#!/bin/sh /etc/rc.common
USE_PROCD=1
START=20
STOP=90
start_service() {
  mkdir -p /var/run/claude-guard/events
  chmod 700 /var/run/claude-guard /var/run/claude-guard/events
  procd_open_instance guard
  procd_set_param command /usr/bin/lua /usr/libexec/claude-guard/cli.lua enforce
  procd_set_param respawn 3600 5 5
  procd_set_param stderr 1
  procd_close_instance
  procd_open_instance dns-observer
  procd_set_param command /bin/sh /usr/libexec/claude-guard/capture.sh
  procd_set_param respawn 3600 5 5
  procd_set_param stderr 1
  procd_close_instance
}
stop_service() {
  # Intentionally retain the last known destination block on service stop.
  logger -t claude-guard 'Stopped observer; existing independent nft guard retained. Explicit remove is required for rollback.'
}
