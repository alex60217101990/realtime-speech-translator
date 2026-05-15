#!/usr/bin/env bash
# Build a Windows .exe + NSIS installer.
# Usage:
#   VERSION=0.1.0 ./scripts/package-windows.sh
#   SIGNTOOL_THUMBPRINT=... ./scripts/package-windows.sh   # if cert configured
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

VERSION="${VERSION:-0.0.0}"
ARCH="${ARCH:-amd64}"
OUT_DIR="dist/windows"
mkdir -p "$OUT_DIR" "build/windows"

echo "==> building windows/$ARCH"
GOOS=windows GOARCH="$ARCH" CGO_ENABLED=1 GOEXPERIMENT=simd \
    go build -trimpath -ldflags="-s -w -H windowsgui -X main.version=$VERSION" \
        -o bin/translator.exe ./cmd/translator

# Sign the exe if a thumbprint was provided (CI step).
if [ -n "${SIGNTOOL_THUMBPRINT:-}" ] && command -v signtool >/dev/null 2>&1; then
    echo "==> signing exe"
    signtool sign /fd SHA256 /sha1 "$SIGNTOOL_THUMBPRINT" \
        /tr http://timestamp.digicert.com /td SHA256 \
        bin/translator.exe
fi

if command -v makensis >/dev/null 2>&1; then
    echo "==> NSIS installer"
    makensis -DVERSION="$VERSION" build/windows/installer.nsi
    mv "build/windows/RST-Setup-$VERSION.exe" "$OUT_DIR/"
    if [ -n "${SIGNTOOL_THUMBPRINT:-}" ] && command -v signtool >/dev/null 2>&1; then
        signtool sign /fd SHA256 /sha1 "$SIGNTOOL_THUMBPRINT" \
            /tr http://timestamp.digicert.com /td SHA256 \
            "$OUT_DIR/RST-Setup-$VERSION.exe"
    fi
else
    echo "warning: makensis not found — skipping installer. Install NSIS from https://nsis.sourceforge.io/"
fi

echo "Done: $(ls -1 $OUT_DIR 2>/dev/null || true)"
