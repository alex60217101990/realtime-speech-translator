// Command translator-ui is the Ebiten-based native UI for the
// realtime speech translator.
//
// Built on github.com/hajimehoshi/ebiten/v2 + Kage shaders; ships
// as a single binary with ~30–50 MB RAM at steady state. The
// engines (internal/{stt,tts,mt,audio,app}) stay untouched; this
// binary imports them through internal/sessionbuild + internal/uiebt
// and pipes the live Session.Events / Session.AudioBuckets stream
// onto the App widgets.
//
// The Fyne shell at cmd/translator stays alive as the fallback
// until parity is reached.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	rstapp "github.com/alex60217101990/realtime-speech-translator/internal/app"
	"github.com/alex60217101990/realtime-speech-translator/internal/config"
	"github.com/alex60217101990/realtime-speech-translator/internal/logging"
	"github.com/alex60217101990/realtime-speech-translator/internal/sessionbuild"
	"github.com/alex60217101990/realtime-speech-translator/internal/uiebt"
	"github.com/alex60217101990/realtime-speech-translator/internal/uihost"
)

func main() {
	closeLog, err := logging.Init(logging.Options{})
	if err != nil {
		fmt.Fprintln(os.Stderr, "log init:", err)
	} else {
		defer closeLog()
	}
	slog.Info("translator-ui starting", "stack", "ebiten")

	cfg, err := config.Load()
	if err != nil {
		slog.Warn("config load failed; using defaults", "err", err)
		cfg = config.Default()
	}

	app := uiebt.NewApp()
	applyLanguagesToApp(app, cfg)
	app.AttachLogSink(loggingLogSink{})

	ctx, cancel := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Wire the Models tab. The catalog is static, but installed
	// state is filesystem-derived so we snapshot it now + refresh
	// after every successful install via SetModelInstalled.
	catalog := newCatalogAdapter(app)
	app.SetCatalog(catalog.toUIEntries(), catalog.installedSnapshot(), catalog, ctx)

	// Shared handle the mic-button + reload loop both poke at.
	// reload chan triggers the session-rebuild loop after Settings
	// save so MT/TTS/STT/lang switches actually take effect.
	sess := &sessionHandle{reload: make(chan struct{}, 1)}
	app.SetMicHandler(func(on bool) {
		sess.SetActive(on)
	})

	// Settings: Save persists config + signals reload. The session
	// loop reloads from disk so the next cycle picks up the change.
	app.SetSettings(settingsSnapshotFromCfg(cfg), buildSettingsSaver(cfg, sess))

	// Session loop in background — boots immediately, rebuilds on
	// every reload signal until ctx is cancelled.
	go runSessionLoop(ctx, app, sess)

	if err := uiebt.RunApp(ctx, app); err != nil {
		slog.Error("ui exited with error", "err", err)
		os.Exit(1)
	}
}

// applyLanguagesToApp pushes the configured source / target
// languages onto the App so the side-pane flag pills render the
// right label.
func applyLanguagesToApp(app *uiebt.App, cfg config.Settings) {
	src := strings.ToLower(cfg.SourceLang)
	tgt := strings.ToLower(cfg.TargetLang)
	app.SetLanguages(src, prettyLang(src), tgt, prettyLang(tgt))
}

// sessionHandle is the shared box that the mic-button click
// handler + Settings-save handler + the session loop all poke at.
// session  — currently-running Session (nil before first build).
// reload   — buffered chan, send on it to ask the loop to tear
//            down the current Session and rebuild from the latest
//            on-disk config.
type sessionHandle struct {
	mu      sync.Mutex
	session *rstapp.Session
	reload  chan struct{}
}

func (h *sessionHandle) set(s *rstapp.Session) {
	h.mu.Lock()
	h.session = s
	h.mu.Unlock()
}

// Reload signals the session loop to rebuild on the next cycle.
// Non-blocking: a pending reload is coalesced with this one.
func (h *sessionHandle) Reload() {
	select {
	case h.reload <- struct{}{}:
	default:
	}
}

// SetActive starts or pauses mic capture based on the toggle. No-op
// when the session is not yet built (early click before models
// finish loading) — the mic button still flips visually so the user
// sees the click registered.
func (h *sessionHandle) SetActive(on bool) {
	h.mu.Lock()
	s := h.session
	h.mu.Unlock()
	if s == nil {
		return
	}
	var err error
	if on {
		err = s.Resume()
	} else {
		err = s.Pause()
	}
	if err != nil {
		slog.Warn("mic toggle failed", "on", on, "err", err)
	}
}

