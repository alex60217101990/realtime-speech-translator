---
title: Packaging (M6)
date: 2026-05-15
tags:
  - architecture
  - packaging
  - milestone-m6
---

# Packaging (M6)

M6 closes the development loop with installable artifacts. The build matrix produces, per release tag, four binaries:

| OS | Artifact | Tooling |
|---|---|---|
| macOS arm64+amd64 | `RST-<v>-amd64-arm64.dmg` (universal `.app`) | `lipo`, `codesign`, `notarytool`, `hdiutil` |
| Linux amd64 | `RST-<v>-amd64.AppImage` + `realtime-speech-translator_<v>_amd64.deb` | `linuxdeploy`, `nfpm` |
| Windows amd64 | `RST-Setup-<v>.exe` | NSIS, optional `signtool` |

The macOS bundle is universal — `scripts/package-macos.sh` builds `amd64` and `arm64` binaries separately and `lipo`s them. Linux and Windows ship native-arch only for v1.0; arm64 Linux is a CI matrix addition we can do later without touching the scripts.

## Build layout

```
build/
├── macos/
│   ├── Info.plist          (templated; __VERSION__ + __BUILD__ substituted)
│   ├── entitlements.plist  (mic input, JIT for ggml, network client for downloader)
│   └── AppIcon.icns        (optional; user-supplied)
├── linux/
│   ├── rst.desktop         (AppImage + .deb)
│   ├── nfpm.yaml           (envsubst template — ${VERSION}, ${ARCH})
│   └── icons/rst.png       (placeholder if missing)
└── windows/
    ├── installer.nsi
    └── icon.ico            (optional)
```

```
scripts/
├── build-deps.sh           (whisper.cpp + sentencepiece + ctranslate2)
├── package-macos.sh
├── package-linux.sh
└── package-windows.sh
```

```
dist/                       (gitignored, populated by package scripts)
├── macos/  RST.app + .dmg
├── linux/  *.AppImage + *.deb
└── windows/ *.exe
```

## CI workflows

Two GitHub Actions workflows:

| File | When | Output |
|---|---|---|
| [[../../../.github/workflows/build.yml]] | every push to main, every PR | vet + race tests + `make build` per OS |
| [[../../../.github/workflows/release.yml]] | tag `v*` (push), manual dispatch | full package matrix → uploaded artifacts → draft GitHub Release |

Release matrix runs the same `scripts/package-*.sh` we run locally — so the local build IS the CI build, just with secrets injected.

## Signing & notarisation

Optional and gated on secrets:

| Secret | Used by | Effect |
|---|---|---|
| `APPLE_IDENTITY` | macOS | `codesign --options runtime` + entitlements |
| `APPLE_NOTARY_PROFILE` | macOS | `xcrun notarytool submit --keychain-profile` + `stapler staple` |
| `SIGNTOOL_THUMBPRINT` | Windows | `signtool sign /sha1 …` for both `.exe` and the NSIS installer |

If a secret is not set, the build still succeeds — produces an unsigned artifact. macOS users can clear quarantine with `xattr -dr com.apple.quarantine RST.app` for self-builds; Windows shows the SmartScreen warning but the installer still works.

## Distribution channels

| Channel | Status |
|---|---|
| GitHub Releases (canonical) | wired (release.yml uploads to draft) |
| Homebrew tap | TODO M7 — `brew install --cask alex/rst` |
| AUR | TODO post-v1.0 — `realtime-speech-translator-bin` |
| Flatpak / Snap | TODO post-v1.0 |
| Microsoft Store / WinGet | TODO post-v1.0 — winget manifest PR |
| Mac App Store | **never** — sandbox forbids virtual mic access (see [[../decisions/ADR-001 Desktop Only]]) |

## What is intentionally not in M6

- Real production-quality icons. Placeholders only; design in M7.
- ARM64 Linux + Windows builds. Easy to add later; needs runners.
- Auto-update mechanism (Sparkle / squirrel / similar). Out of scope for v1.0.
- LICENSE + NOTICE files in the installer (will land in M7 with the final license decision).

## Open questions

- macOS hardened-runtime entitlements may need `com.apple.security.cs.allow-dyld-environment-variables` removed when we ship a vendored Piper binary in M8 — current entitlement set is conservative.
- The macOS `dmg` is plain UDZO; a styled DMG with arrow and Applications-folder shortcut would be nicer. `create-dmg` (Homebrew) handles it in ~50 lines if we ever want it.

## See also

- [[../../wiki/06-Build-and-Packaging]]
- [[../../wiki/10-Licensing-Distribution]]
- [[../../../scripts/package-macos.sh]]
- [[../../../scripts/package-linux.sh]]
- [[../../../scripts/package-windows.sh]]
- [[../../../.github/workflows/release.yml]]
