#!/usr/bin/env bash
# Rapig 本机备份（Linux）。
#
# 目录默认跟随部署：脚本在 <home>/scripts/ 下时，<home>/rapig 是二进制，
# <home>/data 是数据，<home>/backup 是备份。也可用环境变量覆盖。
#
# 快照布局（每个快照一个目录，名字是 UTC 时间）：
#   backup/<YYYYmmdd-HHMMSS>/
#     db/rapig.db      一致性快照（sqlite3 .backup 或 python3 backup API）
#     storage/         rsync 硬链增量（-_link-dest 指向上一个快照）
#     bootstrap/       初始管理员口令（600，单独保管）
#     manifest.json    字节数、db sha256、blob 数量、版本
#
# 保留策略：最近 7 个快照 + 最多 4 个周日 + 最多 3 个每月 1 号，其余删除。
# 退出码：0 成功；非 0 失败（可用于 cron 告警）。
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HOME_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

BIN="${RAPIG_BIN:-$HOME_DIR/rapig}"
DATA_DIR="${RAPIG_DATA_DIR:-$(dirname "$BIN")/data}"
BACKUP_DIR="${RAPIG_BACKUP_DIR:-$(dirname "$BIN")/backup}"
DB_PATH="$DATA_DIR/db/rapig.db"

log() { printf '[backup %s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
die() { printf '[backup] ERROR: %s\n' "$*" >&2; exit 1; }

[[ -x "$BIN" ]] || die "binary not found or not executable: $BIN (set RAPIG_BIN)"
[[ -f "$DB_PATH" ]] || die "database not found: $DB_PATH (set RAPIG_DATA_DIR)"

TS="$(date -u +%Y%m%d-%H%M%S)"
if [[ -e "$BACKUP_DIR/$TS" ]]; then
  base="$TS"; n=1
  while [[ -e "$BACKUP_DIR/$TS" ]]; do TS="$base-$n"; n=$((n + 1)); done
fi
SNAP="$BACKUP_DIR/$TS"
mkdir -p "$SNAP/db" "$SNAP/storage" "$SNAP/bootstrap"

# 1) 一致性数据库快照。优先 sqlite3；没有就用 python3（Ubuntu 默认有）。
sqlite_snapshot() {
  local src="$1" dst="$2"
  if command -v sqlite3 >/dev/null 2>&1; then
    sqlite3 "$src" ".backup '$dst'"
    return 0
  fi
  if command -v python3 >/dev/null 2>&1; then
    python3 - "$src" "$dst" <<'PY'
import sqlite3, sys
src, dst = sys.argv[1], sys.argv[2]
s = sqlite3.connect(f"file:{src}?mode=ro", uri=True)
d = sqlite3.connect(dst)
s.backup(d)
d.close(); s.close()
PY
    return 0
  fi
  die "need sqlite3 or python3 for a consistent database snapshot"
}

log "snapshot $TS -> $SNAP"
log "database copy ($DB_PATH)"
sqlite_snapshot "$DB_PATH" "$SNAP/db/rapig.db"
chmod 600 "$SNAP/db/rapig.db"

# 2) 对象存储：硬链增量，只复制变化的内容。
PREV="$(find "$BACKUP_DIR" -mindepth 1 -maxdepth 1 -type d -printf '%f\n' | sort | grep -v "^$TS$" | tail -1 || true)"
if command -v rsync >/dev/null 2>&1; then
  if [[ -n "$PREV" && -d "$BACKUP_DIR/$PREV/storage" ]]; then
    log "storage rsync (link-dest $PREV)"
    rsync -a --delete --numeric-ids --link-dest="$BACKUP_DIR/$PREV/storage" \
      "$DATA_DIR/storage/" "$SNAP/storage/"
  else
    log "storage rsync (first snapshot)"
    rsync -a --numeric-ids "$DATA_DIR/storage/" "$SNAP/storage/"
  fi
else
  die "rsync is required for storage snapshots (apt-get install -y rsync)"
fi

# 3) bootstrap 目录含初始口令。
if [[ -d "$DATA_DIR/bootstrap" ]]; then
  cp -a "$DATA_DIR/bootstrap/." "$SNAP/bootstrap/"
  chmod -R go-rwx "$SNAP/bootstrap" || true
fi

# 4) manifest（JSON 手写，避免额外依赖）。
DB_SHA="$(sha256sum "$SNAP/db/rapig.db" | cut -d' ' -f1)"
DB_SIZE="$(stat -c %s "$SNAP/db/rapig.db")"
BLOBS="$(find "$SNAP/storage" -type f 2>/dev/null | wc -l | tr -d ' ')"
STORAGE_SIZE="$(du -sb "$SNAP/storage" 2>/dev/null | cut -f1)"
VERSION="$("$BIN" version 2>/dev/null | head -1 || echo unknown)"
cat >"$SNAP/manifest.json" <<EOF
{
  "created_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "snapshot": "$TS",
  "db_sha256": "$DB_SHA",
  "db_bytes": $DB_SIZE,
  "blob_files": $BLOBS,
  "storage_bytes_apparent": ${STORAGE_SIZE:-0},
  "version": "$VERSION"
}
EOF
log "manifest: db=$DB_SIZE bytes blobs=$BLOBS"

# 5) 保留策略。
prune() {
  local all newest_sunday=0 newest_month=0
  mapfile -t all < <(find "$BACKUP_DIR" -mindepth 1 -maxdepth 1 -type d -printf '%f\n' | sort)
  local n=${#all[@]}
  local -A keep=()
  local i ts dow day
  for ((i = 0; i < n; i++)); do
    ts="${all[$i]}"
    # 最近 7 个永远保留
    if ((i >= n - 7)); then keep["$ts"]=1; continue; fi
    dow="$(date -u -d "${ts:0:8}" +%u 2>/dev/null || echo 0)"
    day="$(date -u -d "${ts:0:8}" +%d 2>/dev/null || echo 00)"
    if [[ "$dow" == "7" && ${newest_sunday:-0} -lt 4 ]]; then keep["$ts"]=1; newest_sunday=$((newest_sunday + 1)); fi
    if [[ "$day" == "01" && ${newest_month:-0} -lt 3 ]]; then keep["$ts"]=1; newest_month=$((newest_month + 1)); fi
  done
  local removed=0
  for ts in "${all[@]}"; do
    if [[ -z "${keep[$ts]:-}" ]]; then
      rm -rf -- "${BACKUP_DIR:?}/$ts"
      removed=$((removed + 1))
    fi
  done
  log "retention: kept ${#keep[@]}, removed $removed"
}
prune

log "OK $SNAP"
