---
title: Go 1.26 SIMD (simd/archsimd)
date: 2026-05-15
tags:
  - library
  - go-1.26
  - simd
  - performance
aliases:
  - simd/archsimd
---

# Go 1.26 SIMD (`simd/archsimd`)

> [!important] Experimental, gated by build flag
> Set `GOEXPERIMENT=simd` for the package to be visible. The package is **not** subject to Go 1 compatibility — API may break.

## Import

```go
import "simd/archsimd"
```

The doc.go header is `//go:build goexperiment.simd` — so all files using SIMD **must** also have a matching build tag, otherwise they fail to compile on non-experimental builds:

```go
//go:build goexperiment.simd && amd64
```

See [[../../../internal/audio/sample/convert_simd_amd64.go|convert_simd_amd64.go]] for the canonical pattern in this repo.

## Architecture support

Currently **AMD64 only**. ARM64 (NEON, SVE) is on the roadmap but not in 1.26. Path source: `$GOROOT/src/simd/archsimd/types_amd64.go`.

## Vector types

128/256/512-bit registers exposed as structs:

| Width | int8 | int16 | int32 | float32 | float64 |
|---|---|---|---|---|---|
| 128 | `Int8x16` | `Int16x8` | `Int32x4` | `Float32x4` | `Float64x2` |
| 256 | `Int8x32` | `Int16x16` | `Int32x8` | `Float32x8` | `Float64x4` |
| 512 | `Int8x64` | `Int16x32` | `Int32x16` | `Float32x16` | `Float64x8` |

> [!warning] Don't take pointers
> `// For performance reasons, it is recommended to use the vector types directly as values. It is not recommended to take the address of a vector type, allocate it in the heap, or put it in an aggregate type.` — `doc.go`.

## CPU feature check

```go
var hasAVX2 = archsimd.X86.AVX2()
```

`X86` is a global `archsimd.X86Features` zero-size struct. Methods: `AVX()`, `AVX2()`, `AVX512()`, `AVX512BITALG()`, etc. See `$GOROOT/src/simd/archsimd/cpu.go`. Cache the result at init — checking inside the hot loop defeats the purpose.

## Useful int16 → float32 pipeline

```go
scale := archsimd.BroadcastFloat32x8(1.0 / 32768.0)
for ; i+8 <= n; i += 8 {
    v := archsimd.LoadInt16x8Slice(src[i:])   // 128-bit int16x8
    f := v.ExtendToInt32().ConvertToFloat32().Mul(scale) // → float32x8 (256-bit)
    f.StoreSlice(dst[i:])
}
```

Choice notes:

- `Int16x8 → Int32x8 → Float32x8` uses **256-bit AVX2** registers, broad CPU support (since Haswell 2013).
- `Int16x16 → Int32x16 → Float32x16` would use 512-bit registers — **AVX-512 required**, much rarer.
- `ExtendToInt32` returns the *next size up* int32 vector, not the same width — confusingly named.

## Functions worth knowing

| Function | Effect |
|---|---|
| `LoadInt16x8Slice(s)` | Load 8 int16 from `s[0:8]`; panics if shorter. |
| `LoadInt16x8SlicePart(s)` | Safe partial load; tail elements zero. |
| `v.StoreSlice(s)` | Store full vector; panics on short dst. |
| `v.StoreSlicePart(s)` | Safe partial store. |
| `BroadcastFloat32x8(x)` | Splat scalar to all 8 lanes. |
| `Float32x8.Mul(y)` | Element-wise multiply. |
| `Float32x8.MulAdd(y, z)` | Fused multiply-add. |
| `Int16x8.ExtendToInt32() Int32x8` | Sign-extend, widen to next size. |
| `Int32x8.ConvertToFloat32() Float32x8` | Integer→float. |

## Benchmarks (this repo, i7-9750H Coffee Lake)

| Path | ns/op | MB/s |
|---|---|---|
| Portable scalar (compiler auto-vectorised) | 128 | 7500 |
| AVX2 explicit | 117 | 8200 |

> [!note] Why the gain is modest
> The Go compiler in 1.26 already auto-vectorises the trivial `for i, v := range src { dst[i] = float32(v) * scale }` loop reasonably well on amd64. Explicit SIMD wins more on operations the compiler can't see (saturating arithmetic, gather/scatter, shuffles, packed comparisons). For this project, expect bigger wins in:
> - **Resampling** (rational rate conversion 22050↔48000)
> - **Saturating int conversion** (Float32→Int16 with clip)
> - **Mel-spectrogram FFT pre-multiply** (windowing)

## Pitfalls

> [!bug] Pitfall: code without build tag breaks non-experimental builds
> The simd package itself has `//go:build goexperiment.simd`. Any file that imports it **must** carry the same tag, plus an architecture tag if amd64-only. The matching portable file uses the inverted tag — see [[../gotchas/SIMD Build Tag]].

> [!bug] Pitfall: `Int16x8.ExtendToInt32` returns wider register
> Not the same lane count — it widens both the lanes and the register size. `Int16x8 → Int32x8` (4×128 bit → 8×256 bit conceptually). If you want the same-size truncated convert, that's a different function and may need AVX-512.

## References

- Article: https://antonz.org/go-1-26/
- Article: https://habr.com/ru/articles/995906/
- Article: https://habr.com/ru/companies/avito/articles/1000616/
- Source: `$GOROOT/src/simd/archsimd/`
- Used in: [[../../../internal/audio/sample/convert_simd_amd64.go|convert_simd_amd64.go]]
