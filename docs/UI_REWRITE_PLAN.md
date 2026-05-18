# UI Rewrite Plan — GPU-accelerated, audio-reactive shell

Status: **draft / proposal** · Owner: aleksandr · Target reference
mock: `docs/ui-mock-2026-05.png` (futuristic Russian↔English
translator with a particle-entropy sphere in the center, glassmorphic
side panels, mic / language switch / settings controls).

The rest of this document is the long-form plan. It is the single
source of truth when we come back to actually implement the rewrite
— every choice has a reasoning paragraph and an alternative listed
so the next person (or future self) does not have to redo the
analysis.

---

## 1. Why rewrite the UI

Current state: `internal/ui` is built on Fyne v2. Fyne is:

- Software-rendered text + 2D primitives. Custom shaders are not
  supported. Audio-reactive 3D is impossible without an embedded
  GL viewport, which Fyne deliberately does not expose.
- Layouts are box-model only; the glassmorphic / depth design we
  want (translucent layers, blur, glow halo around the sphere)
  requires per-fragment alpha compositing the current renderer does
  not perform.
- Typography is limited to one font face per app and a small fixed
  type scale. The reference mock uses three sizes + tabular
  numerals + emoji flags — Fyne struggles with all three.

The engines (`internal/stt`, `internal/tts`, `internal/mt`,
`internal/audio/*`, `internal/app`) are **already separated** from
the UI by a clean event channel (`rstapp.Session.Events()`). That
is the entire surface the new UI needs to consume, so the rewrite
is a pure replacement of `cmd/translator/main.go` + `internal/ui/`
— nothing under `internal/{stt,tts,mt,audio,app,config,paths,…}`
needs to move.

## 2. Goal of the rewrite

Match the reference mock at the following budgets on a 2019 Intel
MacBook Pro (the developer's baseline):

| metric                          | budget                  |
|---------------------------------|-------------------------|
| frame rate (idle)               | 60 FPS locked           |
| frame rate (sphere + audio FX)  | ≥ 60 FPS, target 120    |
| frame time variance             | ≤ 2 ms p99              |
| RAM steady-state (UI process)   | ≤ 200 MB                |
| audio-callback → viz lag        | ≤ 2 audio periods (≈ 20 ms) |
| time-to-first-frame (cold)      | ≤ 800 ms                |
| binary size delta over today    | ≤ +25 MB                |
| number of new cgo deps          | ≤ 1                     |

These are non-negotiable. Any candidate stack that cannot hit them
on the baseline machine is out, regardless of how pretty its demos
look on Apple Silicon.

## 3. Candidate stacks evaluated

ChatGPT's recommendation (GLFW + raw OpenGL + Dear ImGui + PortAudio
+ Whisper.cpp) is one of seven I evaluated. The Whisper / PortAudio
parts of that recommendation are irrelevant — we already have
sherpa-onnx + malgo, and they are strictly better for our use case.
The UI half (GLFW + OpenGL + ImGui) is one of three serious
contenders. The recommendation of **Ebiten** is wrong for this
target: Ebiten is a 2D engine, and the sphere in the mock is a
real 3D mesh, not a sprite or radial gradient.

| stack                              | 3D? | shader access | native macOS controls | learning cost | community |
|------------------------------------|-----|---------------|------------------------|----------------|-----------|
| **Fyne v2** (today)                | no  | no            | partial                | low            | large     |
| **Ebiten + Kage**                  | no¹ | yes (Kage)    | no                     | low            | huge      |
| **Gio**                            | no  | yes (custom)  | no                     | medium         | medium    |
| **G3N**                            | yes | yes (GLSL)    | no                     | medium         | small     |
| **GLFW + OpenGL + Dear ImGui**     | yes | yes (GLSL)    | no                     | high           | huge      |
| **GLFW + OpenGL + Nuklear / NanoVG** | yes | yes (GLSL)   | no                     | high           | small     |
| **Wails v2 + WebGL (Three.js)**    | yes | yes (GLSL)    | yes (WKWebView)        | low            | huge      |
| **Raylib-Go**                      | yes | yes (GLSL)    | no                     | low            | medium    |

