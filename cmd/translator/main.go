// Command translator is the realtime speech translator GUI.
//
// Pipeline (see internal/app/session for the wiring):
//
//	mic ─▶ capture ─▶ stt (streaming Zipformer + Silero VAD)
//	                       │
//	                  Partial / Final
//	                       │
//	                       ▼
//	                 mt.Engine (SMaLL-100 / OPUS-MT / Disabled)
//	                       │
//	                  Translation
//	                       │
//	                       ▼
//	                 tts (sherpa Piper, streaming PCM)
//	                       │
//	                       ▼
//	                playback ─▶ speaker
//
// Flags override config.yaml for one-shot tweaks; the Settings tab
// in the UI writes config.yaml directly.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	rstapp "github.com/alex60217101990/realtime-speech-translator/internal/app"
	"github.com/alex60217101990/realtime-speech-translator/internal/config"
	"github.com/alex60217101990/realtime-speech-translator/internal/logging"
	"github.com/alex60217101990/realtime-speech-translator/internal/models"
	"github.com/alex60217101990/realtime-speech-translator/internal/mt"
	"github.com/alex60217101990/realtime-speech-translator/internal/paths"
	"github.com/alex60217101990/realtime-speech-translator/internal/stt"
	"github.com/alex60217101990/realtime-speech-translator/internal/tts"
	"github.com/alex60217101990/realtime-speech-translator/internal/tts/native"
	"github.com/alex60217101990/realtime-speech-translator/internal/ui"
)

// version is filled at build time via -ldflags "-X main.version=...".
var version = "dev"

// errModelsMissing flags a buildSession failure that the caller can
// recover from by installing the missing artefacts through the
// Models tab. Anything else is a hard error.
var errModelsMissing = errors.New("models missing")

