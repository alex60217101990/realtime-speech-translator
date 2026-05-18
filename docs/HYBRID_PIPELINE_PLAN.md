# Hybrid Pipeline Plan — best of `perf/simd-mt-tuning` over `feat/sherpa-rewrite`

Status: **draft / proposal — Whisper path SUPERSEDED by T-one** ·
Owner: aleksandr ·

> **Update 2026-05-18.** While drafting this plan we discovered that
> the **T-one** streaming Russian ASR (Voicekit / T-Software DC,
> Apache-2.0, 71.6 M params, 300 ms chunks, ~1.2 s total latency)
> is **already integrated into sherpa-onnx** as the
> `OnlineToneCtcModelConfig` recognizer family. The published
> tarball is `sherpa-onnx-streaming-t-one-russian-2025-09-08.tar.bz2`
> (128 MB, one `model.onnx` + `tokens.txt`). This obsoletes the
> VAD-plus-offline-Whisper detour for Russian: we now ship two
> homogeneous streaming backends inside the **same** `stt.Engine`
> implementation, selected per-model by a `ModelKind` switch:
>
>   - `ModelTransducer` — Zipformer trio (English, multilingual zh+en).
>   - `ModelToneCtc`    — T-one single .onnx (Russian, telephony-
>                          trained but also fine on 16 kHz mic input).
>
> Stages 1 and 6 of the original plan collapse into a one-day change.
> See §4.1 and §5 below — they are correct as written; the rest of
> the plan (sentence stitching, hallucination filter, TM, Fix-trans-
> lation UI, EventID, hot-swap MT) remains valuable independent of
> the STT backend choice. The `vadwhisper` sub-package becomes
> optional, kept only as a future fallback for languages without a
> streaming sherpa model.
Reference branches:

  - `perf/simd-mt-tuning` — yesterday's working build. Whisper.cpp
    based, WebRTC VAD, full async pipeline with sentence-stitching,
    hallucination filter, translation memory and a "Fix last
    translation" UI affordance.
  - `feat/sherpa-rewrite` — the rewrite. Streaming Zipformer (only
    English), Silero VAD, sherpa in-process Piper, lock-free SPSC
    ring, SIMD audio conversion, Models tab + installer, native TTS
    fallback. Loses every "smart" piece of pipeline logic from the
    perf branch.

Goal: a single hybrid branch that **keeps every infrastructure
improvement** from the rewrite and **re-introduces every piece of
pipeline-quality logic** from the perf branch, while picking the
best backend per language (streaming Zipformer for English, VAD +
offline Whisper for Russian and everything else).

---

## 1. What we keep from each branch

| concern                          | source        | why |
|----------------------------------|---------------|-----|
| sherpa-onnx engines (Zipformer + Piper)            | rewrite | per-platform Go modules, no whisper.cpp compile, in-process Piper with streaming PCM callback |
| Silero VAD                                          | rewrite | better than WebRTC on background-noise rejection, ships an ONNX model already in the catalog |
| SSE2 int16↔float32                                  | rewrite | 5×/10× faster than scalar, zero-alloc |
| Lock-free SPSC ringbuf (cache-line padded)          | rewrite | 21 GB/s, race-clean |
| capture/playback with `unsafe.Slice` zero-copy      | rewrite | one memmove per audio callback, sync.Pool buffers |
| Models tab + installer (catalog, tarball extract)   | rewrite | first-launch UX, runtime model swap |
| Native TTS fallback (say / espeak / powershell)    | rewrite | works on a fresh box without any model download |
| `internal/paths`, `config`, `logging`, `crashreport` | rewrite | already debugged, no reason to revisit |
| **Async pipeline topology** (mtQ / ttsQ / out)      | perf    | concurrency between utterance N+1 STT and N's translate is the whole reason perceived latency is low |
| **Sentence stitching guard**                        | perf    | mid-clause fragments stitched into one MT input (700 ms window, 4-fragment max, 4 s cap per fragment) |
| **Hallucination fingerprints + energy gate**        | perf    | drops YouTube-credit phrases and low-RMS noise utterances |
| **Whisper preprocessing**                           | perf    | 100 Hz high-pass IIR + peak normalisation (target 0.7, max 12×, no floor 0.005, ceiling 0.5) — single largest win against Whisper hallucinations |
| **Language seed prompts**                           | perf    | one-line phrase per ISO-639-1 code that primes the decoder; not rolling history (avoids compounding hallucination) |
| **Rolling vocab + prompt history**                  | perf    | top-20 words + last N finals concatenated into Whisper InitialPrompt, 200-rune cap |
| **MT translation memory** (LRU 1024 + JSON persist) | perf    | persistent cache with hit-rate / saved-time stats; Override() for user corrections |
| **"Fix last translation" UI dialog**                | perf    | user types correction → `cache.Override()` + sync to disk; the same source maps to the corrected target next time |
| **EventID async pairing**                           | perf    | monotonic counter on every STT Final; `TranslationUpdate` carries the same ID; the UI appends the translation line that matches |
| **Hot-swap MT** (`ReloadMT`)                        | perf    | install a new MT model in the Models tab → pipeline swaps engines without restart |
| **Latency decomposition stats**                     | perf    | hangover / queue / stt / mt / display / tts / total, not just one number |
| **Engine row for Piper installer in UI**            | perf    | mirrored in the rewrite's Models tab, keep the dialog UX |

