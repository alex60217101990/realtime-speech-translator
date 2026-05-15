# 06 — Сборка и упаковка

## Общая стратегия

```
third_party/ → CMake build → static libs (*.a) + headers
       ↓
Go cgo (CGO_CFLAGS, CGO_LDFLAGS) → static linking
       ↓
Fyne package → .app / AppImage / .exe
       ↓
Codesign / Notarize / Signtool
       ↓
Release artifact (GitHub release)
```

## Системные toolchain-требования

| OS | Tools |
|---|---|
| macOS | Xcode CLT (`xcode-select --install`), CMake 3.20+, ninja |
| Linux | gcc 11+, CMake, ninja, libsamplerate-dev, libpulse-dev, libasound2-dev |
| Windows | MSYS2 + mingw-w64 (или MSVC + clang-cl), CMake, ninja |

## Сборка native dependencies (`scripts/build-deps.sh`)

```bash
#!/usr/bin/env bash
set -euo pipefail

OS=$(uname -s)
ARCH=$(uname -m)
OUT=third_party/build/${OS}-${ARCH}
mkdir -p "$OUT"

# 1. whisper.cpp
cmake -S third_party/whisper.cpp -B "$OUT/whisper" \
  -DBUILD_SHARED_LIBS=OFF \
  -DWHISPER_BUILD_EXAMPLES=OFF \
  -DWHISPER_BUILD_TESTS=OFF \
  -DGGML_METAL=$([[ $OS == Darwin ]] && echo ON || echo OFF) \
  -DGGML_CUDA=OFF \
  -G Ninja
cmake --build "$OUT/whisper" -j

# 2. CTranslate2
cmake -S third_party/ctranslate2 -B "$OUT/ct2" \
  -DBUILD_SHARED_LIBS=OFF \
  -DWITH_MKL=OFF \
  -DOPENMP_RUNTIME=NONE \
  -DWITH_RUY=ON \
  -G Ninja
cmake --build "$OUT/ct2" -j

# 3. Piper (или onnxruntime + sentencepiece)
cmake -S third_party/piper -B "$OUT/piper" \
  -DBUILD_SHARED_LIBS=OFF \
  -G Ninja
cmake --build "$OUT/piper" -j
```

## Сборка Go binary (`Makefile`)

```makefile
PROJECT := translator
BIN := bin/$(PROJECT)
OS := $(shell uname -s | tr '[:upper:]' '[:lower:]')
ARCH := $(shell uname -m)
DEPS := third_party/build/$(shell uname -s)-$(ARCH)

CGO_CFLAGS := -I$(DEPS)/whisper/include -I$(DEPS)/ct2/include -I$(DEPS)/piper/include
CGO_LDFLAGS := -L$(DEPS)/whisper -L$(DEPS)/ct2 -L$(DEPS)/piper \
  -lwhisper -lggml -lctranslate2 -lpiper -lstdc++ -lm

.PHONY: build deps clean

deps:
	./scripts/build-deps.sh

build: deps
	CGO_CFLAGS="$(CGO_CFLAGS)" CGO_LDFLAGS="$(CGO_LDFLAGS)" \
		go build -trimpath -ldflags="-s -w" -o $(BIN) ./cmd/translator

run: build
	./$(BIN)

clean:
	rm -rf bin third_party/build
```

## Кросс-компиляция (CI)

Не используем `GOOS/GOARCH` через одну машину — cgo это ломает. Вместо этого:

- **GitHub Actions matrix**:
  - `macos-14` → строит macOS arm64
  - `macos-13` → строит macOS amd64 (старый Xeon runner) → потом `lipo` склеивает universal
  - `ubuntu-22.04` → linux amd64
  - `ubuntu-22.04-arm` → linux arm64 (если доступен) или QEMU
  - `windows-2022` → windows amd64

```yaml
# .github/workflows/build.yml (фрагмент)
strategy:
  matrix:
    include:
      - { os: macos-14, target: darwin-arm64 }
      - { os: macos-13, target: darwin-amd64 }
      - { os: ubuntu-22.04, target: linux-amd64 }
      - { os: windows-2022, target: windows-amd64 }
```

## Упаковка

### macOS

```bash
# 1. universal binary
lipo -create -output bin/translator bin/translator-arm64 bin/translator-amd64

# 2. Bundle через fyne CLI
fyne package -os darwin -icon assets/icon.png -name "RST"

# 3. Подпись
codesign --deep --force --options runtime \
  --sign "Developer ID Application: <Team>" RST.app

# 4. Нотаризация
xcrun notarytool submit RST.zip --apple-id ... --team-id ... --wait
xcrun stapler staple RST.app
```

### Linux

- **AppImage**: `linuxdeploy --appdir AppDir --executable bin/translator --desktop-file rst.desktop --icon-file icon.png --output appimage`
- **Deb**: `nfpm pkg --packager deb --config nfpm.yaml`. Зависимости: `pulseaudio | pipewire-pulse`, `libsamplerate0`.

### Windows

- **NSIS** script упаковывает `translator.exe` + DLLs (если есть динамические) + ярлык.
- **Signtool**: `signtool sign /fd SHA256 /a /tr http://timestamp.digicert.com /td SHA256 RST-setup.exe`.

## Размер артефактов (цель)

| OS | Размер | Что внутри |
|---|---|---|
| macOS .app (zipped) | ~45 MB | binary + Info.plist + icons, без моделей |
| Linux AppImage | ~50 MB | + libsamplerate (упакована) |
| Windows installer | ~45 MB | exe + uninstall |

Модели грузятся отдельно при первом запуске (~1.2 GB суммарно для default-набора).

## Reproducible build

- `-trimpath` для удаления абсолютных путей.
- `SOURCE_DATE_EPOCH` для детерминизма.
- `go mod vendor` опционально для git-зеркала.
