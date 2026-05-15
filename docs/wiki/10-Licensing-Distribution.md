# 10 — Лицензирование и распространение

## Сводка лицензий зависимостей

| Компонент | Лицензия | Можно ли bundle в проект | Коммерческое использование |
|---|---|---|---|
| whisper.cpp (код) | MIT | да | да |
| Whisper модели (OpenAI) | MIT | да | да |
| CTranslate2 (код) | MIT | да | да |
| NLLB-200 (модель Meta) | **CC-BY-NC-4.0** | да (с атрибуцией) | **нет — только non-commercial** |
| Piper (код) | MIT | да | да |
| Piper voices | разные (часто MIT/CC0/CC-BY) | проверять каждый | обычно да |
| Fyne | BSD-3 | да | да |
| malgo (miniaudio) | MIT0 | да | да |
| go-webrtcvad | MIT | да | да |
| BlackHole driver | **GPL-3** | **нельзя** в проприетарном продукте | guided install допустимо |
| VB-CABLE driver | freeware, restricted distribution | **нельзя** bundle без разрешения автора | guided install допустимо |
| PulseAudio module-null-sink | LGPL-2.1+ | предустановлено в OS | да |

## ⚠ NLLB-200 CC-BY-NC

Эта модель **запрещает коммерческое использование**. Это самая большая licensing-проблема проекта.

### Варианты решения

| Подход | Плюсы | Минусы |
|---|---|---|
| Оставить NLLB, проект **non-commercial only** | Best quality, 200 языков | Нельзя продавать; в README жирным «non-commercial use only» |
| Заменить на **OPUS-MT** (Apache-2.0, Helsinki-NLP) | Коммерчески свободно | Меньше языков на пару, ниже качество, нужны отдельные модели на пару |
| Заменить на **M2M-100** (MIT, Meta) 418M | MIT, 100 языков | Хуже NLLB по BLEU, больше RAM на похожем размере |
| Дать **выбор пользователю** в Settings | Гибкость | Сложнее в коде, два набора моделей в manifest |

**Рекомендация**: для open-source non-commercial — NLLB default; добавить OPUS-MT как alternative в Settings.

## Лицензия самого проекта

Кандидаты:

| Лицензия | Совместимость с NLLB-CC-BY-NC | Защита от закрытых форков |
|---|---|---|
| MIT | ок | нет |
| Apache-2.0 | ок | нет, но patent grant |
| **AGPL-3** | ок | сильная (network use copyleft) |
| GPL-3 | ок | сильная |

Рекомендация: **Apache-2.0** при намерении open-source без копилефта; **AGPL-3** при желании защитить от proprietary форков. Окончательно — на усмотрение автора.

## Привязка драйверов: guided install — почему

- BlackHole (GPL-3) — bundle с проприетарным/Apache app смешивает лицензии; GPL заставит весь проект быть GPL.
- VB-CABLE — автор требует индивидуальное письменное разрешение для редистрибуции.

Решение: показываем модальное окно «Please install <driver> from <url>» и кнопку открыть браузер/installer. Юридически чисто.

Альтернатива (post v1.0): написать собственный driver под каждую OS (HAL plugin macOS, ALSA module Linux, WDM/WASAPI Windows). Это +3–6 месяцев работы и стоимость code signing certificates.

## Telemetry

**Нет**. Никакой телеметрии, никакой отправки данных. Это указано в Setup wizard.

Crash reporter (фаза M7) — **opt-in**, локально на диск; upload только при явном «Send crash report» с UI.

## Распространение

| Канал | OS | Замечания |
|---|---|---|
| GitHub Releases | все | основной |
| Homebrew tap | macOS | `brew install --cask alex.../rst` |
| AUR / Flatpak / Snap | Linux | сообщество |
| WinGet | Windows | manifest подаётся в microsoft/winget-pkgs |

**App Store / Mac App Store** — не подходит из-за sandbox-ограничений (доступ к virtual mic заблокирован).

## Атрибуция (в About/Help screen)

Список используемых open-source компонентов с их лицензиями. Авто-генерируется из `go.mod` через `github.com/google/go-licenses`:

```bash
go-licenses csv ./... > NOTICE.csv
```

В UI — простой scrollable list.

## Privacy Policy (mini)

В Settings → Privacy:

> This application processes all audio locally on your device.
> No audio, transcripts, or translations are sent over the network
> except when downloading models on first run.
> Model downloads come from official sources (Hugging Face, GitHub releases).
> No telemetry or analytics are collected.

## Чек-лист перед первым публичным релизом

- [ ] Выбрана лицензия проекта (`LICENSE` файл)
- [ ] `NOTICE` файл с атрибуцией всех зависимостей
- [ ] Решение по NLLB (commercial/non-commercial badge в README)
- [ ] DCO/CLA политика для contributors
- [ ] Privacy notice виден при первом запуске
- [ ] Code signing certificates: Apple Developer ID, Windows EV или OV cert