## 2. What we drop or replace

  - `whisper.cpp` — replaced by sherpa-onnx's offline Whisper.
    Same model files (.bin) are *not* compatible; sherpa wants the
    .onnx pair (encoder.int8.onnx + decoder.int8.onnx) k2-fsa
    publishes. Tiny / base / small available, all multilingual.
  - WebRTC VAD — replaced by Silero ONNX. Aggressiveness 0..3 → a
    single `Threshold` knob in [0, 1] (0.5 default = ~aggressiveness 2).
  - Fyne `models_screen.go` from perf — replaced by the rewrite's
    `internal/ui/models_screen.go` + `internal/models/{catalog,
    installer}` which already handle tarball extract and progress.

## 3. Architecture after the merge

```
                ┌─────────────────────────────────────────────────┐
                │  cmd/translator + Fyne UI (Main / Models /      │
                │  Settings tabs, Start/Stop, Voice viz, two-col  │
                │  transcript/translation, Fix-last dialog)        │
                └─────────────────────────────────────────────────┘
                                       ▲
                                       │ EventID-matched
                                       │ Partial / Final / Translation
                                       │
┌──────────────────────────────────────┴──────────────────────────────────────┐
│                            internal/app.Session                              │
│                                                                              │
│  cap → stt.Backend → (stitch buffer) → mtQ (cap=4) → MT (Serial+Cached)      │
│                                              │                                │
│                                              ▼                                │
│                                          ttsQ (cap=2)                         │
│                                              │                                │
│                                              ▼                                │
│                                       tts.Engine (Piper)                      │
│                                              │                                │
│                                              ▼                                │
│                                        playback.Ring → device                 │
│                                                                              │
│  stats: capture / vad / utts / drops / underruns / mt latency / tts latency  │
│         + lag decomposition (queue, stt, mt, display, tts, total)            │
└──────────────────────────────────────────────────────────────────────────────┘
                                       ▲
                       ┌───────────────┴───────────────────┐
                       │                                   │
                ┌──────┴───────┐                  ┌────────┴────────┐
                │ Streaming    │                  │ VadWhisper      │
                │ Zipformer    │  ◄── stt.Backend │ offline Whisper │
                │ (English)    │                  │ (RU / other)    │
                └──────────────┘                  └─────────────────┘
                       │                                   │
                Silero VAD as gate                  Silero VAD segments,
                Partials + Finals                   Finals only, prompt-
                via OnlineRecognizer                seeded per language
```

The pipeline above the `stt.Backend` interface is **engine-agnostic**.
Sentence stitching, hallucination filter, MT cache, EventID and the
Fix-translation override all live in `internal/app`, not in either
backend.

## 4. New / changed packages

### 4.1 `internal/stt` (extend the existing package)

  - Already exports `Backend` interface (done in this branch).
  - Existing `Engine` (streaming Zipformer) keeps emitting both
    Partial and Final. Add an internal RMS-energy gate before the
    first AcceptWaveform of a new in-segment burst so quiet noise
    cannot start an utterance.

### 4.2 `internal/stt/vadwhisper` (new sub-package)

  - Wraps `sherpa.VoiceActivityDetector` + `sherpa.OfflineRecognizer`
    (Whisper model type).
  - One goroutine: feed mic into VAD, drain `vad.Front()` segments,
    apply preprocessing (HPF 100 Hz + peak normalise), AcceptWaveform
    on a fresh `OfflineStream`, Decode, GetResult, emit Final with
    detected language metadata.
  - No Partial events (sherpa Whisper offers no incremental output).
  - Knobs (`Config`) and defaults ported from perf's
    `internal/stt/whisper.go` and `internal/audio/vad/vad.go`:
      - `Threads = max(NumCPU/2, 2)` — leaves cycles for MT.
      - `Language` (ISO-639-1 or "auto") forwarded to Whisper.
      - `LanguageSeed` table (13 entries: ru, uk, en, es, de, fr, it,
        pt, pl, nl, tr, ja, zh) when Language != "auto".
      - `MinSpeechSec = 0.30`, `MaxSpeechSec = 12`,
        `MinSilenceSec = 0.20`, `Threshold = 0.5` for VAD.
      - `PeakTarget = 0.7`, `PeakCeiling = 0.5`,
        `PeakFloor = 0.005`, `PeakMaxGain = 12`.
  - Implements the `stt.Backend` interface so `app.Session` can
    accept it interchangeably.