func main() {
	src := flag.String("source", "", "override source language (ISO-639-1 or 'auto')")
	dst := flag.String("target", "", "override target language (ISO-639-1)")
	noTTS := flag.Bool("no-tts", false, "disable TTS + playback for this run")
	logLevel := flag.String("log-level", "", "override log level (debug/info/warn/error)")
	flag.Parse()

	logOpts := logging.Options{}
	if *logLevel != "" {
		if err := logOpts.Level.UnmarshalText([]byte(*logLevel)); err == nil {
			logOpts.LevelExplicit = true
		}
	}
	closeLog, err := logging.Init(logOpts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "log init:", err)
	} else {
		defer closeLog()
	}
	slog.Info("translator starting", "version", version)

	cfg, err := config.Load()
	if err != nil {
		slog.Warn("config load failed; using defaults", "err", err)
		cfg = config.Default()
	}
	if *src != "" {
		cfg.SourceLang = *src
	}
	if *dst != "" {
		cfg.TargetLang = *dst
	}
	if *noTTS {
		cfg.TTSEnabled = false
	}

	a := app.NewWithID("io.github.alex60217101990.rstranslator")
	w := a.NewWindow("Realtime Speech Translator")
	w.Resize(fyne.NewSize(720, 520))

	live, liveCtl := buildLiveTab(cfg)

	ctx, cancel := context.WithCancel(context.Background())

	// sessionState owns the lifecycle of the rstapp.Session so we
	// can rebuild it after the user installs missing models without
	// restarting the whole binary.
	var (
		state    sessionState
		stateMu  sync.Mutex
		tryStart func()
	)

	tabs := container.NewAppTabs()

	rebuildSettings := func() {
		settings := ui.SettingsScreen(w, cfg, ui.SettingsCallbacks{
			AvailableSTTModels: listDirs(modelsSubdir("stt")),
			AvailableTTSVoices: listDirs(modelsSubdir("tts")),
			OnSave: func(s config.Settings) {
				slog.Info("settings saved", "stt", s.STTModel, "mt", s.MTBackend, "tts", s.TTSVoice)
				cfg = s
				// Reload the session so the new model paths take
				// effect immediately. tryStart handles cleanup of
				// any previous session.
				if tryStart != nil {
					tryStart()
				}
			},
		})
		// Find an existing Settings tab and replace its content;
		// otherwise append. Lookup-by-name is safer than indexing
		// because the tab order may evolve.
		for _, item := range tabs.Items {
			if item.Text == "Settings" {
				item.Content = settings
				tabs.Refresh()
				return
			}
		}
		tabs.Append(container.NewTabItem("Settings", settings))
	}

	modelsTab := ui.ModelsScreen(w, models.DefaultCatalog(), ui.ModelsCallbacks{
		OnInstalled: func(e models.Entry) {
			slog.Info("model installed", "kind", e.Kind, "name", e.Name)
			// Auto-point the config at the freshly installed
			// artefact so the user does not have to open Settings
			// just to make a download usable. VAD is singleton
			// (silero_vad.onnx); STT and TTS are name-keyed.
			switch e.Kind {
			case models.KindSTT:
				cfg.STTModel = e.Name
				_ = config.Save(cfg)
			case models.KindTTS:
				cfg.TTSVoice = e.Name
				_ = config.Save(cfg)
			}
			rebuildSettings()
			if tryStart != nil {
				tryStart()
			}
		},
	})

	// Order matches the previous UX: Main first, Models in the
	// middle (because that is where the user goes from a fresh
	// install), Settings last.
	tabs.Append(container.NewTabItem("Main", live))
	tabs.Append(container.NewTabItem("Models", modelsTab))
	rebuildSettings()

	w.SetContent(tabs)

	tryStart = func() {
		stateMu.Lock()
		defer stateMu.Unlock()

		if state.session != nil {
			_ = state.session.Stop()
			_ = state.session.Close()
			state = sessionState{}
		}

		session, nativeTTS, err := buildSession(cfg)
		if err != nil {
			if errors.Is(err, errModelsMissing) {
				slog.Warn("session not started; install models via the Models tab", "err", err)
				fyne.Do(func() {
					liveCtl.currentStatus = "waiting for models"
					liveCtl.partial.SetText("Install STT + VAD models in the Models tab to start.")
					refreshStatusBlock(liveCtl, rstapp.Stats{})
				})
				return
			}
			slog.Error("build session", "err", err)
			fyne.Do(func() {
				liveCtl.currentStatus = "error"
				liveCtl.partial.SetText("Session error: " + err.Error())
			})
			return
		}
		if nativeTTS != nil {
			slog.Info("piper unavailable; using native TTS fallback", "backend", nativeTTS.Backend())
		}
		if err := session.Start(ctx); err != nil {
			slog.Error("session start", "err", err)
			_ = session.Close()
			fyne.Do(func() {
				liveCtl.currentStatus = "start failed"
				liveCtl.partial.SetText("Start failed: " + err.Error())
			})
			return
		}
		state.session = session
		state.nativeTTS = nativeTTS
		fyne.Do(func() {
			liveCtl.currentStatus = "running"
			liveCtl.partial.SetText("")
			refreshHeader(liveCtl, cfg)
		})
		go pumpEvents(ctx, session, liveCtl, nativeTTS, &cfg)
	}

	ctl := liveCtl

	// Start/Stop toggle: button text + behaviour switches based on
	// whether we currently own a running session.
	stopSession := func() {
		stateMu.Lock()
		defer stateMu.Unlock()
		if state.session == nil {
			return
		}
		slog.Info("ui: stop button — tearing down session")
		_ = state.session.Stop()
		_ = state.session.Close()
		state = sessionState{}
		fyne.Do(func() {
			ctl.currentStatus = "stopped"
			ctl.startBtn.SetText("Start")
			ctl.partial.SetText("")
			refreshStatusBlock(ctl, rstapp.Stats{})
		})
	}

	ctl.startBtn.OnTapped = func() {
		stateMu.Lock()
		running := state.session != nil
		stateMu.Unlock()
		if running {
			stopSession()
			return
		}
		slog.Info("ui: start button — bringing up session")
		ctl.currentStatus = "starting"
		ctl.partial.SetText("")
		tryStart()
	}

	// Reflect post-tryStart state in the button label.
	refreshStartBtnFromState := func() {
		fyne.Do(func() {
			stateMu.Lock()
			defer stateMu.Unlock()
			if state.session != nil {
				ctl.startBtn.SetText("Stop")
			} else {
				ctl.startBtn.SetText("Start")
			}
		})
	}
	_ = refreshStartBtnFromState // referenced inside tryStart below

	// Wrap tryStart to also flip the button.
	innerTryStart := tryStart
	tryStart = func() {
		innerTryStart()
		refreshStartBtnFromState()
	}

	tryStart()

	w.SetOnClosed(func() {
		cancel()
		stateMu.Lock()
		if state.session != nil {
			_ = state.session.Close()
		}
		stateMu.Unlock()
	})

	w.ShowAndRun()
}

