---
title: SentencePiece does not bundle abseil into libsentencepiece.a
date: 2026-05-15
tags:
  - gotcha
  - build
  - cgo
  - sentencepiece
---

# SentencePiece does not bundle abseil into `libsentencepiece.a`

## Symptom

```
Undefined symbols for architecture x86_64:
  "absl::lts_20260107::strings_internal::AppendPack(...)", referenced from:
      sentencepiece::SentencePieceProcessor::ParseExtraOptions(...) in libsentencepiece.a[38](sentencepiece_processor.cc.o)
```

## Cause

`SPM_USE_BUILTIN_PROTOBUF=ON` bundles protobuf into the SP archive but **not** abseil. Abseil is pulled in via CMake `FetchContent_Declare`/`MakeAvailable` as a sibling project. Its targets compile as separate static libs (`libabsl_strings.a`, `libabsl_str_format_internal.a`, …) under `build_go/third_party/abseil-cpp/absl/<sub>/`. They are **not** merged into `libsentencepiece.a`. There are ~90 such targets — listing each in `cgo LDFLAGS` is unmaintainable.

## Fix

After SP builds, merge every `libabsl_*.a` into one combined archive `libabsl_combined.a` placed next to `libsentencepiece.a`:

```bash
# build-deps.sh excerpt
"$CMAKE" --build "$build" --target sentencepiece-static -j
"$CMAKE" --build "$build" -j || true              # build every absl_* target

absl_libs=$(find "$build/third_party/abseil-cpp" -name "libabsl_*.a")
case $(uname -s) in
  Darwin) libtool -static -o "$build/src/libabsl_combined.a" $absl_libs ;;
  *)      # MRI script for GNU ar
          {
            echo "create $build/src/libabsl_combined.a"
            for f in $absl_libs; do echo "addlib $f"; done
            echo save
            echo end
          } | ar -M ;;
esac
```

cgo LDFLAGS:

```go
// #cgo LDFLAGS: -lsentencepiece -labsl_combined -lstdc++
```

Resulting archive is ~2 MB.

## Why this works

Both `libtool -static` (macOS) and `ar -M` (GNU binutils) concatenate object members from input archives into one output archive without re-running the linker. The combined archive then satisfies any abseil symbol the SP `.a` references.

## Prevention

If we upgrade SentencePiece and it suddenly bundles abseil itself, the combined lib becomes redundant but not harmful. Keep the merge step until SP releases note otherwise.

## Alternative considered

Use the system abseil (`brew install abseil`, `apt install libabsl-dev`). Rejected because:

- Distribution would need per-OS install instructions.
- ABI drift between system abseil and the bundled version SP was compiled against.

## Date discovered

2026-05-15 (M3a first MT build).
