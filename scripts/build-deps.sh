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

build_sentencepiece() {
  local src="$REPO_ROOT/third_party/sentencepiece"
  local build="$src/build_go"
  if [ ! -d "$src" ]; then
    echo "build-deps: $src missing" >&2
    exit 1
  fi
  "$CMAKE" -S "$src" -B "$build" \
    -DCMAKE_BUILD_TYPE=Release \
    -DSPM_ENABLE_SHARED=OFF \
    -DSPM_USE_BUILTIN_PROTOBUF=ON \
    -DSPM_ENABLE_TCMALLOC=OFF \
    -DSPM_BUILD_TEST=OFF \
    -DCMAKE_POSITION_INDEPENDENT_CODE=ON
  # Build sentencepiece-static plus all abseil-cpp dependency targets;
  # abseil is fetched via FetchContent and is not bundled into
  # libsentencepiece.a, so cgo linking requires its archives separately.
  "$CMAKE" --build "$build" --target sentencepiece-static --parallel "$JOBS"
  # Build everything else so all the absl_* targets become real .a files.
  "$CMAKE" --build "$build" --parallel "$JOBS" || true
  # Merge every libabsl_*.a into one libabsl_combined.a so the cgo
  # LDFLAGS need only `-labsl_combined`. libtool -static on macOS, ar
  # MRI script on Linux/Windows.
  local absl_dir="$build/third_party/abseil-cpp"
  local out_dir="$build/src"
  local out_lib="$out_dir/libabsl_combined.a"
  local absl_libs
  absl_libs=$(find "$absl_dir" -name "libabsl_*.a" -print)
  if [ -n "$absl_libs" ]; then
    case "$(uname -s)" in
      Darwin)
        # shellcheck disable=SC2086
        libtool -static -o "$out_lib" $absl_libs 2>/dev/null
        ;;
      *)
        rm -f "$out_lib"
        local mri="$build/absl_combined.mri"
        {
          echo "create $out_lib"
          for f in $absl_libs; do echo "addlib $f"; done
          echo "save"
          echo "end"
        } > "$mri"
        ar -M < "$mri"
        ;;
    esac
    echo "build-deps: produced $out_lib"
  fi
}

build_ctranslate2() {
  local src="$REPO_ROOT/third_party/ctranslate2"
  local build="$src/build_go"
  if [ ! -d "$src" ]; then
    echo "build-deps: $src missing" >&2
    exit 1
  fi
  # OpenBLAS on Linux, Accelerate on macOS, Ruy on Windows. Disable
  # CUDA, MKL, oneDNN — we want a single static lib usable on any CPU.
  case "$(uname -s)" in
    Darwin) local blas_flag="-DWITH_ACCELERATE=ON -DWITH_OPENBLAS=OFF" ;;
    Linux)  local blas_flag="-DWITH_OPENBLAS=ON -DWITH_ACCELERATE=OFF" ;;
    *)      local blas_flag="-DWITH_RUY=ON" ;;
  esac
  "$CMAKE" -S "$src" -B "$build" \
    -DCMAKE_BUILD_TYPE=Release \
    -DBUILD_SHARED_LIBS=OFF \
    -DWITH_MKL=OFF \
    -DWITH_DNNL=OFF \
    -DWITH_CUDA=OFF \
    -DWITH_TENSOR_PARALLEL=OFF \
    -DWITH_TESTS=OFF \
    -DWITH_PYTHON=OFF \
    -DWITH_RUY=ON \
    -DOPENMP_RUNTIME=NONE \
    -DCMAKE_POSITION_INDEPENDENT_CODE=ON \
    -DCMAKE_POLICY_VERSION_MINIMUM=3.5 \
    $blas_flag
  "$CMAKE" --build "$build" --target ctranslate2 --parallel "$JOBS"
}

case "${1:-all}" in
  all)            build_whisper && build_sentencepiece && build_ctranslate2 ;;
  whisper)        build_whisper ;;
  sentencepiece)  build_sentencepiece ;;
  ctranslate2)    build_ctranslate2 ;;
  *)
    echo "build-deps: unknown target '$1'" >&2
    exit 2
    ;;
esac
