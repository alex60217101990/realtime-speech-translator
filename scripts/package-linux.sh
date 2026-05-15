#!/usr/bin/env bash
# Build a Linux AppImage and a .deb. Both wrap the same binary and
# .desktop entry; nfpm + linuxdeploy do the heavy lifting and are
# expected to be present on the build host (CI installs them).
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

VERSION="${VERSION:-0.0.0}"
ARCH="${ARCH:-amd64}"
OUT_DIR="dist/linux"

mkdir -p "$OUT_DIR" build/linux/icons

# 1. Build the binary.
echo "==> building linux/$ARCH"
GOARCH="$ARCH" CGO_ENABLED=1 GOEXPERIMENT=simd \
    go build -trimpath -ldflags="-s -w -X main.version=$VERSION" \
        -o bin/translator ./cmd/translator

# 2. Placeholder icon when no real one is committed. Real icon should
#    live at build/linux/icons/rst.png (256x256 PNG).
if [ ! -f build/linux/icons/rst.png ]; then
    printf '\x89PNG\r\n\x1a\n' > build/linux/icons/rst.png  # 8-byte stub
    echo "warning: build/linux/icons/rst.png missing — using stub"
fi

# 3. AppImage via linuxdeploy.
if command -v linuxdeploy >/dev/null 2>&1; then
    echo "==> AppImage"
    APPDIR="dist/linux/AppDir"
    rm -rf "$APPDIR"
    mkdir -p "$APPDIR/usr/bin" "$APPDIR/usr/share/applications" "$APPDIR/usr/share/icons/hicolor/256x256/apps"
    cp bin/translator "$APPDIR/usr/bin/translator"
    cp build/linux/rst.desktop "$APPDIR/usr/share/applications/rst.desktop"
    cp build/linux/icons/rst.png "$APPDIR/usr/share/icons/hicolor/256x256/apps/rst.png"
    cp build/linux/icons/rst.png "$APPDIR/rst.png"

    OUTPUT_NAME="RST-$VERSION-$ARCH"
    OUTPUT="$OUT_DIR/${OUTPUT_NAME}.AppImage" linuxdeploy \
        --appdir "$APPDIR" \
        --desktop-file "$APPDIR/usr/share/applications/rst.desktop" \
        --icon-file "$APPDIR/rst.png" \
        --output appimage
    mv "${OUTPUT_NAME}".AppImage "$OUT_DIR/" 2>/dev/null || true
else
    echo "warning: linuxdeploy not found — skipping AppImage. Install from https://github.com/linuxdeploy/linuxdeploy/releases"
fi

# 4. .deb via nfpm.
if command -v nfpm >/dev/null 2>&1; then
    echo "==> .deb"
    if ! command -v envsubst >/dev/null 2>&1; then
        echo "warning: envsubst not found (apt: gettext-base) — skipping .deb"
    else
        VERSION="$VERSION" ARCH="$ARCH" envsubst < build/linux/nfpm.yaml > "$OUT_DIR/nfpm.yaml"
        nfpm pkg --packager deb --config "$OUT_DIR/nfpm.yaml" --target "$OUT_DIR/"
    fi
else
    echo "warning: nfpm not found — skipping .deb. Install from https://github.com/goreleaser/nfpm/releases"
fi

echo "Done: $(ls -1 $OUT_DIR/*.AppImage $OUT_DIR/*.deb 2>/dev/null || true)"
