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
- [[architecture/STT Pipeline M2]] — current milestone wiring, thread model, backpressure
- [[architecture/Goroutine Model]] — channels, backpressure, OS-thread locking
- [[architecture/Cgo Boundary]] — where cgo lives, why, build implications

### Libraries
- [[libraries/malgo]] — miniaudio Go binding, callback semantics
- [[libraries/whisper.cpp]] — STT, bindings/go submodule, build wiring
- [[libraries/go-webrtcvad]] — VAD frames, byte input format
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
- [[decisions/ADR-003 NLLB CC-BY-NC]] — licensing implications
- [[decisions/ADR-004 Monolithic cgo Binary]] — no sidecars
- [[decisions/ADR-005 Fyne over Wails Flutter]] — pure Go, mobile-ready

### Gotchas
- [[gotchas/CGO_ENABLED Required]] — malgo, vad, whisper all need cgo
- [[gotchas/SIMD Build Tag]] — `GOEXPERIMENT=simd` required, gated by build constraint
- [[gotchas/VAD Hangover Inflates Duration]] — track speech-only ms separately
- [[gotchas/Whisper Build Env]] — `C_INCLUDE_PATH` + `LIBRARY_PATH` mandatory
- [[gotchas/Audio Thread No Allocation]] — capture/playback callbacks must be alloc-free

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