// runSessionLoop boots the session, then waits for either the
// outer context to be cancelled (program exit) or a reload signal
// (Settings save). On reload it tears the current Session down,
// re-loads the config from disk, and rebuilds. Audio/event pumps
// are scoped per-cycle so they exit cleanly.
func runSessionLoop(ctx context.Context, app *uiebt.App, sess *sessionHandle) {
	cfg, err := config.Load()
	if err != nil {
		slog.Warn("session loop: initial config load failed", "err", err)
		cfg = config.Default()
	}
	for {
		applyLanguagesToApp(app, cfg)
		cycleCtx, cancelCycle := context.WithCancel(ctx)
		res := startSessionCycle(cycleCtx, app, cfg, sess)
		if res == nil {
			cancelCycle()
			// Build failed — wait for ctx.Done OR a reload (user
			// might Save new settings that fix the issue).
			select {
			case <-ctx.Done():
				return
			case <-sess.reload:
				newCfg, _ := config.Load()
				cfg = newCfg
				continue
			}
		}
		select {
		case <-ctx.Done():
			cancelCycle()
			closeCycle(res)
			return
		case <-sess.reload:
			slog.Info("session loop: reloading from config")
			app.SetStatus(uiebt.StatusWaiting)
			sess.set(nil)
			cancelCycle()
			closeCycle(res)
			newCfg, _ := config.Load()
			cfg = newCfg
		}
	}
}

// startSessionCycle builds the session + spins up the audio/event
// pumps. Returns the live Result, or nil on a hard failure (caller
// is expected to surface that to the UI and stall).
func startSessionCycle(ctx context.Context, app *uiebt.App, cfg config.Settings, sess *sessionHandle) *sessionbuild.Result {
	app.SetStatus(uiebt.StatusWaiting)

	res, err := sessionbuild.Build(cfg)
	if err != nil {
		slog.Error("session build failed", "err", err)
		app.SetStatus(uiebt.StatusError)
		app.AppendTranslation("Не удалось загрузить модели — откройте Models tab.",
			time.Now().Format("15:04"))
		return nil
	}
	slog.Info("session built",
		"stt_model", res.STTModel, "stt_kind", res.STTKind)

	bucketsCh := res.Session.EnableAudioBuckets(60)

	if err := res.Session.Start(ctx); err != nil {
		slog.Error("session start failed", "err", err)
		app.SetStatus(uiebt.StatusError)
		_ = res.Session.Close()
		return nil
	}
	sess.set(res.Session)
	app.SetStatus(uiebt.StatusRunning)
	app.SetRecording(true)

	effective := cfg
	effective.MTBackend = res.MTBackend
	effective.STTModel = res.STTModel
	app.SetSettings(settingsSnapshotFromCfg(effective), buildSettingsSaver(effective, sess))

	go pumpEvents(ctx, res, app, cfg)
	go pumpAudio(ctx, bucketsCh, app)
	return res
}

func closeCycle(res *sessionbuild.Result) {
	if res == nil {
		return
	}
	_ = res.Session.Close()
	if res.MTCache != nil {
		_ = res.MTCache.Sync()
	}
}

// pumpAudio converts uihost.AudioBucket frames into the Sphere's
// SphereAudio uniforms and pushes them to the App. ~60 Hz.
//
// Bass/Mid/Treble arrive pre-normalised by the spectrum analyser
// (each tracks its own slow EWMA reference) so they sit in [0,1]
// already. RMS is the raw chunk RMS — typical voice peaks at
// ~0.10-0.20, so we boost it ×4 with a hard clamp to make the
// shader's halo/dust uniforms swing through their full range
// during normal-volume speech.
func pumpAudio(ctx context.Context, ch <-chan uihost.AudioBucket, app *uiebt.App) {
	for {
		select {
		case <-ctx.Done():
			return
		case b, ok := <-ch:
			if !ok {
				return
			}
			rms := b.Rms * 4.0
			if rms > 1 {
				rms = 1
			}
			app.SetAudio(uiebt.SphereAudio{
				Bass:   b.Bass,
				Mid:    b.Mid,
				Treble: b.Treble,
				Rms:    rms,
			})
		}
	}
}

// pumpEvents reads Session.Events and feeds the App's transcript /
// translation panes.
func pumpEvents(ctx context.Context, res *sessionbuild.Result, app *uiebt.App, cfg config.Settings) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-res.Session.Events():
			if !ok {
				return
			}
			switch e := ev.(type) {
			case rstapp.Final:
				app.AppendTranscript(e.Text, time.Now().Format("15:04"))
			case rstapp.Translation:
				app.AppendTranslation(e.Target, time.Now().Format("15:04"))
			case rstapp.Partial:
				// Partials skipped — the screenshot shows finished
				// bubbles only. A future "live transcript" widget
				// inside the bottom strip can pick these up.
				_ = e
			case rstapp.ErrorEv:
				slog.Warn("session error", "err", e.Err)
			}
		}
	}
}

// prettyLang maps an ISO-639-1 code to the human-friendly label
// the pane header displays.
func prettyLang(iso string) string {
	switch iso {
	case "ru":
		return "Русский"
	case "en":
		return "English"
	case "es":
		return "Español"
	case "de":
		return "Deutsch"
	case "fr":
		return "Français"
	case "it":
		return "Italiano"
	case "pt":
		return "Português"
	case "pl":
		return "Polski"
	case "uk":
		return "Українська"
	case "ja":
		return "日本語"
	case "zh":
		return "中文"
	case "auto":
		return "Авто"
	}
	return strings.ToUpper(iso)
}
