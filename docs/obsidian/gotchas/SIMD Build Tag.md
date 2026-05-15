---
title: SIMD requires both env flag and build tag
date: 2026-05-15
tags:
  - gotcha
  - simd
  - build
---

# SIMD requires both env flag and build tag

## Symptom

Either:

```
package simd is not in std (...)
```

or:

```
undefined: archsimd.LoadInt16x8Slice
```

depending on how the import was written.

## Cause

Go 1.26's `simd/archsimd` package is experimental. It exists in the standard library tree (`$GOROOT/src/simd/archsimd/`) but every file has `//go:build goexperiment.simd`. Without the experiment flag the package compiles down to *no files at all* — `go doc simd/archsimd` returns `no buildable Go source files`.

Two separate switches:

1. **Environment**: `GOEXPERIMENT=simd` — instructs `go` to enable the `goexperiment.simd` build constraint.
2. **Build tag on caller files**: every `.go` file that imports `simd/archsimd` **must** also be guarded:
   ```go
   //go:build goexperiment.simd && amd64
   ```
   Otherwise it tries to compile on plain builds and fails.

## Fix

In this repo: two files per SIMD-accelerated function.

```go
// convert_simd_amd64.go
//go:build goexperiment.simd && amd64
package sample
import "simd/archsimd"
func Int16ToFloat32(...) { /* AVX2 path */ }
```

```go
// convert_portable.go
//go:build !goexperiment.simd || !amd64
package sample
func Int16ToFloat32(...) { /* scalar */ }
```

Both files declare `func Int16ToFloat32`; only one compiles at a time depending on the tag.

[[../../../Makefile|Makefile]] exports `GOEXPERIMENT=simd` by default; CI does the same. To build without SIMD: `GOEXPERIMENT= make build`.

## Prevention

If adding a new SIMD-optimised function, always create the portable twin in the same package or `go build` will fail on non-AMD64 or non-experimental Go.

## Date discovered

2026-05-15 (M1 sample conversion).
