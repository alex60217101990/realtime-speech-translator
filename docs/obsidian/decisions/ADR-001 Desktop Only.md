---
title: "ADR-001: Desktop only (no iOS/Android)"
date: 2026-05-15
tags:
  - decision
  - adr
  - accepted
status: accepted
deciders: alex60217101990
---

# ADR-001: Desktop only (no iOS/Android)

## Status

**Accepted** — 2026-05-15

## Context

The core feature is routing translated audio into a virtual microphone so other apps (Zoom, Discord, Meet, Teams, OBS) consume it as a normal mic input. This is the entire selling point.

On the four candidate platforms:

| Platform | Virtual mic possible? |
|---|---|
| macOS | Yes — BlackHole / Loopback / SoundFlower |
| Linux | Yes — PulseAudio `module-null-sink`, PipeWire `loopback` |
| Windows | Yes — VB-CABLE, VAC, Voicemeeter |
| iOS | **No** — sandbox forbids inter-app audio routing without jailbreak |
| Android | **No** — except via root or a narrow AccessibilityService trick limited to telephony |

## Decision

Ship **macOS + Linux + Windows** as M1–v1.0 targets. Mobile is deferred indefinitely and would, if attempted, be a **preview-only mode**: listen and play translated audio locally, but not route to other apps.

## Consequences

Positive:

- All three desktop OSes share the same Go + cgo build toolchain — single source of truth.
- Fyne v2 covers all three with one codebase.
- Virtual-mic install can be guided rather than bundled — see [[ADR-006 Virtual Mic Guided Install]].

Negative:

- Larger and more obvious competitive products exist on mobile (Google Translate, DeepL); we cannot reach them.
- Bluetooth mic UX is worse on desktop than mobile because the OS doesn't auto-route around HFP problems — see [[../concepts/Bluetooth HFP Trap]].

## Revisit when

- Apple opens iOS to virtual audio (unlikely).
- A new mobile use case emerges that does **not** need virtual mic (e.g. "translate the foreign speaker on my screen for me" — listen + play only).

## See also

- [[../../wiki/04-Virtual-Audio]]
- [[ADR-005 Fyne over Wails Flutter]]