### 4.3 `internal/app/session.go` (extend)

Add the smart pipeline pieces from the perf branch on top of what
already exists:

  - **EventID** counter — monotonic uint64 stamped on every Final
    and every Translation; UI uses it for async pairing.
  - **Stitch buffer** — same shape as perf's `routeMTJob`:
    `stitchWindow = 700 ms`, `stitchMaxUtts = 4`,
    `stitchMaxUttDuration = 4 s`. Behaviour: when a Final lacks
    terminal punctuation (`.?!…`) and is shorter than the cap, hold
    it; the next Final within the window concatenates; flush on
    terminal punctuation, on cap, or on window expiry.
  - **Hallucination filter** — port the exact fingerprint map +
    normalisation rule (lowercase, trim, strip `[]` and `()`).
  - **Rolling prompt** — `PromptHistory = 1` (configurable),
    concatenate last N finals + top-20 vocab into a 200-rune
    `InitialPrompt` and hand to the backend via a new
    `Backend.SetPrompt(string)` extension method. Streaming Zipformer
    can ignore it (sherpa OnlineRecognizer has hotwords instead);
    VadWhisper uses it as Whisper InitialPrompt.
  - **Energy gate** — keep a running EWMA of capture RMS
    (`α = 0.97`), reject utterances whose peak < `bgPeak × 5.0`
    or < absolute floor (≈ 800 / 32768 in float32 = 0.024).
    Implemented in the stitch step so backends do not have to
    repeat the logic.
  - **Latency decomposition** — extend `Stats` with `QueueWait`,
    `STTLatency`, `MTLatency`, `TTSLatency`, `DisplayLatency`,
    `TotalLag` (per last utterance, EWMA over 10).
  - **Hot-swap MT** — `Session.ReloadMT(mt.Engine)` that wraps in
    `Serial`, takes the cache forward, calls back into `runMT` via
    an atomic pointer.

### 4.4 `internal/mt/cache.go` (already pulled from main)

  - Wire `LoadCached` into the cmd/translator boot path; today the
    rewrite only constructs `mt.Disabled` or `Serial(small100)` —
    neither is wrapped in the cache.
  - Persist cache file at `paths.TranslationMemory()` with a 30 s
    sync ticker (matches perf).

### 4.5 `internal/ui` + `cmd/translator` (additive)

  - Add the "Fix last translation" button next to Start/Stop. Opens
    a dialog with the source text + an editable translation field;
    on Save, calls `cache.Override(src, srcLang, tgtLang, edited)`
    and triggers a Sync.
  - Add a pin / checkmark column to the translation list — pinned
    entries are never evicted from the LRU. Persist via the same
    cache file.
  - Status block: add a fourth row with the cache stats
    (`TM: N entries (M pinned)   hits H / miss M (R%)   saved ≈ T`)
    matching the perf branch's UI exactly.

## 5. Backend selection logic

`config.Settings` gains a single field:

```go
STTEngine string `yaml:"stt_engine"` // "auto" | "streaming" | "vad_whisper"
```

`auto` resolves at session construction:

  - If `SourceLang == "en"` and a streaming Zipformer model is
    installed → use streaming.
  - Else if a Whisper model is installed → use vad_whisper.
  - Else → emit a Models-required hint via the UI placeholder
    (already wired).

This keeps single-language English users on the snappier streaming
engine and routes everything multilingual through Whisper.

## 6. Implementation stages

### Stage 1 — backend factory + VadWhisper

  - `internal/stt/vadwhisper/vadwhisper.go` with config + Engine.
  - `internal/stt.Factory(cfg)` that returns the right Backend.
  - `cmd/translator`: switch buildSession to the factory.
  - Models catalog: add Whisper-tiny (~75 MB) and Whisper-base
    (~140 MB). Default voice for the multilingual path is base.
  - Effort: 1–1.5 days.

### Stage 2 — pipeline-quality logic

  - Stitch buffer in `internal/app/session.go`.
  - Hallucination filter + normalisation helpers.
  - Energy gate in `internal/audio/capture` (EWMA + gate check
    surfaced as a flag on the buffer handed to PushFunc — backends
    can ignore the flag if they have their own VAD).
  - EventID + Translation pairing.
  - Effort: 1–2 days.

### Stage 3 — translation memory

  - Wire `mt.LoadCached` into the boot path.
  - Persist at `paths.TranslationMemory()`; 30 s sync ticker.
  - Stats exposed via `Session.Stats().CacheStats`.
  - Effort: half a day.

### Stage 4 — UI features

  - Fix-last-translation button + dialog.
  - Pin column on the translation list.
  - Cache stats row in the status block.
  - Effort: 1 day.

