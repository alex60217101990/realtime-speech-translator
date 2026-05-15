---
title: Virtual Mic (M4)
date: 2026-05-15
tags:
  - architecture
  - vmic
  - milestone-m4
---

# Virtual Mic (M4)

End-to-end completion: the TTS PCM produced in [[TTS Stage M3b]] is now routed into a virtual audio device that other applications consume as a microphone. The application closes the loop intended by the project's headline feature.

## Detection

[[../../../internal/vmic/vmic.go]] holds a name-pattern table keyed by `runtime.GOOS`. Patterns are substring matches against the device name miniaudio reports. We cover:

| OS | Patterns | Kind |
|---|---|---|
| macOS | `blackhole`, `loopback audio`, `soundflower` | `KindBlackHole`, `KindLoopback`, `KindSoundflower` |
| Linux | `rstranslator`, `null sink`, `null output`, `pw-loopback` | `KindPulseNullSink`, `KindPipeWireLoopback` |
| Windows | `cable input (vb-audio`, `cable input`, `cable-a input`, `voicemeeter input`, `line 1 (virtual audio cable)` | `KindVBCable`, `KindVBCableHiFi`, `KindVoicemeeter`, `KindVAC` |

Patterns are intentionally permissive (substring rather than equality) because vendors version their device names: BlackHole comes in `2ch` and `16ch` variants, VB-CABLE has `A` and `B` siblings, PulseAudio names depend on the sink user-chosen ID.

## Two enumeration entrypoints

```go
// Use inside Session, sharing its miniaudio context:
devs, err := vmic.Detect(s.mctx)

// Use during one-shot startup checks where no Session exists yet:
devs, err := vmic.DetectQuick()
```

`DetectQuick` and `AllQuick` spin a throwaway miniaudio context (~5 ms), enumerate, and tear it down. They are safe to call before [[../../../internal/app/session.go|Session]] is constructed.

## Selection in main()

[[../../../cmd/translator/main.go#pickPlaybackDevice|pickPlaybackDevice]]:

1. If `--output-device <substring>` is set, scan all playback devices (`AllQuick`) for a name match. First hit wins.
2. Otherwise run `DetectQuick` and use the first known virtual mic.
3. If nothing matches, leave `cfg.PlaybackDeviceID = nil` (default output) and return `missingVMic=true`.

The chosen DeviceID feeds [[../../../internal/app/session.go|Session.Config.PlaybackDeviceID]], which the playback `malgo.DeviceConfig` plumbs straight into the device-open call.

## Install wizard

When `missingVMic` is set, [[../../../cmd/translator/main.go#showVMicWizard|showVMicWizard]] runs a modal a tick after the main window appears (we wait 150 ms so the modal has a parent to attach to). The dialog renders the `vmic.GuideFor()` steps for the current OS plus two buttons:

| Button | Behaviour |
|---|---|
| **Continue (preview only)** | Dismiss; Session runs against system default output |
| **Open install page** | Opens the upstream URL in the system browser via `app.OpenURL` |

On Linux there is a follow-up confirm dialog offering to run `vmic.AutoInstall` — that path shells out to `pactl load-module module-null-sink sink_name=rstranslator_out`. After success the user must restart the app to pick up the new device (miniaudio caches the device list at context init).

## Linux runtime install

[[../../../internal/vmic/install.go#AutoInstall|vmic.AutoInstall]]:

1. Check `runtime.GOOS == "linux"`; refuse otherwise.
2. `exec.LookPath("pactl")` — bail with a helpful error if PulseAudio/PipeWire-pulse is not installed.
3. `pactl load-module module-null-sink sink_name=rstranslator_out sink_properties=device.description=RST_Translator`.
4. `vmic.Unload` cleans up the module on app exit (or before reloading); it greps `pactl list short modules` for the sink name and `pactl unload-module <id>` matching rows.

We deliberately do **not** call `Unload` on session.Stop because the sink should persist while the user finishes their call. Cleanup is offered as a settings action in M5.

## Failure modes and degradation

| Symptom | What happens |
|---|---|
| No virtual mic, user dismisses wizard | Session opens default output; UI shows `VMic: not found`; preview-mode works |
| `pactl` missing on Linux | `AutoInstall` returns a typed error rendered through `dialog.ShowError` |
| BlackHole installed but not loaded in CoreAudio | miniaudio still enumerates it; we use it. macOS does not require a separate "load" step. |
| VB-CABLE installed without reboot | Device often missing until the user reboots; we report `missingVMic=true` and the wizard reminds them of the reboot step |

## Tests

[[../../../internal/vmic/vmic_test.go]] covers:

- Pattern matching for every supported OS (positive + negative cases).
- Install-guide invariants (non-empty title, steps, URL where applicable).
- String helpers (`splitLines`, `splitFields`, `contains`) for the `Unload` parser.

No tests exercise `Detect` against a real miniaudio context — that requires hardware and is left to manual QA per [[../../wiki/09-Testing]].

## See also

- [[../decisions/ADR-006 Virtual Mic Guided Install]]
- [[../../wiki/04-Virtual-Audio]]
- [[TTS Stage M3b]]
