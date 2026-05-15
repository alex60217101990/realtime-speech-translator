---
title: Beta Polish (M7)
date: 2026-05-15
tags:
  - architecture
  - operability
  - milestone-m7
---

# Beta Polish (M7)

M7 turns the working translator into something users can run unattended for an hour without anyone losing data when it crashes. Three pillars: **structured logging**, **panic capture**, **degraded-device warning**.

## Structured logging

[[../../../internal/logging/logging.go]] sets up `log/slog`:

- File handler — JSON, level `Debug`, written to `<data>/translator.log`.
- Stderr handler — text, level controlled by `LOG_LEVEL` env or the Options.
- Multi-handler fans every record to both; each downstream handler filters by its own level.

File rotation is **size-triggered, one backup**:

```
translator.log         active
translator.log.1       previous (rotated when active hits 5 MB)
```

We bypass `lumberjack` and the like — the desktop app's log volume is modest, the value of a streaming rotator does not justify the dep.

`Init` returns a `func() error` that closes the file. `main()` defers it; `log/slog` is otherwise the only public surface — everywhere else in the code calls `slog.Info(...)` directly.

## Panic capture

[[../../../internal/crashreport/crashreport.go]]:

- `crashreport.Recover(version, state, onSave)` — deferred in `main()` as the outermost recover. It writes a `.txt` report with build, timestamp, optional state, full `debug.Stack()` to `<data>/crash-reports/<timestamp>.txt`, then **re-panics** so the runtime's normal exit behaviour kicks in.
- `crashreport.Prune(N)` — called at startup to cap the report dir at 20 newest files. Stops disks from filling on a panic-loop machine.
- `crashreport.List()` — for the UI's "Reveal crash reports" affordance (wired in M8).

Re-throwing the panic is deliberate: we want the process to exit non-zero so the OS / supervisor / packager log captures the failure, and so users see the macOS / Windows native "App crashed" toast. Swallowing would hide real bugs.

The state callback is itself wrapped in a recover so a racy session-pointer access cannot tank the report write.

## Bluetooth HFP detection

[[../../../internal/audio/capture/capture.go|capture.Source.Health]] returns a `HealthReport` with the device's actual negotiated sample rate, channel count and format. When a BT headset's mic is opened the OS forces HFP/SCO mode (8 or 16 kHz mono) — playback quality on the same device drops with it. We surface this to the user:

- **Log**: `slog.Warn("capture device negotiated low-rate mono — Bluetooth HFP suspected", …)`.
- **UI**: when `HFPSuspect` is true the Main tab gets a wrapped yellow warning under the header: `"⚠ Bluetooth headset is in HFP mode (mic forces N Hz mono SCO). Speech quality may drop. Use a wired mic for best results."`.

Heuristic: `Channels == 1 && InternalRate ∈ {8000, 16000} && InternalRate < RequestedRate`. False positives are possible (a legitimate 16 kHz built-in mic on an old laptop), but the user's response — try a different mic — is harmless.

## Linker tag for `version`

`cmd/translator/main.go` declares `var version = "0.0.0"`. The package scripts pass `-ldflags="-X main.version=$VERSION"`, so release builds carry the tag inside the binary. `crashreport.Save` writes it into every report; `slog` includes it at startup. Local `make build` keeps the default `0.0.0`.

## Privacy posture

- **No telemetry. Period.** Logs and crash reports are local files. The user explicitly hits "Reveal in Finder/Explorer" if they want to share one.
- The downloader points at pinned URLs (Hugging Face + GitHub releases) — no analytics beacons, no fingerprinting.
- See README "Privacy" section + [[../decisions/ADR-001 Desktop Only]].

## Tests

- [[../../../internal/logging/logging_test.go]] — Init writes JSON, rotation kicks in past `MaxBytes`, missing source file is OK, LOG_LEVEL env is read.
- [[../../../internal/crashreport/crashreport_test.go]] — Save persists, List returns chronological order, Prune trims, Recover rethrows after writing.

## What is intentionally not in M7

- Upload step for crash reports. Manual reveal-in-file-manager only for v1.0.
- macOS native crash reporter integration (Crashpad / Sentry). Out of scope.
- Audio dropout monitor / underrun spike detection. Counters exist on Session, surfaced in UI; no automated regression alert yet.
- Multi-language UI (en/ru). Settings labels English-only.

## See also

- [[../../../README.md]] — user-facing entry point
- [[../../../LICENSE]]
- [[../../wiki/07-Performance]]
- [[../../wiki/09-Testing]]