// sessionState bundles the live session and the optional native TTS
// fallback so the lifecycle goroutine can swap both atomically.
type sessionState struct {
	session   *rstapp.Session
	nativeTTS *native.Engine
}

// buildSession resolves all model paths from the settings + paths
// helpers, constructs the MT engine, and returns a ready-but-not-yet-
// started rstapp.Session.
//
// When TTSEnabled is true but the Piper voice files are missing or
// fail to load, the session is constructed with TTS off and a
// nativeTTS engine (say / espeak / powershell) is returned for the
// cmd layer to invoke directly from the event pump. Returns
// nativeTTS == nil when sherpa Piper loaded successfully or when
// the host has no usable native synthesizer either.
func buildSession(cfg config.Settings) (*rstapp.Session, *native.Engine, error) {
	sttDir, err := paths.STTDir(cfg.STTModel)
	if err != nil {
		return nil, nil, fmt.Errorf("stt dir: %w", err)
	}
	vadPath, err := paths.VADModel()
	if err != nil {
		return nil, nil, fmt.Errorf("vad path: %w", err)
	}
	// Detect ModelKind by which files are on disk. Transducer
	// families ship encoder/decoder/joiner; T-one (CTC) ships a
	// single model.onnx.
	tokensPath := filepath.Join(sttDir, "tokens.txt")
	if _, err := os.Stat(tokensPath); err != nil {
		return nil, nil, fmt.Errorf("%w: stt/%s/tokens.txt", errModelsMissing, cfg.STTModel)
	}
	if _, err := os.Stat(vadPath); err != nil {
		return nil, nil, fmt.Errorf("%w: vad/silero_vad.onnx", errModelsMissing)
	}

	sttCfg := stt.DefaultConfig()
	sttCfg.Tokens = tokensPath
	sttCfg.VADModel = vadPath
	sttCfg.NumThreads = cfg.Threads
	sttCfg.VADThreshold = cfg.VADThreshold

	switch {
	case fileExists(filepath.Join(sttDir, "encoder.onnx")):
		sttCfg.Kind = stt.ModelTransducer
		sttCfg.Encoder = filepath.Join(sttDir, "encoder.onnx")
		sttCfg.Decoder = filepath.Join(sttDir, "decoder.onnx")
		sttCfg.Joiner = filepath.Join(sttDir, "joiner.onnx")
		for _, leaf := range []string{"decoder.onnx", "joiner.onnx"} {
			if !fileExists(filepath.Join(sttDir, leaf)) {
				return nil, nil, fmt.Errorf("%w: stt/%s/%s", errModelsMissing, cfg.STTModel, leaf)
			}
		}
	case fileExists(filepath.Join(sttDir, "model.onnx")):
		sttCfg.Kind = stt.ModelToneCtc
		sttCfg.ToneCtcModel = filepath.Join(sttDir, "model.onnx")
	default:
		return nil, nil, fmt.Errorf("%w: stt/%s/(encoder|model).onnx", errModelsMissing, cfg.STTModel)
	}
	slog.Info("stt backend resolved",
		"model_name", cfg.STTModel,
		"kind", sttKindName(sttCfg.Kind),
	)

	var ttsCfg tts.Config
	ttsEnabled := cfg.TTSEnabled
	var nativeFallback *native.Engine
	if ttsEnabled {
		ttsDir, err := paths.TTSDir(cfg.TTSVoice)
		if err == nil && piperVoiceComplete(ttsDir) {
			ttsCfg = tts.DefaultConfig()
			ttsCfg.Model = filepath.Join(ttsDir, "model.onnx")
			ttsCfg.Tokens = filepath.Join(ttsDir, "tokens.txt")
			ttsCfg.DataDir = filepath.Join(ttsDir, "espeak-ng-data")
			ttsCfg.NumThreads = cfg.Threads
		} else {
			slog.Warn("piper voice not installed; attempting native TTS",
				"voice", cfg.TTSVoice, "err", err)
			ttsEnabled = false
			if n, err := native.New(); err == nil {
				nativeFallback = n
			} else {
				slog.Warn("no native TTS either; translations will be text-only", "err", err)
			}
		}
	}

	mtEngine, err := buildMT(cfg)
	if err != nil {
		slog.Warn("mt unavailable; running passthrough", "err", err)
		mtEngine = mt.Disabled{}
	}

	sess, err := rstapp.New(rstapp.Config{
		Source:     cfg.SourceLang,
		Target:     cfg.TargetLang,
		STT:        sttCfg,
		TTS:        ttsCfg,
		MT:         mtEngine,
		TTSEnabled: ttsEnabled,
		Logger:     slog.Default(),
	})
	if err != nil {
		return nil, nil, err
	}
	return sess, nativeFallback, nil
}

