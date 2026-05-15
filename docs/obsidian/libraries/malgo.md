---
title: malgo (miniaudio Go binding)
date: 2026-05-15
tags:
  - library
  - audio
  - cgo
aliases:
  - github.com/gen2brain/malgo
  - miniaudio
---

# malgo — `github.com/gen2brain/malgo`

Go cgo binding for [miniaudio](https://miniaud.io/), the C audio library used for cross-platform capture/playback.

## Version pinned

`v0.11.25` — see `go.mod`.

## Build requirement

> [!warning] CGO mandatory
> All malgo files have build tags excluding pure-Go builds. **`CGO_ENABLED=1` is required**; `go build` with `CGO_ENABLED=0` will fail with `undefined: malgo.DeviceID`. We export this in [[../../../Makefile|Makefile]] and CI.

No extra system libs on macOS (uses CoreAudio). On Linux: `libasound2-dev` + `libpulse-dev`. On Windows: WASAPI bundled.

## Core API surface

```go
ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, logCallback)
defer func(){ _ = ctx.Uninit(); ctx.Free() }()

dc := malgo.DefaultDeviceConfig(malgo.Capture)   // or Playback / Duplex
dc.Capture.Format   = malgo.FormatS16
dc.Capture.Channels = 1
dc.SampleRate       = 16000
dc.Alsa.NoMMap      = 1   // safer on Linux

dev, _ := malgo.InitDevice(ctx.Context, dc, malgo.DeviceCallbacks{
    Data: onFrames,
})
dev.Start()
// ...
dev.Stop()
dev.Uninit()
```

Note `ctx.Context` (lowercase `Context` is the C handle) is passed to `InitDevice`, not the `*AllocatedContext` itself.

## Callback semantics

Signature:

```go
func onFrames(pOutput, pInput []byte, framecount uint32)
```

> [!important] Real-time thread
> The Data callback runs on a **miniaudio-owned OS thread**, not a Go goroutine you spawned. Consequences:
> - No allocations — they would trigger GC pauses that ruin latency.
> - No blocking syscalls.
> - No `select` on a channel that might block (`select` with `default` is OK).
> - Slice arguments are **borrowed for the call duration only** — must not be retained.

For capture, `pOutput` is nil-ish (or unused for `DeviceTypeCapture`). For playback, fill `pOutput`; `pInput` is unused.

## Zero-copy byte→int16 view

For `FormatS16` mono the byte buffer is exactly `framecount * 2` bytes and 2-byte aligned:

```go
hdr := unsafe.SliceData(pSample)
samples := unsafe.Slice((*int16)(unsafe.Pointer(hdr)), int(framecount))
```

See [[../../../internal/audio/capture/capture.go|capture.go]] and [[../../../internal/audio/playback/playback.go|playback.go]].

## Device enumeration

`ctx.Devices(malgo.Capture)` returns `[]DeviceInfo`. Each has `ID *DeviceID` and `Name string`. To open a specific device set `dc.Capture.DeviceID = info.ID.Pointer()`.

## Underrun / overrun

Not surfaced via API. Instead infer from "callback came in with the same buffer repeatedly" or measure via the application-side ring (compare write and read pointers). Our session code reports both via [[../../../internal/app/session.go#DroppedSamples|DroppedSamples]] and `Underruns()`.

## Bluetooth caveats

When a BT headset's mic is opened on macOS, the system switches the device into HFP mode (8 or 16 kHz mono SCO) which **also degrades the playback channel** to that same quality. malgo cannot prevent this — see [[../concepts/Bluetooth HFP Trap]].

## References

- Repo: https://github.com/gen2brain/malgo
- Examples: `~/go/pkg/mod/github.com/gen2brain/malgo@v0.11.25/_examples/`
- Used in: [[../architecture/Audio Pipeline]], [[../../../internal/audio/capture/capture.go|capture.go]], [[../../../internal/audio/playback/playback.go|playback.go]]