¹ Ebiten ships a software-rendered fake-3D primitive set; not
suitable for a particle sphere.

### 3.1 Detailed assessment

**Wails v2 + WebGL** — the fast path to the reference mock.
WKWebView on macOS, WebView2 on Windows, WebKitGTK on Linux —
no Chromium bundled. JavaScript / TypeScript bridge to Go via
generated bindings; the engines stay in Go. Three.js + react-three-
fiber make the sphere a 30-line component. RAM ~80–150 MB on macOS
with WKWebView (measured on a comparable shipping app). Frame time
6–8 ms steady. Cold start 400 ms.

  - Pros: shortest path to the mock; CSS makes glassmorphism /
    blur / typography one-liners; we already know React / TS;
    runs unchanged on Windows + Linux from one codebase; auto-
    update infra (Wails handles).
  - Cons: WebView memory ceiling is real (≈ 250 MB on long
    sessions); GC pauses in V8 are visible at 120 FPS though
    invisible at 60; sphere shader is GLSL inside WebGL, no
    Metal directly. Audio bridge is JSON over Wails — fine for
    Stats at 30 Hz, too slow for raw PCM, which is why audio
    stays in Go and only amplitude buckets cross the bridge.

**G3N** — the cleanest 3D-on-Go option. Scene graph + camera +
lights + GLSL material out of the box. Fewer moving parts than
GLFW + raw OpenGL.

  - Pros: Go-native, no JS / TS, no second build pipeline; full
    GLSL for the sphere shader; can import a glTF model; built-in
    GUI module is enough for transcript lists.
  - Cons: small community, fewer working demos to crib from;
    GUI module looks like 2014 (no antialiased fonts beyond
    FreeType bitmap); no native controls; deprecated OpenGL on
    modern macOS (works but Apple has marked it for removal).

**GLFW + OpenGL + Dear ImGui** — ChatGPT's choice. Maximum
performance ceiling, maximum control.

  - Pros: every published "futuristic GPU UI" demo on GitHub uses
    this stack; binding maturity excellent; lowest RAM (~50 MB);
    120 FPS trivially.
  - Cons: ImGui aesthetics are wrong for a consumer app — looks
    like a dev tool. Beautiful typography and glassmorphic panels
    require writing them by hand. Time-to-first-mock at least 3×
    longer than Wails. Two engineers months of work vs two weeks.

**GLFW + OpenGL + custom 2D + Nuklear / NanoVG** — like the
above but with prettier 2D primitives. Same time cost, slightly
better aesthetics out of the box, much smaller community.

**Raylib-Go** — well-loved, simple API, ships with shader hot
reload. 3D + audio + UI in one package.

  - Pros: shortest path to a working 3D sphere of any non-Web
    option (single file, ~80 lines for a textured sphere with
    audio uniform).
  - Cons: raylib's UI primitives (`raygui`) look like a 2000s
    Win32 dialog; we would still write our own UI on top.

**Ebiten + Kage** — only good for the audio meter / spectrum.
Sphere would have to be a baked sprite atlas — not the same thing
as the mock.

## 4. Recommendation

Take a **two-track approach**, with the engines isolated so we can
swap UI implementations without touching them.

### Track A (ship within 1–2 weeks) — Wails v2

1. Hand-craft the mock in React + Three.js + Tailwind.
2. Bridge `rstapp.Session.Events()` to a JS stream via Wails
   generated bindings.
3. Push amplitude / VAD-active / FFT buckets to the renderer via
   the same bridge (binary message over the Wails IPC, not JSON
   per sample — see §6.4 for the protocol).
4. Settings + Models tabs become normal React forms; the
   existing `internal/models/{catalog,installer}` API is exposed
   directly.

Outcome: the reference mock is shipping for users while we still
own the door to swap the UI later. RAM ≈ 150 MB, FPS = 60.

