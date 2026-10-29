#!/usr/bin/env bash
# Rapig 升级（Linux）：备份 → 原子替换二进制 → 重启 → 健康检查，失败自动回滚。
#
# 用法：scripts/upgrade.sh /path/to/rapig.new
# 环境变量：RAPIG_BIN（默认 <home>/rapig）、RAPIG_SERVICE（默认 rapig）、
#           RAPIG_HEALTH_URL（默认 http://127.0.0.1:50011/health）。
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HOME_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
BIN="${RAPIG_BIN:-$HOME_DIR/rapig}"
SERVICE="${RAPIG_SERVICE:-rapig}"
URL="${RAPIG_HEALTH_URL:-http://127.0.0.1:50011/health}"
NEW="${1:-}"

log() { printf '[upgrade] %s\n' "$*"; }
die() { printf '[upgrade] ERROR: %s\n' "$*" >&2; exit 1; }

[[ -n "$NEW" ]] || die "usage: upgrade.sh /path/to/rapig.new"
[[ -f "$NEW" ]] || die "new binary not found: $NEW"
[[ -x "$BIN" ]] || die "current binary not found: $BIN"

log "1/5 backup"
RAPIG_BIN="$BIN" RAPIG_DATA_DIR="${RAPIG_DATA_DIR:-}" "$SCRIPT_DIR/backup.sh" || die "backup failed, aborting upgrade"

log "2/5 stage $NEW next to $BIN"
STAGE="$BIN.new.$$"
cp "$NEW" "$STAGE"
chmod 755 "$STAGE"

OLD="$BIN.old.$$"
cp "$BIN" "$OLD"

restart() {
  if systemctl list-unit-files "${SERVICE}.service" >/dev/null 2>&1; then
    sudo systemctl restart "$SERVICE"
  elif [[ -n "${RAPIG_RESTART_CMD:-}" ]]; then
    eval "$RAPIG_RESTART_CMD"
  else
    die "no systemd unit $SERVICE and RAPIG_RESTART_CMD is not set"
  fi
}
healthy() {
  for _ in $(seq 1 30); do
    if curl -fsS --max-time 2 "$URL" >/dev/null 2>&1; then return 0; fi
    sleep 1
  done
  return 1
}

log "3/5 swap binary"
mv -f "$STAGE" "$BIN"

log "4/5 restart $SERVICE"
if ! restart || ! healthy; then
  log "health check failed, rolling back binary"
  mv -f "$OLD" "$BIN"
  restart || true
  die "upgrade failed; previous binary restored (check logs: journalctl -u $SERVICE)"
fi

log "5/5 OK: $("$BIN" version 2>/dev/null | head -1 || true)"
rm -f "$OLD"
