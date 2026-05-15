---
title: go-webrtcvad
date: 2026-05-15
tags:
  - library
  - audio
  - vad
  - cgo
aliases:
  - webrtcvad
  - github.com/maxhawkins/go-webrtcvad
---

# go-webrtcvad

Cgo binding to Google's WebRTC voice-activity detector C code.

## Version pinned

`v0.0.0-20210121163624-be60036f3083` — last commit on master, unchanged since 2021. Stable; the upstream C code hasn't moved either.

## API

```go
v, _ := webrtcvad.New()
defer v = nil // freed by runtime finalizer
v.SetMode(2)  // 0..3, 2 = balanced

active, err := v.Process(sampleRate, frameBytes)
```

> [!important] Input format
> `frameBytes` is **int16 LE** as `[]byte`, not `[]int16`. The wrapper unsafely re-types it inside. From a `[]int16 frame`:
>
> ```go
> bytes := unsafe.Slice((*byte)(unsafe.Pointer(&frame[0])), len(frame)*2)
> ```

## Supported frame sizes

WebRTC accepts **only** 10, 20, 30 ms frames at 8 / 16 / 32 / 48 kHz. We use 30 ms @ 16 kHz = 480 samples per frame. Other sizes cause `Process` to return `false, error`. Validate via `ValidRateAndFrameLength` if accepting user input.

## Modes (aggressiveness)

| Mode | Behaviour |
|---|---|
| 0 | Lax — accepts most input as speech, low false negatives |
| 1 | Default |
| 2 | **This project default** — balanced |
| 3 | Strict — rejects more, may clip soft consonants |

Tunable in [[../../../internal/audio/vad/vad.go#Config|vad.Config.Aggressiveness]].

## Build

Cgo only. No external system libs — C source is vendored in the package. `CGO_ENABLED=1` mandatory.

## Pitfalls

> [!bug] Stateless mode
> WebRTC VAD has no internal smoothing — each frame is judged independently. Real-time stability comes from *our* hangover/min-speech logic on top, not from the VAD itself.

> [!bug] Sensitivity to gain
> Very quiet input (< -40 dBFS) gets rejected even with mode 0. If a BT mic is muted or far-field, all frames return inactive.

## See also

- Repo: https://github.com/maxhawkins/go-webrtcvad
- Wrapper: [[../../../internal/audio/vad/vad.go|vad.go]]
- Concept: [[../concepts/VAD Segmentation]]