### Track B (optional, deferred) — Go-native GPU UI

If Wails turns out to be insufficient (RAM creep over multi-hour
sessions, audio-callback-to-viz jitter, packaging headaches), then
migrate to:

  **GLFW + OpenGL 4.1 (macOS) / 4.5 (Linux, Windows) + custom
  shader pipeline + Skia-go (for typography) + per-feature
  hand-written widgets.**

That is roughly two engineer-months of work and is only justified
if Track A blocks on something Wails fundamentally cannot do. The
sphere itself ports from Three.js to GLSL almost line-by-line.

Both tracks read the same engine API, so the engines never see
the difference. The tracks share the **Audio bridge protocol** in §6.4.

## 5. Architecture

```
┌────────────────────────────────────────────────────────────────┐
│                        cmd/translator                          │
│ ┌──────────────────────────┐  ┌──────────────────────────────┐ │
│ │  rstapp.Session          │  │  uihost (new package)         │ │
│ │  (already exists)        │  │  ── publishes Events + Stats  │ │
│ │  STT / MT / TTS / audio  │──┤  ── pulls AudioBucket frames  │ │
│ └──────────────────────────┘  │  ── owns the bridge transport │ │
│                                └────────────┬──────────────────┘ │
└──────────────────────────────────────────────┼───────────────────┘
                                               │ Wails bridge
                                               │ (or Track B: in-proc Go)
                                               ▼
                          ┌──────────────────────────────────────┐
                          │ UI process (React+Three.js initially)│
                          │                                      │
                          │ ┌────────────┐  ┌─────────────────┐  │
                          │ │ Mock chrome │  │ Three.js sphere │  │
                          │ │ flags / btn │  │ + audio shader  │  │
                          │ └────────────┘  └─────────────────┘  │
                          └──────────────────────────────────────┘
```

Key design choice: introduce **`internal/uihost`** as a small
Go package whose entire job is to define the IPC contract — event
shapes, stats shape, audio bucket frame shape — and to glue them
to whatever transport (Wails channel today, gRPC tomorrow, raw
WebSocket if we ever need a remote UI). The current `cmd/translator`
imports `internal/uihost` instead of touching `Session` directly,
so we never re-couple the UI back into the engines.

## 6. Engineering plan

### 6.1 Stage 0 — engine API freeze (no UI work)

Goal: make sure the engines never have to be edited again when we
change UI tech.

  - Move the `Event` and `Stats` types from `internal/app` into
    `internal/app/api`, with versioned JSON tags. Add
    `MarshalBinary`/`UnmarshalBinary` for the audio-bucket frame
    (§6.4).
  - Add `internal/app/api/v1` so future breaking changes can
    coexist.
  - Add a `Session.AudioBuckets() <-chan AudioBucket` channel —
    see §6.4 for the bucket shape.
  - Cover the new types with table-driven tests (encode → decode →
    deep-equal).

Estimated effort: **1 day**.

### 6.2 Stage 1 — Wails skeleton

  - `go install github.com/wailsapp/wails/v3/cmd/wails3@latest`
  - `wails3 init -n rsts-ui -t react-ts -d ./ui-wails`
  - Replace the boilerplate `app.go` with a thin shim that holds
    a `*rstapp.Session` and re-exports `Events`, `Stats`,
    `AudioBuckets` plus the catalog and installer.
  - Build the existing engine codebase into the Wails binary via
    `go.work` so we do not vendor twice.

Estimated effort: **1 day**.

### 6.3 Stage 2 — chrome (no sphere yet)

Reproduce the reference mock minus the sphere, using React +
Tailwind + Radix UI for accessible primitives:

  - top bar (title + connection dot + window controls — we keep
    the OS chrome and only style the content).
  - left and right transcript panels with bubble cards
    (`Привет! Как дела?` + timestamp).
  - bottom action row: mic button (large, glowing), language
    switch, settings cog, mic level meter, language quality
    selector.
  - flag chips at the top of each panel with native-flag emoji
    (no SVG download needed).

