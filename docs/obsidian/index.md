---
title: Realtime Speech Translator — Knowledge Vault
date: 2026-05-15
tags:
  - moc
aliases:
  - Home
  - Vault Index
cssclasses:
  - vault-index
---

# Realtime Speech Translator — Knowledge Vault

> [!info] Purpose
> Engineering knowledge graph for the **realtime-speech-translator** project. Captures non-obvious facts, decisions, gotchas, and library quirks discovered during implementation so future sessions can pick up without re-discovering. Distinct from [[../wiki/Home|user-facing wiki]].

## Map of Content

### Architecture
- [[architecture/Audio Pipeline]] — capture → VAD → STT → MT → TTS → playback
- [[architecture/STT Pipeline M2]] — M2 milestone wiring (VAD + Whisper)
- [[architecture/MT Stage M3a]] — M3a MT integration (MADLAD/OPUS-MT via CT2+SP)
- [[architecture/TTS Stage M3b]] — M3b TTS + playback (Piper subprocess, ringbuf)
- [[architecture/Virtual Mic M4]] — M4 virtual-mic detection + guided install
- [[architecture/Goroutine Model]] — channels, backpressure, OS-thread locking
- [[architecture/Cgo Boundary]] — where cgo lives, why, build implications

### Libraries
- [[libraries/malgo]] — miniaudio Go binding, callback semantics
- [[libraries/whisper.cpp]] — STT, bindings/go submodule, build wiring
- [[libraries/go-webrtcvad]] — VAD frames, byte input format
- [[libraries/CTranslate2]] — Transformer inference for MT; cgo shim and 30+ static libs
- [[libraries/SentencePiece]] — subword tokenizer for MT; abseil merge gotcha
- [[libraries/Piper TTS]] — TTS subprocess, 22050 Hz PCM, voice + JSON sidecar
- [[libraries/Fyne v2]] — UI framework, thread-safety, widget binding
- [[libraries/Go 1.26 SIMD]] — `simd/archsimd`, build tag, AVX2 path

### Concepts
- [[concepts/Audio Sample Formats]] — int16/float32 conventions, sample rates
- [[concepts/VAD Segmentation]] — frame size, hangover, pre-pad
- [[concepts/Whisper Streaming]] — sliding window vs VAD-batched
- [[concepts/Bluetooth HFP Trap]] — why mic kills A2DP playback quality

### Decisions
- [[decisions/ADR-001 Desktop Only]] — drop iOS/Android, virtual mic feasibility
- [[decisions/ADR-002 Piper over XTTS]] — neutral voice, resource budget
- [[decisions/ADR-003 NLLB CC-BY-NC]] — licensing implications (now superseded)
- [[decisions/ADR-004 Monolithic cgo Binary]] — no sidecars
- [[decisions/ADR-005 Fyne over Wails Flutter]] — pure Go, mobile-ready
- [[decisions/ADR-006 Virtual Mic Guided Install]] — detect, never bundle; Linux auto-install only
- [[decisions/ADR-007 MADLAD over NLLB]] — default MT, Apache-2.0, 419 languages
- [[decisions/ADR-008 Piper Subprocess]] — TTS via subprocess for M3b, cgo migration deferred

### Gotchas
- [[gotchas/CGO_ENABLED Required]] — malgo, vad, whisper all need cgo
- [[gotchas/SIMD Build Tag]] — `GOEXPERIMENT=simd` required, gated by build constraint
- [[gotchas/VAD Hangover Inflates Duration]] — track speech-only ms separately
- [[gotchas/Whisper Build Env]] — `C_INCLUDE_PATH` + `LIBRARY_PATH` mandatory
- [[gotchas/Audio Thread No Allocation]] — capture/playback callbacks must be alloc-free
- [[gotchas/SP Abseil Static Bundling]] — SP doesn't bundle abseil; merge `libabsl_combined.a`
- [[gotchas/CT2 Linker Symphony]] — 30+ static libs in cgo LDFLAGS, CMake policy override

## Conventions

> [!note] Vault rules
> - One concept per file. Use `[[wikilinks]]` to connect, not headings.
> - Add a `tags:` frontmatter entry with at least one of: `library`, `concept`, `decision`, `gotcha`, `architecture`.
> - When a fact is discovered while debugging, also add it to [[gotchas/]] with the date and the symptom that surfaced it.
> - ADRs (Architecture Decision Records) use the form `ADR-NNN Short Title.md`; once accepted, do not edit retroactively — supersede with a new ADR and link.
> - Cite source file paths as `relative/path:line` so future sessions can jump straight to the code.

## Project state

- **Current milestone:** M2 — STT integration ([[../wiki/08-Roadmap|roadmap]])
- **Go:** 1.26 (`go.mod`)
- **Platforms:** macOS, Linux, Windows (desktop)
- **Build root:** [[../../Makefile|Makefile]] — `make build`, `make test-race`, `make bench`

## Recent log

- 2026-05-15 — M1 skeleton committed (`ea9f21f`).
- 2026-05-15 — M2 done: whisper.cpp `v1.8.4` submodule, static libs built, [[libraries/go-webrtcvad]] integrated, [[libraries/whisper.cpp|whisper]] Go wrapper, [[architecture/STT Pipeline M2|VAD→Whisper pipeline]] wired into [[../../internal/app/session.go|Session]], Fyne transcript pane. Downloader supports resume + sha256 + retry. Tests + race + vet green.
- 2026-05-15 — M3a done: [[libraries/CTranslate2|CTranslate2 v4.7.1]] + [[libraries/SentencePiece|SentencePiece v0.2.1]] submodules + cgo shims. MT engine interface with [[architecture/MT Stage M3a|MADLAD-400-3B (Apache-2.0, default)]] and per-pair OPUS-MT backends. Manifest updated, UI split into transcript/translation panes. ADR-007 supersedes NLLB. Build wiring documented in [[gotchas/CT2 Linker Symphony]] and [[gotchas/SP Abseil Static Bundling]].
- 2026-05-15 — M3b done: [[libraries/Piper TTS|Piper TTS subprocess]] wrapper, playback device reintroduced, [[architecture/TTS Stage M3b|TTS branch wired into Pipeline]]. Manifest grew with 5 default voices (en/ru/es/de/fr). `--tts on` + `--voice lang=path` on CLI. End-to-end loop closed: speaker hears translated audio. ADR-008 documents subprocess-over-cgo decision and migration plan.
- 2026-05-15 — M4 done: [[architecture/Virtual Mic M4|internal/vmic]] enumerates BlackHole / VB-CABLE / PulseAudio null-sink across platforms; Session accepts `PlaybackDeviceID`; first-launch install wizard with OS-specific guidance and Linux auto-install via `pactl`. ADR-006 fixes the detect-don't-bundle policy.