// fileExists is the obvious helper; the os.Stat dance reads poorly
// in switch arms.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func sttKindName(k stt.ModelKind) string {
	switch k {
	case stt.ModelTransducer:
		return "transducer"
	case stt.ModelToneCtc:
		return "tone_ctc"
	}
	return "unknown"
}

// piperVoiceComplete returns true when the directory contains the
// three files required by the sherpa Piper engine.
func piperVoiceComplete(dir string) bool {
	for _, leaf := range []string{"model.onnx", "tokens.txt", "espeak-ng-data"} {
		if _, err := os.Stat(filepath.Join(dir, leaf)); err != nil {
			return false
		}
	}
	return true
}

func buildMT(cfg config.Settings) (mt.Engine, error) {
	root, err := paths.OPUSMTRoot()
	if err != nil {
		return nil, err
	}
	sm, err := paths.MTDir("small100")
	if err != nil {
		return nil, err
	}
	return mt.Build(mt.FactoryConfig{
		Backend:          cfg.MTBackend,
		SMaLL100ModelDir: sm,
		SMaLL100SPModel:  filepath.Join(sm, "sentencepiece.bpe.model"),
		OPUSMTRoot:       root,
		Threads:          cfg.Threads,
	})
}

// liveControls bundles the widgets the event pump updates.
type liveControls struct {
	header        *widget.Label
	statusLine    *widget.Label
	statsLine     *widget.Label
	lagLine       *widget.Label
	partial       *widget.Label
	speakingDot   *widget.Label
	viz           *ui.VoiceViz
	startBtn      *widget.Button
	transcript    *widget.List
	transcriptD   []string
	translation   *widget.List
	translationD  []string
	currentStatus string
}

