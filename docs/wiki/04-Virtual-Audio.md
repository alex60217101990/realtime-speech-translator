# 04 — Виртуальный микрофон

Чтобы перевод воспринимался другими приложениями (Zoom, Discord, Meet, Teams, OBS) как обычный микрофонный вход, мы пишем PCM в виртуальное аудио-устройство. На каждой ОС оно реализуется по-своему.

## macOS — BlackHole 2ch

- **Откуда**: <https://github.com/ExistentialAudio/BlackHole>
- **Лицензия**: GPL-3
- **Установка**:
  - `brew install --cask blackhole-2ch` (рекомендуем в UI guide)
  - либо `.pkg` с GitHub releases
- **Detect**: через CoreAudio `kAudioHardwarePropertyDevices` ищем устройство, имя содержит `BlackHole`. Реализация в `internal/vmic/detect/macos.go` через cgo CoreAudio.
- **Маршрутизация**: после установки в System Settings → Sound → Output **не нужно** менять — мы пишем напрямую в device by name.
- **Чтобы пользователь слышал перевод**: создаём в Audio MIDI Setup «Multi-Output Device» из BlackHole + Headphones; либо включаем monitoring в нашем приложении (отдельный playback на default speakers/headphones).

## Linux — PulseAudio / PipeWire

- **PulseAudio**: загружаем модуль `module-null-sink`.
  ```bash
  pactl load-module module-null-sink \
    sink_name=rstranslator_out \
    sink_properties=device.description="RST_Translator"
  ```
  Затем в Zoom выбираем `Monitor of rstranslator_out` как input.
- **PipeWire** (актуальные дистры): то же `pactl` работает поверх pipewire-pulse, плюс есть нативный `pw-loopback`.
- **Detect**:
  ```bash
  pactl list short sinks | grep rstranslator_out
  ```
  Если нет — приложение само вызывает `pactl load-module ...` (через `os/exec`). Модуль выгружается при выходе или сохраняется persist между запусками — это опция в settings.
- **Реализация**: `internal/vmic/install/linux.go` — обёртка над `pactl`. Никаких прав root не требуется.

## Windows — VB-CABLE

- **Откуда**: <https://vb-audio.com/Cable/>
- **Лицензия**: freeware (donationware), распространение требует разрешения автора.
- **Установка**: пользователь скачивает `.zip`, разворачивает, запускает `VBCABLE_Setup_x64.exe` как админ. После установки в системе появляются устройства `CABLE Input (VB-Audio Virtual Cable)` (output) и `CABLE Output ...` (input).
- **Detect**: через WASAPI `IMMDeviceEnumerator::EnumAudioEndpoints` ищем по имени `CABLE Input`. Реализация в `internal/vmic/detect/windows.go` через cgo windows-headers либо через `github.com/moutend/go-wca`.
- **Чтобы слышать перевод**: либо включить наш monitoring, либо в Windows Sound settings включить "Listen to this device" для `CABLE Output`.

## UI flow «virtual mic не найден»

`internal/vmic/install/wizard.go` отображает экран с инструкцией под текущую ОС:

1. **Заголовок**: «Virtual microphone is required for routing translated audio to other apps.»
2. **Кнопки**:
   - `Install (open browser/installer)` — открывает URL/запускает brew/pactl
   - `Re-check` — повторная детекция (polling каждые 2s)
   - `Continue without virtual mic` — режим preview-only: перевод слышен только в локальных динамиках
3. **Скриншот** инструкции для каждой ОС (PNG в `internal/ui/assets/vmic/`).

## Безопасность маршрутизации

- Запись в virtual mic device **не** требует elevated прав на любой из ОС — это обычный output device.
- Установка драйверов — требует прав (kext signing на macOS до 10.15; WDM-driver на Windows). Поэтому мы не упаковываем драйверы, а делегируем установку пользователю.

## Альтернативы (на будущее)

- **macOS**: Loopback (платный, $99), Soundflower (deprecated), Background Music (открытый, но менее популярный).
- **Linux**: PipeWire native modules.
- **Windows**: VAC (Virtual Audio Cable — платный, $30), Voicemeeter (бесплатный, но overkill).

Поддержка добавляется в `detect` как fallback по device name patterns.
