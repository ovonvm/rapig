#!/usr/bin/env bash
# 构建 Windows 单文件控制面板（.NET 8 WPF 自包含 + 内嵌 Go 核，需管理员运行）。
# 产物：dist/Rapig.exe（单文件，可直接发布；不再打包 zip）
# 前置：先运行 scripts/build.sh 生成 dist/rapig.exe（Go 核，含 embed 前端）。
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo dev)
DIRTY=""
if [[ "$VERSION" == *-dirty ]]; then DIRTY="+dirty"; VERSION="${VERSION%-dirty}"; fi
DIST=dist
GUI_DIR=gui/Rapig.Gui
OUT="$DIST/gui-publish"

DOTNET=""
if command -v dotnet >/dev/null 2>&1; then
  DOTNET=dotnet
elif [ -x "/c/Program Files/dotnet/dotnet.exe" ]; then
  DOTNET="/c/Program Files/dotnet/dotnet.exe"
else
  echo "ERROR: 未找到 dotnet SDK（需要 .NET 8 SDK 构建 GUI）" >&2
  exit 1
fi

if [ ! -f "$DIST/core/rapig.exe" ]; then
  echo "ERROR: 缺少 dist/core/rapig.exe，请先运行 scripts/build.sh" >&2
  exit 1
fi

rm -rf "$OUT"
mkdir -p "$GUI_DIR/payload"

echo "> staging core payload"
cp "$DIST/core/rapig.exe" "$GUI_DIR/payload/rapig-core.exe"

echo "> publish single-file GUI ($VERSION$DIRTY)"
"$DOTNET" publish "$GUI_DIR" -c Release -r win-x64 --self-contained true \
  -p:PublishSingleFile=true \
  -p:IncludeNativeLibrariesForSelfExtract=true \
  -p:EnableCompressionInSingleFile=true \
  -p:DebugType=none \
  -p:Version=0.12.0 \
  -p:InformationalVersion="$VERSION$DIRTY" \
  -o "$OUT" >/dev/null

if [ ! -f "$OUT/Rapig.exe" ]; then
  echo "ERROR: publish 未生成 Rapig.exe" >&2
  exit 1
fi
DLL_COUNT=$(find "$OUT" -maxdepth 1 -name '*.dll' | wc -l)
if [ "$DLL_COUNT" -gt 0 ]; then
  echo "ERROR: 单文件发布目录仍包含 $DLL_COUNT 个 dll，请检查 csproj/发布参数" >&2
  exit 1
fi

cp "$OUT/Rapig.exe" "$DIST/Rapig.exe"
rm -rf "$OUT" "$GUI_DIR/payload"
echo "  exe: $DIST/Rapig.exe ($(du -h "$DIST/Rapig.exe" | cut -f1))"

# 单文件 exe 是唯一发布物；如需 zip 便于分发，设 RAPIG_GUI_ZIP=1。
ZIP="$DIST/Rapig-${VERSION}${DIRTY}-win-x64.zip"
if [ "${RAPIG_GUI_ZIP:-0}" != "1" ]; then
  rm -f "$ZIP"
  exit 0
fi
rm -f "$ZIP"
if command -v powershell >/dev/null 2>&1; then
  powershell -NoProfile -Command "Compress-Archive -Path '$DIST/Rapig.exe' -DestinationPath '$ZIP' -Force" >/dev/null 2>&1 || true
fi
if [ -f "$ZIP" ]; then
  echo "  zip: $ZIP ($(du -h "$ZIP" | cut -f1))"
fi