func buildLiveTab(cfg config.Settings) (fyne.CanvasObject, *liveControls) {
	ctl := &liveControls{
		header:        widget.NewLabel(""),
		statusLine:    widget.NewLabel(""),
		statsLine:     widget.NewLabel(""),
		lagLine:       widget.NewLabel(""),
		partial:       widget.NewLabel(""),
		speakingDot:   widget.NewLabel(" "),
		viz:           ui.NewVoiceViz(),
		currentStatus: "idle",
	}
	ctl.partial.Wrapping = fyne.TextWrapWord

	mkList := func(data *[]string) *widget.List {
		return widget.NewList(
			func() int { return len(*data) },
			func() fyne.CanvasObject {
				l := widget.NewLabel("")
				l.Wrapping = fyne.TextWrapWord
				return l
			},
			func(i widget.ListItemID, o fyne.CanvasObject) {
				o.(*widget.Label).SetText((*data)[i])
			},
		)
	}
	ctl.transcript = mkList(&ctl.transcriptD)
	ctl.translation = mkList(&ctl.translationD)

	refreshHeader(ctl, cfg)
	refreshStatusBlock(ctl, rstapp.Stats{})

	ctl.startBtn = widget.NewButton("Start", nil)

	top := container.NewVBox(
		ctl.header,
		ctl.statusLine,
		ctl.statsLine,
		ctl.lagLine,
	)

	center := container.NewBorder(nil, nil, nil, ctl.speakingDot, ctl.viz)

	buttons := container.NewGridWithColumns(1, ctl.startBtn)

	twoCol := container.NewGridWithColumns(2,
		container.NewBorder(
			widget.NewLabelWithStyle("Transcript", fyne.TextAlignLeading, fyne.TextStyle{Italic: true}),
			nil, nil, nil,
			ctl.transcript,
		),
		container.NewBorder(
			widget.NewLabelWithStyle("Translation", fyne.TextAlignLeading, fyne.TextStyle{Italic: true}),
			nil, nil, nil,
			ctl.translation,
		),
	)

	root := container.NewBorder(
		container.NewVBox(top, center),
		container.NewVBox(buttons, ctl.partial, twoCol),
		nil, nil,
		nil,
	)
	return root, ctl
}

// refreshHeader rewrites the header line that summarises which
// engines / devices the current session is bound to. Called on boot
// and every time cfg changes.
func refreshHeader(ctl *liveControls, cfg config.Settings) {
	parts := []string{
		"Source: " + nonEmpty(cfg.SourceLang, "auto"),
		"Target: " + nonEmpty(cfg.TargetLang, "—"),
		"MT: " + nonEmpty(cfg.MTBackend, "off"),
		"TTS: " + ttsLabel(cfg),
		"VMic: " + nonEmpty(cfg.OutputDevice, "system default"),
	}
	ctl.header.SetText(strings.Join(parts, "    "))
}

func ttsLabel(cfg config.Settings) string {
	if !cfg.TTSEnabled {
		return "off"
	}
	if cfg.TTSVoice == "" {
		return "piper"
	}
	return "piper · " + cfg.TTSVoice
}

func nonEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// refreshStatusBlock updates the three status rows that mirror the
// pre-rewrite UX: status / capture / mic / VAD / utts, then per-stage
// timings, then a lag breakdown.
func refreshStatusBlock(ctl *liveControls, st rstapp.Stats) {
	status := ctl.currentStatus
	if !st.Running && status == "" {
		status = "idle"
	}
	if st.Running && status == "" {
		status = "running"
	}
	ctl.statusLine.SetText(fmt.Sprintf(
		"Status: %s   Captured: %.1fs   Mic: %d%%   VAD active: %d%%   Utts: %d (drop %d)",
		status,
		st.Captured.Seconds(),
		st.MicPct,
		st.VADActivePct,
		st.Utterances,
		st.STTDropped,
	))
	ctl.statsLine.SetText(fmt.Sprintf(
		"MT: %s   TTS: %s   Drops: mt=%d tts=%d cap=%d   Underruns: %d",
		fmtMs(st.MTLast), fmtMs(st.TTSLast),
		st.MTDropped, st.TTSDropped, st.CaptureDropped,
		st.PlaybackUnderrn,
	))
	ctl.lagLine.SetText(fmt.Sprintf(
		"v%s   prev_mt: %s   prev_tts: %s",
		version, fmtMs(st.MTLast), fmtMs(st.TTSLast),
	))
}

