# ui-wails — Wails v3 frontend (in progress)

Target shell for the rewrite described in
[docs/UI_REWRITE_PLAN.md](../docs/UI_REWRITE_PLAN.md). This directory
will hold the Wails v3 project (Go backend shim + React + Three.js
frontend) that replaces the Fyne shell at
[cmd/translator](../cmd/translator) once the migration is complete.

The Go engines (`internal/{stt,tts,mt,audio,app}`) stay untouched —
the Wails app imports `internal/uihost` for the IPC contract and
`internal/app` for the live `Session`.

## Layout (target, when scaffold completes)

```
ui-wails/
├── README.md           # this file
├── wails.json          # wails3 project manifest
├── main.go             # Wails v3 entry — embeds Session + UIHost
├── app.go              # Go-side bindings exposed to JS
└── frontend/
    ├── package.json
    ├── vite.config.ts
    ├── tailwind.config.cjs
    ├── index.html
    ├── src/
    │   ├── main.tsx
    │   ├── App.tsx          # tab router (Live / Models / Settings)
    │   ├── live/
    │   │   ├── LiveTab.tsx  # the screenshot — header, sphere,
    │   │   │                # transcript, translation, log pane
    │   │   ├── Sphere.tsx   # react-three-fiber audio-reactive
    │   │   │                # mesh; binds to AudioBucket stream
    │   │   ├── TranscriptPane.tsx
    │   │   ├── TranslationPane.tsx
    │   │   ├── LogPane.tsx  # rolling structured-log tail
    │   │   ├── StatusBar.tsx
    │   │   └── shaders/
    │   │       ├── vertex.glsl
    │   │       └── fragment.glsl
    │   ├── models/
    │   │   └── ModelsTab.tsx  # catalog + install + progress
    │   ├── settings/
    │   │   └── SettingsTab.tsx
    │   ├── bridge/
    │   │   ├── events.ts      # JSON Event subscription helper
    │   │   ├── stats.ts       # 4 Hz Stats poll helper
    │   │   └── audio.ts       # binary AudioBucket subscription
    │   └── styles/
    │       ├── globals.css    # Tailwind base + design tokens
    │       └── theme.ts       # color tokens matching the mock
```

## Design reference

`docs/ui-mock-2026-05.png` (or whatever the user pasted into the
chat where this rewrite was approved): a dark glassmorphic shell
with a left "Русский" transcript card stack, an animated entropy
sphere in the centre, a right "English" translation card stack,
mic / language switch / settings controls along the bottom edge,
and a thin status / quality strip at the very bottom.

The screenshot is one source of truth for typography weights,
corner radii, glow colours and the chat-bubble shadow stack.
Match the radii (~20 px on cards, ~14 px on input pills), the
backdrop-blur (~12 px), and the rim-lit fresnel on the sphere.

## What is already in place (this commit)

- **`internal/uihost`** — wire types and AudioBucket binary frame.
- **`internal/uihost.NewSpectrum`** — bass/mid/treble buckets the
  sphere shader subscribes to.
- This directory's README + the empty scaffolding above is the
  next thing to land.

## What is NOT in place yet (next sessions)

Per `docs/UI_REWRITE_PLAN.md`:

  - Stage 1 — `wails3 init`, app.go shim exposing Events/Stats/
              AudioBuckets/Catalog/Install bindings, frontend
              boilerplate (Vite + React + TS + Tailwind).
  - Stage 2 — chrome (header, language flags, transcript +
              translation card stacks, status bar, mic / language
              switch / settings buttons). NO sphere yet, NO audio
              binding yet — pure visual match against the mock
              using static data.
  - Stage 3 — three.js audio-reactive sphere, GLSL shader, binary
              AudioBucket subscription, FPS lock at 60.
  - Stage 4 — Models tab in the new style (download progress bars
              shaped like the mock cards; one-click install →
              auto-config update via the existing
              `internal/models/installer.go` + the auto-MTBackend
              hook).
  - Stage 5 — Settings tab matching the mock styling.
  - Stage 6 — codesign / notarise / package per OS (`wails3 build`
              already does most of this; we just keep the existing
              scripts/package-*.sh as fallbacks).

## Bootstrap (when the implementer is ready)

```bash
# one-off, per machine
go install github.com/wailsapp/wails/v3/cmd/wails3@latest
brew install node                     # macOS — or system package manager

# from the repo root
cd ui-wails
wails3 init -n rsts-ui -t react-ts -d .
npm install
npm install three @react-three/fiber @react-three/drei zustand
npm install -D tailwindcss postcss autoprefixer @types/three
npx tailwindcss init -p

# then point app.go at internal/app.Session via go.work or a relative
# import, and start filling in src/live/ + src/bridge/ per the layout
# above.

wails3 dev   # live-reload dev shell
wails3 build # release bundle for the current host
```

The translator binary at `cmd/translator` keeps working unchanged
throughout — the Wails app lands as a parallel binary
(`cmd/translator-ui` or `ui-wails/build/bin/rsts-ui`) and the Fyne
shell stays as the fallback until parity is reached.

## Why two tracks

The plan is explicitly two-track (see §4 of `UI_REWRITE_PLAN.md`):

  - Track A — **Wails v3 + React + Three.js**, fast path to the
              reference mock (~2 weeks).
  - Track B — **GLFW + OpenGL + Skia-go**, deferred, only spun up
              if Track A blocks on something fundamental
              (~2 months).

Both consume the same `internal/uihost` contract, so a swap later
is a UI-only change.
