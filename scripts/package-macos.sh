#!/usr/bin/env bash
# Build the macOS .app bundle. Universal (arm64 + amd64) when both
# binaries are available; otherwise a single-arch bundle is produced
# from the host arch.
#
# Usage:
#   scripts/package-macos.sh                # current host arch
#   ARCH=amd64,arm64 scripts/package-macos.sh
#   APPLE_IDENTITY="Developer ID Application: …" ./package-macos.sh
#   With APPLE_NOTARY_PROFILE set, the bundle is notarised after sign.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

VERSION="${VERSION:-0.0.0}"
BUILD="${BUILD:-$(date -u +%Y%m%d%H%M)}"
ARCH="${ARCH:-$(uname -m | sed 's/x86_64/amd64/')}"

# Where Fyne-style bundle goes.
OUT_DIR="dist/macos"
BUNDLE="$OUT_DIR/RST.app"
BIN_DIR="$BUNDLE/Contents/MacOS"
RES_DIR="$BUNDLE/Contents/Resources"

rm -rf "$BUNDLE"
mkdir -p "$BIN_DIR" "$RES_DIR"

# 1. Build per-arch binaries.
build_arch() {
    local goarch="$1"
    echo "==> building darwin/$goarch"
    GOARCH="$goarch" CGO_ENABLED=1 GOEXPERIMENT=simd \
        go build -trimpath -ldflags="-s -w -X main.version=$VERSION" \
            -o "dist/macos/translator-$goarch" ./cmd/translator
}

IFS=',' read -ra ARCHES <<<"$ARCH"
for a in "${ARCHES[@]}"; do build_arch "$a"; done

# 2. Glue into universal binary (lipo) or copy single-arch.
if [ "${#ARCHES[@]}" -gt 1 ]; then
    echo "==> lipo $ARCH -> universal"
    lipo -create \
        $(printf "dist/macos/translator-%s\n" "${ARCHES[@]}") \
        -output "$BIN_DIR/translator"
else
    cp "dist/macos/translator-${ARCHES[0]}" "$BIN_DIR/translator"
fi
chmod +x "$BIN_DIR/translator"

# 3. Info.plist.
sed -e "s/__VERSION__/$VERSION/g" -e "s/__BUILD__/$BUILD/g" \
    build/macos/Info.plist > "$BUNDLE/Contents/Info.plist"

# 4. Optional icon. fyne can generate .icns from a PNG; we just copy if
#    the .icns is committed alongside Info.plist.
if [ -f build/macos/AppIcon.icns ]; then
    cp build/macos/AppIcon.icns "$RES_DIR/AppIcon.icns"
fi

# 5. Code-sign + notarize (skipped if the identity / notary profile are
#    not provided — the unsigned .app still runs locally with a
#    `xattr -dr com.apple.quarantine RST.app` workaround).
if [ -n "${APPLE_IDENTITY:-}" ]; then
    echo "==> codesign with $APPLE_IDENTITY"
    codesign --force --deep --options runtime --timestamp \
        --entitlements build/macos/entitlements.plist \
        --sign "$APPLE_IDENTITY" \
        "$BUNDLE"

    if [ -n "${APPLE_NOTARY_PROFILE:-}" ]; then
        echo "==> notarising"
        ZIP="$OUT_DIR/RST.zip"
        ditto -c -k --sequesterRsrc --keepParent "$BUNDLE" "$ZIP"
        xcrun notarytool submit "$ZIP" \
            --keychain-profile "$APPLE_NOTARY_PROFILE" --wait
        xcrun stapler staple "$BUNDLE"
        rm -f "$ZIP"
    fi
fi

# 6. Pack the bundle into a DMG via hdiutil (vanilla, no fancy layout).
DMG="$OUT_DIR/RST-$VERSION-${ARCH//,/-}.dmg"
echo "==> hdiutil create $DMG"
hdiutil create -volname "RST $VERSION" \
    -srcfolder "$BUNDLE" \
    -ov -format UDZO \
    "$DMG" >/dev/null

echo "Done: $BUNDLE"
echo "      $DMG"
