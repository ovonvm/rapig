#!/usr/bin/env bash
# Rapig 备份恢复 / 演练（Linux）。
#
# 用法：
#   scripts/restore.sh --list                     列出快照
#   scripts/restore.sh --snapshot 20261027-0230 --drill
#       解到临时目录并校验：db integrity_check + 抽样 blob sha256 + 引用完整性
#   scripts/restore.sh --snapshot ... --target DIR
#       解到指定目录（不覆盖现有数据）
#   scripts/restore.sh --snapshot ... --apply --yes
#       停止服务后覆盖到真实数据目录（危险；需先自行停服务，脚本只做文件替换）
#
# 环境变量：RAPIG_BIN / RAPIG_DATA_DIR / RAPIG_BACKUP_DIR（同 backup.sh）。
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HOME_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

BIN="${RAPIG_BIN:-$HOME_DIR/rapig}"
DATA_DIR="${RAPIG_DATA_DIR:-$(dirname "$BIN")/data}"
BACKUP_DIR="${RAPIG_BACKUP_DIR:-$(dirname "$BIN")/backup}"

MODE=""
SNAP=""
TARGET=""
APPLY=0
YES=0
VERIFY_FULL=0
SAMPLE=8

log() { printf '[restore] %s\n' "$*"; }
die() { printf '[restore] ERROR: %s\n' "$*" >&2; exit 1; }
usage() { sed -n '2,17p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --list) MODE="list"; shift ;;
    --drill) MODE="drill"; shift ;;
    --snapshot) SNAP="${2:-}"; shift 2 ;;
    --target) TARGET="${2:-}"; shift 2 ;;
    --apply) APPLY=1; shift ;;
    --yes) YES=1; shift ;;
    --verify-full) VERIFY_FULL=1; shift ;;
    -h|--help) usage ;;
    *) die "unknown argument: $1 (try --help)" ;;
  esac
done

[[ -d "$BACKUP_DIR" ]] || die "backup dir not found: $BACKUP_DIR"
mapfile -t SNAPS < <(find "$BACKUP_DIR" -mindepth 1 -maxdepth 1 -type d -printf '%f\n' | sort)

if [[ "$MODE" == "list" ]]; then
  for s in "${SNAPS[@]}"; do
    m="$BACKUP_DIR/$s/manifest.json"
    if [[ -f "$m" ]]; then
      printf '%s  %s\n' "$s" "$(tr -d '\n' <"$m" | tr -s ' ')"
    else
      printf '%s  (no manifest)\n' "$s"
    fi
  done
  exit 0
fi

[[ -n "$SNAP" ]] || die "--snapshot is required (see --list)"
SNAP_DIR="$BACKUP_DIR/$SNAP"
[[ -d "$SNAP_DIR" ]] || die "snapshot not found: $SNAP_DIR"
[[ -f "$SNAP_DIR/db/rapig.db" ]] || die "snapshot has no db/rapig.db: $SNAP_DIR"

if [[ $APPLY -eq 1 ]]; then
  [[ $YES -eq 1 ]] || die "--apply needs --yes and a stopped service"
  TARGET="$DATA_DIR"
fi
if [[ -z "$TARGET" ]]; then
  TARGET="$(dirname "$BIN")/restore/$SNAP"
fi
[[ -e "$TARGET" ]] && die "target already exists: $TARGET"

log "restore snapshot $SNAP -> $TARGET"
mkdir -p "$TARGET/db" "$TARGET/storage"
cp -a "$SNAP_DIR/db/rapig.db" "$TARGET/db/rapig.db"
chmod 600 "$TARGET/db/rapig.db"
if [[ -d "$SNAP_DIR/storage" ]]; then
  cp -a "$SNAP_DIR/storage/." "$TARGET/storage/"
fi
if [[ -d "$SNAP_DIR/bootstrap" ]]; then
  cp -a "$SNAP_DIR/bootstrap/." "$TARGET/bootstrap/" 2>/dev/null || true
fi

# 校验：完整性 + blob 引用 + sha256。
VERIFY_PY='
import hashlib, os, random, sqlite3, sys
db, storage, full = sys.argv[1], sys.argv[2], sys.argv[3] == "1"
con = sqlite3.connect(f"file:{db}?mode=ro", uri=True)
ok = con.execute("PRAGMA integrity_check").fetchone()[0]
if ok != "ok":
    print("integrity_check:", ok); sys.exit(2)
db_sha = hashlib.sha256(open(db, "rb").read()).hexdigest()
rows = con.execute("SELECT id, sha256, size FROM blobs").fetchall()
missing = 0
checked = 0
sample = rows if full else random.sample(rows, min(len(rows), 8))
for _id, sha, size in sample:
    p = os.path.join(storage, "objects", sha[:2], sha[2:4], sha)
    checked += 1
    if not os.path.isfile(p):
        print("missing blob:", sha); missing += 1; continue
    if os.path.getsize(p) != size:
        print("size mismatch:", sha, os.path.getsize(p), size); missing += 1; continue
    h = hashlib.sha256()
    with open(p, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    if h.hexdigest() != sha:
        print("sha mismatch:", sha); missing += 1
print(f"db_sha256={db_sha}")
print(f"blobs_total={len(rows)} sampled={checked} missing={missing}")
sys.exit(0 if missing == 0 else 3)
'
if command -v python3 >/dev/null 2>&1; then
  VERIFY_FULL_FLAG=0; [[ $VERIFY_FULL -eq 1 ]] && VERIFY_FULL_FLAG=1
  set +e
  python3 -c "$VERIFY_PY" "$TARGET/db/rapig.db" "$TARGET/storage" "$VERIFY_FULL_FLAG"
  rc=$?
  set -e
  if [[ $rc -ne 0 ]]; then
    die "verification failed for $TARGET (rc=$rc)"
  fi
else
  die "python3 is required for verification"
fi

log "RESTORE OK $TARGET"
if [[ $APPLY -eq 1 ]]; then
  log "restored into live data dir; start the service and check /health + login"
fi