Wire `Final` events to `transcriptStore` and `Translation` events
to `translationStore`; both are simple Zustand stores. Style with
Tailwind + CSS variables driven by the chosen theme.

Estimated effort: **3–4 days**.

### 6.4 Stage 3 — sphere

Three.js scene with one `IcosahedronGeometry(1, 64)` and a custom
`ShaderMaterial`. The fragment shader does Fresnel rim + HDR glow;
the vertex shader displaces each vertex along its normal by a
Simplex-noise field modulated by audio level.

Audio bridge protocol (`internal/uihost/audio_bridge.go`):

```go
// 32-byte fixed-layout frame, little-endian. Wails sends this as
// raw bytes over runtime.EventsEmit — no JSON, no allocation per
// frame, ~3 KB/s at 60 Hz.
type AudioBucket struct {
    SeqNo      uint32  // monotonic
    Rms        float32 // 0..1
    Peak       float32 // 0..1
    Bass       float32 // 0..1, 20–250 Hz
    Mid        float32 // 0..1, 250–4 000 Hz
    Treble     float32 // 0..1, 4 000–8 000 Hz
    VadActive  uint8   // 0 / 1
    Speaking   uint8   // 0 / 1
    _padding   [6]byte
}
```

FFT lives in Go (mjibson/go-dsp 1024-point real FFT every 16 ms
on a windowed copy of the latest mic buffer). The UI shader takes
RMS for outer-ring breath, Bass for sphere displacement amplitude,
Mid for rotation rate, Treble for glow intensity. The shader is
~80 lines of GLSL.

Estimated effort: **2–3 days**.

### 6.5 Stage 4 — Settings + Models tabs

Re-implement on React with the same Wails-bound catalog /
installer API. Progress bars become a real `<progress>` with CSS
transitions; the Models grid shows tier badges and license info.

Estimated effort: **2 days**.

### 6.6 Stage 5 — packaging + signing

  - `wails3 build -platform darwin/amd64,darwin/arm64,linux/amd64,windows/amd64`
  - macOS notarisation: existing `scripts/package-macos.sh` extended
    to call `codesign` + `notarytool` on the bundle Wails produces.
  - Linux: AppImage via `linuxdeploy`; the WebKitGTK runtime adds
    ~15 MB.
  - Windows: WiX MSI, ships WebView2 bootstrapper (Microsoft hosts
    it; Edge already provides it on every supported Windows build).

Estimated effort: **2–3 days**.

### 6.7 Stage 6 (deferred, Track B) — Go-native GPU UI

If Track A ships and we still want native GPU rendering:

  1. `cmd/translator-gpu` parallel binary; `cmd/translator` stays.
  2. GLFW window, OpenGL 4.1 core profile on macOS / 4.5 on others.
  3. Render pipeline: a thin scene graph (camera, sphere mesh,
     glow post-pass, UI batch). Skia-go (`fogleman/gg` is a smaller
     alternative if we drop subpixel AA) for typography and rounded
     rects. ~3 000 LOC.
  4. Layout: a tiny flexbox-like solver of our own (Yoga is
     overkill for the seven elements we have). ~300 LOC.
  5. Event loop: one render thread that pulls from
     `Session.Events()` + `Session.AudioBuckets()`. Mouse / keyboard
     from GLFW, dispatched to widget tree.
  6. The shader code itself ports from Three.js with no semantic
     change.

Two engineer-months estimate is **deliberately pessimistic** —
the audio + engine integration is already done; the only new
thing is widget rendering.

## 7. Risk matrix