### Stage 5 — hot-swap MT

  - `Session.ReloadMT()` with atomic pointer swap.
  - `OnInstalled` hook for MT entries in the Models tab calls
    `Session.ReloadMT()` instead of full session restart.
  - Effort: half a day.

### Stage 6 — language seed + rolling prompt

  - Implement `Backend.SetPrompt(string)` on both backends; no-op
    for streaming Zipformer.
  - VadWhisper consumes prompt via Whisper InitialPrompt (the
    sherpa offline Whisper config exposes this through its own
    `Hr` / decoding params — verify before committing; if absent we
    skip, no quality regression).
  - Rolling prompt builder in `internal/app/session.go`:
    `topVocab(20) + tail(PromptHistory finals)`, capped at 200 runes.
  - Effort: 0.5–1 day.

**Total budget: ~5 working days** for the full hybrid behaviour
above. Compare to the four engineer-months a from-scratch rewrite
of the perf branch on a different ASR stack would cost.

## 7. Risk matrix

| risk                                                       | likelihood | mitigation |
|------------------------------------------------------------|------------|------------|
| sherpa offline Whisper has no `InitialPrompt` knob          | medium     | Drop the rolling-prompt stage gracefully; quality stays close to perf branch already because of the seed phrase + preprocessing |
| Whisper-base too slow on the user's Intel i7-9750H          | low        | Stage 1 already includes tiny as a fallback; UI dropdown lets the user pick |
| Backend switch invalidates the current Final / mtQ in flight | low        | `Session.ReloadMT` and any backend swap must drain mtQ and stop pumping before swap (one atomic.Bool + a wait on the drain channel) |
| Cache file races with concurrent writes                     | low        | 30 s ticker is single-goroutine; UI Override blocks on the same mutex |
| Energy gate false-rejects quiet legitimate speech           | medium     | Floor + ratio tunable in Config; expose in Settings under "Advanced" later |

## 8. Open questions

  1. Do we keep streaming Zipformer **at all** if we ship Whisper-
     tiny/base? Streaming gives true Partials and lower first-token
     latency; Whisper covers more languages. Default answer: yes, keep
     both; let the user pick. The cost of supporting both is small
     because both already implement `stt.Backend`.
  2. Should the energy gate live in `capture` (so every backend
     benefits) or stay in `app/session` (so it can be sidesteped
     for diagnostic recordings)? Default: in `app/session` —
     diagnostic CLIs (`cmd/stt-mic`) skip the gate by construction.
  3. Pin-list persistence — JSON next to TM file, or a sqlite? JSON
     is enough at 1 KiB / entry; sqlite is a new cgo dep we do not
     need. Default: JSON.

## 9. What we deliberately do NOT do in this hybrid

  - **Do not bring back whisper.cpp.** The whole reason for the
    rewrite was to escape the build-time tax and the per-OS
    packaging headache of bundled native libs. sherpa Whisper is
    feature-equivalent for our pipeline.
  - **Do not move STT inference back to the audio callback.** All
    decoding stays on its own goroutine (perf branch did the same).
  - **Do not chase 120 FPS in Fyne.** UI polish that needs it is
    covered separately by `docs/UI_REWRITE_PLAN.md`.

## 10. Acceptance checklist (when the hybrid is "done")

  - [ ] `make build` produces translator that on a fresh `<data>`
        opens the window, shows Models tab, lets the user install
        the default RU pack (Whisper-base + Silero VAD), then
        immediately decodes Russian speech with Whisper.
  - [ ] Installing the EN pack (Streaming Zipformer + Silero VAD)
        and setting `SourceLang=en` flips the backend to streaming
        Zipformer on next Start.
  - [ ] Saying two short clauses inside the 700 ms window produces
        **one** Translation event, not two.
  - [ ] Saying "thanks for watching, like and subscribe" produces
        no STT Final (hallucination filter drops it).
  - [ ] Speaking the same Russian phrase twice produces a TM hit
        the second time; `Session.Stats().CacheStats.Hits` ticks.
  - [ ] Fix-last-translation dialog persists across restart.
  - [ ] Installing a new MT model in Models tab swaps the engine
        without losing the current capture session.

## 11. References

  - Perf branch findings live in
    `git show perf/simd-mt-tuning:internal/stt/pipeline.go` and
    `git show perf/simd-mt-tuning:internal/audio/vad/vad.go`.
  - Sherpa offline Whisper API:
    `~/go/pkg/mod/github.com/k2-fsa/sherpa-onnx-go-macos@v1.13.2/sherpa_onnx.go:485`
    (`OfflineWhisperModelConfig`, `OfflineRecognizer`,
    `OfflineStream`).
  - Sherpa published Whisper model URLs:
    <https://k2-fsa.github.io/sherpa/onnx/pretrained_models/whisper/index.html>
