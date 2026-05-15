---
title: UI Polish & Multi-Language (M5)
date: 2026-05-15
tags:
  - architecture
  - ui
  - milestone-m5
---

# UI Polish & Multi-Language (M5)

M5 turns the demonstration UI from M3b into something users can actually live in: persistent settings, downloadable models, language coverage well past the demo defaults, hotkey, themes.

## Three tabs

The window is now a [`container.AppTabs`](https://docs.fyne.io/api/v2.7/container/apptabs.html):

```
┌─ Main ─────────────────────────────────────────────────────┐
│ Source/Target/MT/TTS/VMic header                           │
│ Status, Stats line                                         │
│ [ Start | Stop ]                                           │
│ ─────────── Transcript ───────────  Translation ─────────  │
│  …live updates…                     …live updates…         │
├─ Models ───────────────────────────────────────────────────┤
│ Per-row: name, status (installed/missing/MB), [Download]   │
│ Refresh + total disk usage                                 │
├─ Settings ─────────────────────────────────────────────────┤
│ Form bound to internal/config.Settings; theme is live      │
│ Save -> writes YAML, restarts required for model swaps     │
└────────────────────────────────────────────────────────────┘
```

## Persistent settings

[[../../../internal/config/config.go]] reads / writes `config.yaml`:

- Linux: `${XDG_CONFIG_HOME:-~/.config}/realtime-speech-translator/config.yaml`
- macOS: `~/Library/Application Support/realtime-speech-translator/config.yaml`
- Windows: `%LOCALAPPDATA%\realtime-speech-translator\config.yaml`

YAML unmarshalling tolerates unknown fields, so adding a setting later does not break older config files. Saved with atomic rename (`os.WriteFile(tmp) -> os.Rename`).

CLI flags still override saved values for the current launch — useful for debugging without flipping persistent state.

## Path resolver

[[../../../internal/models/paths/paths.go]] centralises every "where does this model file live" decision. Concrete entrypoints:

- `paths.Data()` / `paths.Config()` — XDG-aware base directories.
- `paths.Models()` — base under Data, auto-mkdir.
- `paths.Whisper(name) -> .../whisper/ggml-<name>.bin`
- `paths.MTDir(name) -> .../mt/<name>/`
- `paths.TTSVoice(name)` + `paths.TTSVoiceJSON(name)`
- `paths.Exists(p)` / `paths.FileSize(p)` for the Models Manager UI.

## Models Manager

[[../../../internal/ui/models_screen.go]] reads the embedded manifest, builds a row per (Whisper / MT / TTS) entry, and surfaces:

- Status label: `missing (N MB)` or `installed (M MB)`.
- Progress bar that switches between determinate (`Content-Length` known) and indeterminate.
- Download button that fires `internal/models/downloader.Fetch` in a goroutine.

For TTS the row downloads two files (the `.onnx` model and its `.onnx.json` sidecar) sequentially — Piper rejects a model whose sidecar is missing, see [[../libraries/Piper TTS#Voice files]].

Re-download is supported; `delete` is deferred to M6 packaging since it interacts with active-session model handles.

## Settings form

[[../../../internal/ui/settings_screen.go]] is a flat `widget.Form` with hand-bound entries:

- Source / Target language: free-text Entry (validation belongs in the Pipeline, not the UI).
- Whisper model: dropdown over `tiny|base|small|medium|large`.
- MT backend: dropdown over `madlad|opusmt|off`.
- Threads: numeric Entry (0 = auto).
- VAD aggressiveness: slider 0..3 with live label.
- Output device: Entry (substring matched against vmic on next launch).
- Piper TTS: checkbox + binary path override.
- Theme: live-applied via `ui.ApplyTheme`.

`SettingsCallbacks.OnThemeChange` flips the Fyne theme immediately; `OnSave` is left to the host to decide whether to prompt for restart.

## Hotkey

```go
hotkey := &desktop.CustomShortcut{
    KeyName:  fyne.KeySpace,
    Modifier: fyne.KeyModifierShortcutDefault, // Cmd on macOS, Ctrl elsewhere
}
w.Canvas().AddShortcut(hotkey, func(_ fyne.Shortcut) { toggleSession() })
```

`KeyModifierShortcutDefault` is the platform-correct meta key. Hotkey works only while the window has focus — system-wide capture would need OS-specific helpers (CGEventTap on macOS, RegisterHotKey on Windows) and is out of scope for v1.0.

## Theme

`ui.ApplyTheme(a, name)` switches between `theme.LightTheme()`, `theme.DarkTheme()`, and `theme.DefaultTheme()` (system follow). The setting is stored in config; live change does not require restart.

## Languages

Manifest TTS entries grew to **13 voices** (en, ru, es, de, fr, pt, it, zh, uk, pl, nl, tr, ja). The matching MADLAD tag table in [[../../../internal/mt/madlad.go]] already covers all of these and more. Adding a voice is a manifest-only change; no code touched.

## Restart-required matrix

| Setting | Live? | Reason |
|---|---|---|
| Theme | yes | Fyne API call |
| Source / Target / Whisper / MT | no | Session pipeline pins the backend at New() |
| VAD aggressiveness | no | Segmenter is constructed once |
| Output device | no | malgo opens the device at startup |
| TTS toggle | no | Pipeline wiring decided at session New() |
| Piper binary path | no | Engine resolves PATH at New() |

UI surfaces "Saved. Some changes require restart." in the status line. M6 will add explicit "Restart session" without quitting the app.

## What is intentionally not in M5

- Full UI localisation (en/ru). Settings labels currently English-only; i18n table belongs in M6/Beta polish.
- Per-language voice picker UI. Single fallback voice per target works fine for v1.0.
- System-wide global hotkey.
- Crash reporter (M7).

## See also

- [[../../../cmd/translator/main.go]] — tabs + hotkey + theme wiring
- [[../../../internal/ui/]] — Models + Settings screens
- [[../../../internal/config/]] — persistence
- [[../../../internal/models/paths/]] — filesystem layout
- [[../../wiki/05-UI-Fyne]]
- [[../../wiki/08-Roadmap#M5]]
