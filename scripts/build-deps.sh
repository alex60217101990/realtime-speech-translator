#!/usr/bin/env bash
# Builds the native third-party libraries required by the
# realtime-speech-translator. Output is placed under
# third_party/<lib>/build_go/ which matches what the upstream Go bindings
# expect in their LIBRARY_PATH/C_INCLUDE_PATH variables.
#
# Usage:
#   scripts/build-deps.sh           # build everything
#   scripts/build-deps.sh whisper   # build only whisper.cpp
#
# Environment overrides:
#   JOBS        parallel build jobs (default: number of CPUs)
#   CMAKE       cmake binary (default: cmake)
#   GGML_METAL  ON|OFF, default ON on Darwin
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CMAKE="${CMAKE:-cmake}"
JOBS="${JOBS:-$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 4)}"

if ! command -v "$CMAKE" >/dev/null 2>&1; then
  echo "build-deps: cmake not found in PATH" >&2
  exit 1
fi

case "$(uname -s)" in
  Darwin)  : "${GGML_METAL:=ON}"  ;;
  *)       : "${GGML_METAL:=OFF}" ;;
esac

build_whisper() {
  local src="$REPO_ROOT/third_party/whisper.cpp"
  local build="$src/build_go"
  if [ ! -d "$src" ]; then
    echo "build-deps: $src missing — run 'git submodule update --init --recursive'" >&2
    exit 1
  fi

  "$CMAKE" -S "$src" -B "$build" \
    -DCMAKE_BUILD_TYPE=Release \
    -DBUILD_SHARED_LIBS=OFF \
    -DWHISPER_BUILD_EXAMPLES=OFF \
    -DWHISPER_BUILD_TESTS=OFF \
    -DWHISPER_BUILD_SERVER=OFF \
    -DGGML_METAL="$GGML_METAL"

  "$CMAKE" --build "$build" --target whisper --parallel "$JOBS"
}

case "${1:-all}" in
  all|whisper) build_whisper ;;
  *)
    echo "build-deps: unknown target '$1'" >&2
    exit 2
    ;;
esac
