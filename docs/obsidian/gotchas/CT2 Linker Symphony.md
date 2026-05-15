---
title: CTranslate2 link requires 30+ static libs in order
date: 2026-05-15
tags:
  - gotcha
  - build
  - cgo
  - ctranslate2
---

# CTranslate2 link requires 30+ static libs in order

## Symptom

A succession of linker errors, each unblocking the next:

```
Undefined symbols ... ruy::Ctx::GetRuntimeEnabledPaths()           → -lruy_ctx
Undefined symbols ... ruy::CpuInfo::Avx2Fma()                      → -lruy_cpuinfo
Undefined symbols ... _cpuinfo_isa                                 → -lcpuinfo
Undefined symbols ... clog_vlog_warning                            → -lclog
Undefined symbols ... GetEnabledX86Features                        → -lcpu_features
```

## Cause

CTranslate2's CMake exports `libctranslate2.a` as the *interface* target — but its declared link dependencies are CMake targets, not flags. When we link CT2 from cgo we must replicate the full dependency tree by hand:

```
ctranslate2
├── cpu_features
└── ruy_frontend
    ├── ruy_context, ruy_context_get_ctx, ruy_ctx
    ├── ruy_trmul, ruy_thread_pool, ruy_blocking_counter, ruy_wait
    ├── ruy_block_map, ruy_allocator, ruy_prepacked_cache
    ├── ruy_cpuinfo
    │   └── cpuinfo (Pytorch fork — separate target inside ruy)
    │       └── clog
    ├── ruy_kernel_{arm,avx,avx2_fma,avx512}
    ├── ruy_pack_{arm,avx,avx2_fma,avx512}
    ├── ruy_apply_multiplier
    ├── ruy_prepare_packed_matrices
    ├── ruy_have_built_path_for_{avx,avx2_fma,avx512}
    ├── ruy_denormal, ruy_tune, ruy_system_aligned_alloc
    └── (transitive) cpu_features for SIMD detection
```

The order matters on GNU ld (which the linker is on Linux). On Apple's ld64 the order is more forgiving but the lib still has to be present.

## Fix

[[../../../internal/mt/ct2/ct2.go]] declares the full list as cgo LDFLAGS:

```go
// #cgo LDFLAGS: -lctranslate2 -lcpu_features
// #cgo LDFLAGS: -lruy_frontend -lruy_context -lruy_context_get_ctx -lruy_ctx
// #cgo LDFLAGS: -lruy_trmul -lruy_thread_pool -lruy_blocking_counter -lruy_wait
// #cgo LDFLAGS: -lruy_block_map -lruy_allocator -lruy_prepacked_cache -lruy_cpuinfo
// #cgo LDFLAGS: -lruy_kernel_arm -lruy_kernel_avx -lruy_kernel_avx2_fma -lruy_kernel_avx512
// #cgo LDFLAGS: -lruy_pack_arm -lruy_pack_avx -lruy_pack_avx2_fma -lruy_pack_avx512
// #cgo LDFLAGS: -lruy_apply_multiplier -lruy_prepare_packed_matrices
// #cgo LDFLAGS: -lruy_have_built_path_for_avx -lruy_have_built_path_for_avx2_fma -lruy_have_built_path_for_avx512
// #cgo LDFLAGS: -lruy_denormal -lruy_tune -lruy_system_aligned_alloc
// #cgo LDFLAGS: -lcpuinfo -lclog
// #cgo darwin LDFLAGS: -framework Accelerate
// #cgo linux LDFLAGS: -lopenblas
// #cgo LDFLAGS: -lstdc++
```

And `Makefile` adds the corresponding `LIBRARY_PATH` segments:

```makefile
CT2_LIB := $(CT2_BUILD):$(CT2_BUILD)/third_party/cpu_features:$(CT2_BUILD)/third_party/ruy/ruy:$(CT2_BUILD)/third_party/ruy/third_party/cpuinfo:$(CT2_BUILD)/third_party/ruy/third_party/cpuinfo/deps/clog
```

## Other CT2-build pitfalls

### `CMAKE_POLICY_VERSION_MINIMUM=3.5` required

```
CMake Error at third_party/cpu_features/CMakeLists.txt:1 (cmake_minimum_required):
  Compatibility with CMake < 3.5 has been removed from CMake.
```

CMake 4.x dropped legacy compatibility. Fix in build-deps.sh:

```bash
cmake ... -DCMAKE_POLICY_VERSION_MINIMUM=3.5
```

### Target naming

SentencePiece CMake target is `sentencepiece-static` (not `sentencepiece`). CT2 target is `ctranslate2`. Whisper is `whisper`. No standard.

### Warning noise

CT2 vendors `nlohmann/json.hpp` and `half_float/half.hpp` which trigger `-Wdeprecated-literal-operator` under modern clang. Silence via:

```makefile
export CGO_CXXFLAGS := -Wno-deprecated-literal-operator
```

## Prevention

When upgrading CT2, run `make build` and feed each `Undefined symbols` line to a script that maps the prefix to a library name. The list grows roughly monotonically — additions, rarely removals.

## Date discovered

2026-05-15 (M3a first CT2 build).