| risk                                                         | likelihood | mitigation |
|--------------------------------------------------------------|------------|------------|
| WKWebView memory creep over multi-hour calls                 | medium     | Restart the renderer process on user-visible Idle (UI is stateless; Stores re-hydrate from Session) |
| V8 GC pauses visible at 120 FPS                              | low at 60  | Render at 60 FPS; the mock does not actually need 120 |
| Wails bridge overhead drops sphere frame rate                | low        | Audio bucket is binary, not JSON; events are batched per RAF |
| Linux distros without WebKitGTK 2.40                         | medium     | Ship a Flatpak in addition to AppImage |
| Windows users without Edge / WebView2 runtime                | low        | Bundle the Evergreen bootstrapper (15 MB) |
| Track B (GLFW) requires a maintainer we do not have          | high       | Only spin it up if Track A blocks on something concrete |

## 8. What this plan deliberately does NOT do

  - **Does not change any engine code.** STT, MT, TTS, audio,
    config, paths, vmic, crashreport, logging remain untouched.
  - **Does not introduce a second runtime** (Electron / Chromium).
    WKWebView / WebView2 / WebKitGTK are the host OS's own renderer
    — they are already installed.
  - **Does not require Python** at any stage.
  - **Does not commit to a UI framework on day one.** Track A's
    React choice is reversible because everything below
    `internal/uihost` stays Go-native.

## 9. Open questions to answer before starting

  1. Do we want native window chrome (the title bar in the mock is
     custom) or OS chrome? Custom looks better but breaks
     accessibility on Linux GTK. **Default: OS chrome, dark theme.**
  2. Do we lock the renderer to 60 FPS, or render-on-demand when
     amplitude crosses a threshold? **Default: 60 FPS always while
     a session is running; idle pause when Status = idle.**
  3. Do we keep the current Fyne build alive as a fallback target?
     **Default: yes, until Track A ships and we have a week of
     real-user telemetry from it.**

## 10. Time budget summary

| stage                                       | days    |
|---------------------------------------------|---------|
| 0  engine API freeze                        | 1       |
| 1  Wails skeleton                           | 1       |
| 2  chrome (no sphere)                       | 3–4     |
| 3  sphere + audio bridge                    | 2–3     |
| 4  Settings + Models tabs                   | 2       |
| 5  packaging + signing                      | 2–3     |
| **Track A total**                           | **11–14** |
| 6  Go-native GPU UI (Track B)               | 40–50   |

Track A is two calendar weeks of focused work. Track B is two
months and only justified if Track A demonstrably fails.

---

## Appendix A — references

  - Wails v3 docs: <https://v3.wails.io/>
  - Three.js shader material:
    <https://threejs.org/docs/#api/en/materials/ShaderMaterial>
  - r3f (react-three-fiber): <https://docs.pmnd.rs/react-three-fiber>
  - G3N tutorial: <https://github.com/g3n/g3n.github.io>
  - Ebiten Kage shader docs:
    <https://ebitengine.org/en/documents/shader.html>
  - go-dsp FFT: <https://github.com/mjibson/go-dsp>

## Appendix B — shader skeleton for the sphere

```glsl
// vertex shader — displace along normal by audio-modulated noise
uniform float u_time;
uniform float u_bass;
varying vec3 v_normal;

float snoise(vec3 p);    // standard Simplex implementation

void main() {
    v_normal = normal;
    float n  = snoise(position * 2.0 + u_time * 0.4);
    vec3  p  = position + normal * (0.05 + 0.25 * u_bass * n);
    gl_Position = projectionMatrix * modelViewMatrix * vec4(p, 1.0);
}

// fragment shader — Fresnel rim + HDR glow
uniform float u_treble;
varying vec3 v_normal;

void main() {
    float fresnel = pow(1.0 - abs(v_normal.z), 3.0);
    vec3 base = mix(vec3(0.20, 0.30, 1.00), vec3(0.85, 0.20, 1.00), fresnel);
    vec3 glow = base * (1.0 + 1.6 * u_treble);
    gl_FragColor = vec4(glow, 1.0);
}
```

That is roughly the entire visual identity of the reference mock,
in 30 lines of GLSL. The same shader is portable verbatim to
Track B (GLFW + OpenGL 4.1) when / if we get there.
