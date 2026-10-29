#!/usr/bin/env bash
# Rapig 最小健康检查（Linux，给 cron/systemd timer 用）。
#
# 检查项：/health、磁盘余量、进程 RSS、最近一次备份的年龄。
# 环境变量：RAPIG_HEALTH_URL（默认 http://127.0.0.1:50011/health）、
#           RAPIG_BIN / RAPIG_DATA_DIR / RAPIG_BACKUP_DIR（同 backup.sh）、
#           RAPIG_DISK_MIN_PCT=10、RAPIG_RSS_MAX_MB=400、RAPIG_BACKUP_MAX_HOURS=30。
# 退出码：0 全部正常；1 有告警（可接告警系统）。
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HOME_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
BIN="${RAPIG_BIN:-$HOME_DIR/rapig}"
DATA_DIR="${RAPIG_DATA_DIR:-$(dirname "$BIN")/data}"
BACKUP_DIR="${RAPIG_BACKUP_DIR:-$(dirname "$BIN")/backup}"
URL="${RAPIG_HEALTH_URL:-http://127.0.0.1:50011/health}"
DISK_MIN="${RAPIG_DISK_MIN_PCT:-10}"
RSS_MAX="${RAPIG_RSS_MAX_MB:-400}"
BACKUP_MAX_H="${RAPIG_BACKUP_MAX_HOURS:-30}"

bad=0
say() { printf '[health] %s\n' "$*"; }
warn() { say "WARN  $*"; bad=1; }

# 1) HTTP 健康
if body="$(curl -fsS --max-time 5 "$URL" 2>/dev/null)"; then
  say "OK    health $body"
else
  warn "health endpoint unreachable: $URL"
fi

# 2) 磁盘余量（数据目录所在分区）
if [[ -d "$DATA_DIR" ]]; then
  pct="$(df -P "$DATA_DIR" | awk 'NR==2 {gsub("%","",$5); print $5}')"
  if [[ -n "$pct" ]] && (( pct < 100 - DISK_MIN )); then
    say "OK    disk free above ${DISK_MIN}% (used ${pct}%)"
  else
    warn "disk nearly full (used ${pct}%, threshold ${DISK_MIN}% free)"
  fi
else
  warn "data dir missing: $DATA_DIR"
fi

# 3) 进程 RSS（只看 rapig serve 的所有进程之和，单位 KB）
rss_kb="$(ps -eo rss=,args= 2>/dev/null | awk -v b="$BIN" '$0 ~ b && /serve/ {s += $1} END {print s+0}')"
if (( rss_kb > 0 )); then
  rss_mb=$(( rss_kb / 1024 ))
  if (( rss_mb <= RSS_MAX )); then
    say "OK    rss ${rss_mb} MiB (limit ${RSS_MAX})"
  else
    warn "rss ${rss_mb} MiB above ${RSS_MAX} MiB"
  fi
else
  warn "no 'rapig serve' process found for $BIN"
fi

# 4) 备份年龄
newest="$(find "$BACKUP_DIR" -mindepth 1 -maxdepth 1 -type d -printf '%f %T@\n' 2>/dev/null | sort -k2 -n | tail -1)"
if [[ -n "$newest" ]]; then
  ts="${newest%% *}"; epoch="${newest##* }"
  age_h=$(( ( $(date +%s) - ${epoch%.*} ) / 3600 ))
  if (( age_h <= BACKUP_MAX_H )); then
    say "OK    last backup ${age_h}h ago ($ts)"
  else
    warn "last backup ${age_h}h ago (> ${BACKUP_MAX_H}h)"
  fi
else
  warn "no backups in $BACKUP_DIR"
fi

if (( bad == 0 )); then say "HEALTH OK"; else say "HEALTH WARN"; fi
exit "$bad"
