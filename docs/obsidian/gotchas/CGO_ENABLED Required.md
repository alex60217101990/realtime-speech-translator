---
title: CGO_ENABLED required for build
date: 2026-05-15
tags:
  - gotcha
  - build
  - cgo
---

# `CGO_ENABLED=1` required for build

## Symptom

```
undefined: malgo.DeviceID
undefined: malgo.Device
undefined: malgo.AllocatedContext
undefined: malgo.DefaultDeviceConfig
undefined: malgo.InitDevice
undefined: malgo.DeviceCallbacks
```

…or analogous for `webrtcvad`, `whisper.cpp` bindings.

## Cause

All audio-stack libraries are cgo-only. Their Go source files have build tags like `//go:build cgo` (or no pure-Go fallback). `CGO_ENABLED=0` excludes them and the types disappear.

By default `go build` enables cgo *only when cross-compilation matches the host*, but on certain CI runners and some local shells it ends up disabled.

## Fix

Always export at the build entrypoints:

```bash
export CGO_ENABLED=1
```

In this repo:
- [[../../../Makefile|Makefile]] sets `export CGO_ENABLED` at the top.
- [[../../../.github/workflows/build.yml|CI workflow]] has it in the job-level `env:` block.

## Prevention

Add to [[../architecture/Cgo Boundary]] checklist: any new third-party dep — verify it builds with `CGO_ENABLED=0 go build ./its/package` and document.

## Date discovered

2026-05-15 (M1 first integration of malgo).
