#!/usr/bin/env bash
# Full local gate: formatting, vet, tests, cross platform builds, frontend
# typecheck and build. Run this before every commit; CI will run the same steps.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

GO_BIN="${GO_BIN:-$(command -v go || true)}"
if [ -z "$GO_BIN" ]; then
  GO_BIN="/d/dshwork/.tools/go/bin/go"
fi
NODE_BIN="${NODE_BIN:-$(command -v node || true)}"
if [ -z "$NODE_BIN" ]; then
  NODE_BIN="C:/Users/hm/.dsh/dsh-runtimes/dsh-primary-runtime/dependencies/node/bin/node.exe"
fi
PNPM_JS="${PNPM_JS:-$(cd "$(dirname "$NODE_BIN")/../.." && pwd)/pnpm/bin/pnpm.mjs}"
NODE_DIR="$(dirname "$NODE_BIN")"
if command -v cygpath >/dev/null 2>&1; then
  NODE_DIR="$(cygpath -u "$NODE_DIR")"
fi
# pnpm's run-script shims resolve `node` from PATH; keep ours first.
export PATH="$NODE_DIR:$PATH"
export GOTOOLCHAIN="${GOTOOLCHAIN:-local}"

fail() { echo "CHECK FAILED: $*" >&2; exit 1; }

echo "== gofmt =="
GOFMT="${GOFMT:-$(dirname "$GO_BIN")/gofmt}"
unformatted="$("$GOFMT" -l . || true)"
if [ -n "$unformatted" ]; then
  echo "$unformatted"
  fail "gofmt reports unformatted files (run: gofmt -w .)"
fi

echo "== go vet =="
"$GO_BIN" vet ./...

echo "== go test =="
"$GO_BIN" test ./... -count=1 -p 1

echo "== go test -race =="
race_log="$ROOT/data/.check-race.log"
if CGO_ENABLED=1 "$GO_BIN" test -race ./... -count=1 -p 1 >"$race_log" 2>&1; then
  echo "  race detector clean"
elif grep -qiE 'C compiler|requires cgo|cgo: C' "$race_log"; then
  # Windows hosts usually lack a C toolchain; the Linux acceptance host runs this.
  echo "  skipped: no C toolchain on this host (race runs on the Linux acceptance host)"
else
  cat "$race_log"
  rm -f "$race_log"
  fail "go test -race failed"
fi
rm -f "$race_log"

echo "== cross platform build =="
out="$ROOT/data/.check"
mkdir -p "$out"
for target in windows/amd64 linux/amd64 darwin/arm64; do
  os="${target%/*}"
  arch="${target#*/}"
  bin="$out/rapig-$os-$arch"
  [ "$os" = "windows" ] && bin="$bin.exe"
  GOOS="$os" GOARCH="$arch" "$GO_BIN" build -trimpath -o "$bin" ./cmd/rapig
  echo "  built $os/$arch"
done
rm -rf "$out"

echo "== frontend =="
if [ -f web/package.json ]; then
  if [ ! -d web/node_modules ]; then
    fail "web/node_modules is missing (run: node pnpm.mjs --dir web install)"
  fi
  "$NODE_BIN" "$PNPM_JS" --dir web run typecheck
  "$NODE_BIN" "$PNPM_JS" --dir web run build
else
  fail "web/package.json is missing"
fi

echo "CHECK OK"
