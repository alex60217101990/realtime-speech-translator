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

	// Shared handle the mic-button + bringUpSession both poke at.
	// bringUpSession fills sess.Session once the heavy load is done;
	// before that, mic clicks no-op (button still toggles visually).
	sess := &sessionHandle{}
	app.SetMicHandler(func(on bool) {
		sess.SetActive(on)
	})

	// Try to bring up the session in the background so the UI
	// boots immediately. If models are missing we leave the
	// header on "Waiting for models" and the user can still see
	// the chrome / idle sphere.
	go bringUpSession(ctx, app, cfg, sess)

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

// sessionHandle is the shared box that both bringUpSession (fills
// it once the Session is built) and the mic-button click handler
// (calls Pause/Resume on it) point at. Indirection lets the click
// handler exist before the heavy session-build goroutine finishes.
type sessionHandle struct {
	mu      sync.Mutex
	session *rstapp.Session
}

func (h *sessionHandle) set(s *rstapp.Session) {
	h.mu.Lock()
	h.session = s
	h.mu.Unlock()
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

// bringUpSession runs the heavy work (model load, MT init, capture
// device open) off the UI thread. The Ebiten loop owns the window
// and would otherwise stall during the multi-second sherpa init.
func bringUpSession(ctx context.Context, app *uiebt.App, cfg config.Settings, sess *sessionHandle) {
	app.SetStatus(uiebt.StatusWaiting)

	// Build session.
	res, err := sessionbuild.Build(cfg)
	if err != nil {
		slog.Error("session build failed", "err", err)
		app.SetStatus(uiebt.StatusError)
		app.AppendTranslation("Не удалось загрузить модели — откройте Models tab.",
			time.Now().Format("15:04"))
		return
	}
	slog.Info("session built",
		"stt_model", res.STTModel, "stt_kind", res.STTKind)

	// Subscribe to the audio-bucket stream BEFORE Start so the
	// capture wrapper sees a non-nil chan.
	bucketsCh := res.Session.EnableAudioBuckets(60)

	if err := res.Session.Start(ctx); err != nil {
		slog.Error("session start failed", "err", err)
		app.SetStatus(uiebt.StatusError)
		_ = res.Session.Close()
		return
	}
	sess.set(res.Session)
	app.SetStatus(uiebt.StatusRunning)
	app.SetRecording(true) // session auto-started capture

	// Drain the three streams in dedicated goroutines so neither
	// the events feed nor the audio feed can starve the other.
	go pumpEvents(ctx, res, app, cfg)
	go pumpAudio(ctx, bucketsCh, app)

	<-ctx.Done()
	slog.Info("shutting down session")
	_ = res.Session.Close()
	if res.MTCache != nil {
		_ = res.MTCache.Sync()
	}
}

// pumpAudio converts uihost.AudioBucket frames into the Sphere's
// SphereAudio uniforms and pushes them to the App. ~60 Hz.
func pumpAudio(ctx context.Context, ch <-chan uihost.AudioBucket, app *uiebt.App) {
	for {
		select {
		case <-ctx.Done():
			return
		case b, ok := <-ch:
			if !ok {
				return
			}
			app.SetAudio(uiebt.SphereAudio{
				Bass:   b.Bass,
				Mid:    b.Mid,
				Treble: b.Treble,
				Rms:    b.Rms,
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
