---
title: VAD hangover inflates duration if you don't track active-only ms
date: 2026-05-15
tags:
  - gotcha
  - vad
  - audio
---

# VAD hangover inflates duration if you don't track active-only ms

## Symptom

Test expected an utterance duration of ~800 ms (the actual speech length) but got **1260 ms**.

Also: a 100 ms speech burst with `MinSpeechMs=300` was **not** dropped — it passed the min-speech gate.

## Cause

We accumulate **all** frames into `curr []int16`, including:

- The **pre-pad** ring (~150 ms of pre-speech silence prepended to catch onset consonants).
- The **hangover** silence (~300 ms after speech ends, before we close the utterance).

So `speechMs := len(curr) * 1000 / SampleRate` ≈ `prePad + actualSpeech + hangover`. For 800 ms speech that is 150 + 800 + 300 = 1250 ms — matches the failing assertion.

This also lets a 100 ms speech burst pass `speechMs >= MinSpeechMs` because pre-pad + hangover already total 450 ms.

## Fix

Track active-speech time separately. In [[../../../internal/audio/vad/vad.go|vad.go]]:

```go
type Segmenter struct {
    // ...
    silenceMs int
    speechMs  int   // active-only duration; excludes hangover and pre-pad
}

func (s *Segmenter) processFrame(frame []int16) {
    if active {
        s.silenceMs = 0
        s.speechMs += FrameMs
    } else {
        s.silenceMs += FrameMs
    }
    // close conditions check speechMs against MinSpeechMs / MaxSpeechMs
}
```

`Utterance.PCM` still contains the pre-pad + speech + hangover (Whisper benefits from trailing silence to detect end-of-utterance), but `Duration` reports the active-only ms.

## Prevention

Whenever a metric is derived from a buffer that contains padding, **measure the metric at the point of decision**, not by deriving it from the buffer length after the fact.

## Date discovered

2026-05-15 (M2 VAD tests).
