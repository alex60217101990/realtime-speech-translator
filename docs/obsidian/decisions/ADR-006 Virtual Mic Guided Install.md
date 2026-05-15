---
title: "ADR-006: Virtual mic is detected, never bundled"
date: 2026-05-15
tags:
  - decision
  - adr
  - accepted
  - vmic
  - licensing
status: accepted
deciders: alex60217101990
---

# ADR-006: Virtual mic is detected, never bundled

## Status

**Accepted** — 2026-05-15.

## Context

The headline feature requires a virtual playback device that other applications see as a microphone. Three viable backends exist:

| OS | Backend | License |
|---|---|---|
| macOS | BlackHole 2ch (Existential Audio) | **GPL-3** |
| Linux | PulseAudio `module-null-sink` / PipeWire loopback | LGPL-2.1+ (already on the system) |
| Windows | VB-CABLE (VB-Audio Software) | Freeware with non-redistribution clause |

Bundling these drivers into our installer would either:

- Force the whole application into GPL-3 (BlackHole),
- Or violate VB-CABLE's terms (the EULA requires an individual licence agreement to redistribute),
- Or in the Linux case duplicate functionality already in the OS.

There is also a hard technical wall: BlackHole on macOS requires a system audio HAL plug-in installation that must be done with kernel-extension-style privileges. We cannot do that silently from inside the app on any modern macOS version.

## Decision

1. **Detect, do not bundle.** [[../../../internal/vmic|internal/vmic]] enumerates the playback devices miniaudio reports and pattern-matches their names against the known backends.
2. **Guide the user through install.** The first-launch wizard ([[../../../cmd/translator/main.go#showVMicWizard|showVMicWizard]]) shows OS-specific steps and a URL to the upstream installer.
3. **Auto-install only where it is free.** Linux is the exception: `pactl load-module module-null-sink` needs no extra binary, no admin elevation, and no licence dance, so [[../../../internal/vmic/install.go#AutoInstall|vmic.AutoInstall]] does it for the user.
4. **Preview mode is always available.** If the user dismisses the wizard or installation fails, the Session continues to work — TTS audio plays through the system default output (speakers / headphones) instead of the virtual mic. Translation is still useful as a personal real-time speech aid.

## Consequences

Positive:

- Apache-2.0 stays Apache-2.0. No copyleft contamination.
- The macOS .pkg and Windows installer stay tiny; users who already have BlackHole or VB-CABLE save the duplicate download.
- Distribution moves through the upstream-author channel each backend asks for — keeps us out of their support tickets.

Negative:

- First-run UX requires an external installer. Without further polish (M5/Settings) users may not realise the driver is needed for the headline feature.
- macOS installs of BlackHole require a brief password prompt outside our app — looks scary, beyond our control.
- We must keep the name-pattern table (`patternsFor` in [[../../../internal/vmic/vmic.go]]) up to date as each driver's vendor releases new variants.

## Revisit when

- Apple or Microsoft adds a first-party virtual mic API (unlikely in the next 24 months).
- The project picks up a maintainer willing to ship and sign our own HAL plugin / WDM driver. That is a 3–6 month effort plus per-OS code signing certificates — not on the v1.0 critical path.

## See also

- [[../libraries/vmic detect]]
- [[../../wiki/04-Virtual-Audio]]
- [[../../../internal/vmic/vmic.go]]
- [[../../../internal/vmic/install.go]]