func fmtMs(d time.Duration) string {
	if d <= 0 {
		return "—"
	}
	return fmt.Sprintf("%.3fs", d.Seconds())
}

// pumpEvents drains the Session event channel onto the UI controls.
// All widget mutations cross into the Fyne goroutine via go fyne.Do —
// safe under Fyne v2's threading model.
//
// When nativeTTS is non-nil the session itself runs with TTS off and
// every Translation event is forwarded to the native synthesizer in
// a fire-and-forget goroutine.
func pumpEvents(ctx context.Context, s *rstapp.Session, ctl *liveControls, nativeTTS *native.Engine, cfg *config.Settings) {
	statusTick := time.NewTicker(500 * time.Millisecond)
	defer statusTick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-statusTick.C:
			st := s.Stats()
			fyne.Do(func() {
				refreshStatusBlock(ctl, st)
				ctl.viz.SetLevel(st.MicRMS*4, st.VADActivePct > 0)
			})

		case ev, ok := <-s.Events():
			if !ok {
				return
			}
			switch e := ev.(type) {
			case rstapp.Partial:
				txt := e.Text
				fyne.Do(func() { ctl.partial.SetText(txt) })

			case rstapp.Final:
				line := fmt.Sprintf("[%s] %s", nonEmpty(cfg.SourceLang, "src"), e.Text)
				fyne.Do(func() {
					ctl.partial.SetText("")
					appendTranscript(ctl, line)
				})

			case rstapp.Translation:
				line := fmt.Sprintf("[%s] %s", nonEmpty(cfg.TargetLang, "tgt"), e.Target)
				fyne.Do(func() { appendTranslation(ctl, line) })
				if nativeTTS != nil && e.Target != "" {
					go func(text string) {
						if err := nativeTTS.Speak(ctx, text); err != nil {
							slog.Warn("native tts speak", "err", err)
						}
					}(e.Target)
				}

			case rstapp.Speaking:
				dot := " "
				if e.Active {
					dot = "🔊"
				}
				fyne.Do(func() { ctl.speakingDot.SetText(dot) })

			case rstapp.ErrorEv:
				if e.Err != nil && !errors.Is(e.Err, context.Canceled) {
					msg := "! " + e.Err.Error()
					fyne.Do(func() { appendTranscript(ctl, msg) })
				}
			}
		}
	}
}

const historyCap = 128

func appendTranscript(ctl *liveControls, line string) {
	ctl.transcriptD = append(ctl.transcriptD, line)
	if len(ctl.transcriptD) > historyCap {
		ctl.transcriptD = ctl.transcriptD[len(ctl.transcriptD)-historyCap:]
	}
	ctl.transcript.Refresh()
	ctl.transcript.ScrollToBottom()
}

func appendTranslation(ctl *liveControls, line string) {
	ctl.translationD = append(ctl.translationD, line)
	if len(ctl.translationD) > historyCap {
		ctl.translationD = ctl.translationD[len(ctl.translationD)-historyCap:]
	}
	ctl.translation.Refresh()
	ctl.translation.ScrollToBottom()
}

// modelsSubdir resolves <data>/models/<kind>/ for UI directory
// listings. Errors are swallowed — the dropdowns just stay empty.
func modelsSubdir(kind string) string {
	root, err := paths.Models()
	if err != nil {
		return ""
	}
	return filepath.Join(root, kind)
}

// listDirs returns the names of immediate subdirectories of root.
// Used to populate the STT / TTS dropdowns with whatever the user
// has actually downloaded.
func listDirs(root string) []string {
	if root == "" {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		out = append(out, name)
	}
	return out
}
