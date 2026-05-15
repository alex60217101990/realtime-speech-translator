---
title: MT Stage (M3a)
date: 2026-05-15
tags:
  - architecture
  - mt
  - milestone-m3
---

# MT Stage (M3a)

Whisper transcripts → Engine.Translate → translated text. Slots between STT and (future) TTS in the pipeline.

## Component diagram

```
stt.Pipeline.handleUtterance
        │
        ▼
mt.Engine.Translate(src, srcLang, dstLang)
        │
        ├── MADLAD path
        │     ├── sp.Processor.EncodePieces("<2en> hello")     SentencePiece (shared)
        │     ├── ct2.Translator.Translate(pieces, …)          CTranslate2
        │     └── sp.Processor.DecodePieces(outPieces)
        │
        └── OPUS-MT path (per pair {src}-{dst})
              ├── sp.Processor[src].EncodePieces(text)          SentencePiece source.spm
              ├── ct2.Translator[pair].Translate(pieces, …)     CTranslate2
              └── sp.Processor[dst].DecodePieces(outPieces)     SentencePiece target.spm
```

## Backend selection

Made at session construction time in [[../../../cmd/translator/main.go|main.go]]:

```
--mt madlad        # default (MADLAD-400-3B int8)
--mt opusmt        # OPUS-MT per-pair
--mt off           # transcript only
```

Both backends implement the `mt.Engine` interface ([[../../../internal/mt/mt.go]]). The pipeline wraps them in `mt.Serial` so that two threads cannot call CT2 concurrently — see [[#Thread safety]].

## Backpressure

Translation is the *second* slow stage after Whisper. The VAD output channel is the buffer between them (cap = 4). If MT lags, utterances accumulate and eventually drop. The Pipeline does not block the audio thread.

## Initial-prompt continuity

Whisper's rolling prompt was added in M2. MT does **not** consume that prompt. Per-utterance translation is stateless — MADLAD and OPUS-MT both treat each call independently. A future enhancement is to prepend the last final to source text as a discourse context (sometimes helps with anaphora) but it doubles input length and is deferred until we measure quality regressions on long conversations.

## Thread safety

CTranslate2's `Translator` object is internally threaded (each call may fork worker threads from a thread pool sized by `num_threads_per_replica`) but is **not** documented as thread-safe for overlapping `translate_batch` calls. The pipeline's `mt.Serial` wrapper takes a Mutex around each Translate to enforce sequential decoding.

## MADLAD target-tag mapping

MADLAD encodes target language as a literal prefix token in the source:

```
input  = "<2ru> Hello, how are you?"
output = "Привет, как дела?"
```

We map ISO-639-1 → `<2xx>` tags in [[../../../internal/mt/madlad.go|madlad.go]]'s `madladTags` table. Only the common subset is listed (~28 languages); extend per release.

## OPUS-MT model layout

Models live under `ModelsRoot/<src>-<dst>/`:

```
opusmt-models/
├── ru-en/
│   ├── model.bin               (CTranslate2 export)
│   ├── shared_vocabulary.json
│   ├── source.spm              (SentencePiece source)
│   └── target.spm              (SentencePiece target)
├── en-ru/
└── …
```

The Helsinki-NLP convention: separate `.spm` per side, ct2-exported `.bin`. Loaded lazily on first translation request and cached for the lifetime of the OPUSMT engine.

## Latency budget (M3a target)

```
VAD close       300 ms (hangover)
STT (small)     200–400 ms
MT (MADLAD-3B)  600–900 ms        ← new in M3a
Playback queue  ~50 ms (TTS still M3b)
──────────────────────────────────────
total           1.2 – 1.7 s end-to-end (post-VAD)
```

Still within the 2 s p95 target. If MADLAD proves too heavy on common laptops, the user can pick `--mt opusmt` for ~200 ms instead.

## Source

- [[../../../internal/mt/mt.go]]
- [[../../../internal/mt/madlad.go]]
- [[../../../internal/mt/opusmt.go]]
- [[../../../internal/mt/ct2/ct2.go]]
- [[../../../internal/mt/sp/sp.go]]
- [[../../../internal/stt/pipeline.go]]
- [[../../../cmd/translator/main.go]]
