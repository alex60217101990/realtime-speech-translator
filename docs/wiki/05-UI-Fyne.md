# 05 — UI на Fyne v2

## Почему Fyne

| Критерий | Fyne v2 | Wails v2 | Flutter |
|---|---|---|---|
| Pure Go | да | нет (TS/JS) | нет (Dart) |
| macOS/Linux/Windows | да | да | да |
| Webview footprint | нет (~30MB binary) | да (~80–150MB) | да (~50MB) |
| Native look | средне | хорошо | хорошо |
| Сложность сборки | низкая | средняя | высокая |

Wails и Flutter дают красивее UI, но Fyne побеждает по footprint и идеологии «максимум Go».

## Экраны

### 1. Main

```
┌─────────────────────────────────────────┐
│  Realtime Speech Translator             │
├─────────────────────────────────────────┤
│                                         │
│   Source: [Русский     ▼]               │
│   Target: [English     ▼]               │
│                                         │
│   ┌─────────────────────────────────┐   │
│   │         ●  Start                │   │
│   └─────────────────────────────────┘   │
│                                         │
│   Status: Listening                     │
│   VU:    ▮▮▮▮▮▮▮░░░░░░░░░░             │
│                                         │
│   ─ Transcript ───────────────────────  │
│   привет, как дела сегодня              │
│                                         │
│   ─ Translation ──────────────────────  │
│   hi, how are you today                 │
│                                         │
│   [⚙ Settings]  [📦 Models]            │
└─────────────────────────────────────────┘
```

### 2. Settings

- Whisper model: tiny / base / **small** / medium
- TTS voice (per target language): dropdown из установленных
- Input device: auto / [список]
- Output device (virtual mic): [список с приоритетом BlackHole/CABLE/null-sink]
- Monitoring: on/off, monitoring device dropdown
- VAD aggressiveness: 0–3 slider
- Threads: auto / 1 / 2 / 4 / 8
- Idle unload after: 1m / 5m / 15m / never

### 3. Models Manager

Список модели с состояниями: ✓ installed / ⬇ available / ↻ downloading. Кнопки `Download` / `Update` / `Delete`. Внизу — disk usage progress.

### 4. Setup Wizard (первый запуск)

Шаги:
1. Welcome + privacy ("all local")
2. Virtual mic check → install guide если нет
3. Default models download (Whisper-small + NLLB + Piper-en + Piper-ru) — пользователь может пропустить
4. Test session: «Скажите что-нибудь» → показывает transcript + translation + воспроизводит TTS

## Биндинги

Fyne `binding` package:

```go
type uiState struct {
    state       binding.Int          // Idle/Loading/Listening/Speaking/Error
    statusText  binding.String       // "Listening" / "Translating..."
    transcript  binding.String       // last partial
    translation binding.String       // last final
    vuLevel     binding.Float        // 0..1
    srcLang     binding.String
    dstLang     binding.String
}
```

Session при изменениях вызывает `binding.String.Set(...)` из своих goroutine; Fyne сам гарантирует диспатч на main thread.

## Тема

- Light + Dark, по системной настройке.
- Кастомная палитра в `internal/ui/theme.go` — нейтральные тона, акцент-цвет под Start (зелёный) / Stop (красный).

## Локализация UI

`internal/ui/i18n/` — `en.json`, `ru.json`. Загрузка через `embed.FS`. Минимум: меню, статусы, кнопки. Текст transcript/translation, естественно, на выбранных языках STT/MT.

## Доступность

- Все controls имеют labels (для screen readers).
- Hotkey `Ctrl/Cmd+Space` — toggle Start/Stop (опционально, фаза M5).

## Зависимости

```go
require (
    fyne.io/fyne/v2 v2.6.0
    fyne.io/x/fyne v0.0.0-...  // дополнительные виджеты (VU meter)
)
```
