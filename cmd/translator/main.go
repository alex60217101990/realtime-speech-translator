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
	"fyne.io/fyne/v2/dialog"
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

		session, nativeTTS, mtCache, err := buildSession(cfg)
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
		state.mtCache = mtCache
		fyne.Do(func() {
			liveCtl.currentStatus = "running"
			liveCtl.partial.SetText("")
			refreshHeader(liveCtl, cfg)
		})
		go pumpEvents(ctx, session, liveCtl, nativeTTS, &cfg, mtCache)
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
		if state.mtCache != nil {
			if err := state.mtCache.Sync(); err != nil {
				slog.Warn("tm sync on stop failed", "err", err)
			}
		}
		state = sessionState{}
		fyne.Do(func() {
			ctl.currentStatus = "stopped"
			ctl.startBtn.SetText("Start")
			ctl.partial.SetText("")
			refreshStatusBlock(ctl, rstapp.Stats{})
		})
	}

	// Fix-last-translation dialog: lets the user correct the most
	// recent translation; the correction is pinned in the TM
	// cache and replayed on every subsequent identical source.
	ctl.fixBtn.OnTapped = func() {
		ctl.lastSrcMu.Lock()
		src := ctl.lastSrc
		tgt := ctl.lastTgt
		ctl.lastSrcMu.Unlock()
		if src == "" {
			return
		}

		stateMu.Lock()
		cache := state.mtCache
		stateMu.Unlock()
		if cache == nil {
			return
		}

		srcLabel := widget.NewLabel(fmt.Sprintf("[%s] %s", nonEmpty(cfg.SourceLang, "src"), src))
		srcLabel.Wrapping = fyne.TextWrapWord
		entry := widget.NewMultiLineEntry()
		entry.SetText(tgt)
		entry.Wrapping = fyne.TextWrapWord

		dlg := newFixDialog(w, srcLabel, entry, func(corrected string) {
			corrected = strings.TrimSpace(corrected)
			if corrected == "" {
				return
			}
			cache.Override(src, cfg.SourceLang, cfg.TargetLang, corrected)
			if err := cache.Sync(); err != nil {
				slog.Warn("tm sync after override failed", "err", err)
			}
			slog.Info("translation overridden", "src", truncate(src, 80), "tgt", truncate(corrected, 80))
		})
		dlg.Show()
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
		if state.mtCache != nil {
			if err := state.mtCache.Sync(); err != nil {
				slog.Warn("tm sync on close failed", "err", err)
			}
		}
		stateMu.Unlock()
	})

	w.ShowAndRun()
}

// sessionState bundles the live session, the optional native TTS
// fallback, and the in-memory MT cache so the Fix-last-translation
// dialog can call Override + Sync without reaching into the
// session graph.
type sessionState struct {
	session   *rstapp.Session
	nativeTTS *native.Engine
	mtCache   *mt.Cached
	lastSrc   string // most recent stt Final, for the Fix dialog
	lastTgt   string // most recent MT Translation
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
func buildSession(cfg config.Settings) (*rstapp.Session, *native.Engine, *mt.Cached, error) {
	// Resolve "auto" / smart-pick into a concrete model name based
	// on SourceLang and what the user actually has installed under
	// <data>/models/stt/.
	resolvedSTT, autoNote := resolveSTTModel(cfg)
	if resolvedSTT == "" {
		return nil, nil, nil, fmt.Errorf("%w: no STT model installed under <data>/models/stt/", errModelsMissing)
	}
	if autoNote != "" {
		slog.Info("stt auto-select", "picked", resolvedSTT, "reason", autoNote)
	}

	sttDir, err := paths.STTDir(resolvedSTT)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("stt dir: %w", err)
	}
	vadPath, err := paths.VADModel()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("vad path: %w", err)
	}
	tokensPath := filepath.Join(sttDir, "tokens.txt")
	if _, err := os.Stat(tokensPath); err != nil {
		return nil, nil, nil, fmt.Errorf("%w: stt/%s/tokens.txt", errModelsMissing, resolvedSTT)
	}
	if _, err := os.Stat(vadPath); err != nil {
		return nil, nil, nil, fmt.Errorf("%w: vad/silero_vad.onnx", errModelsMissing)
	}

	// Backend factory: pick the right stt.Backend implementation by
	// inspecting which model files are on disk.
	//
	//   - encoder.onnx           → streaming Zipformer transducer.
	//   - model.onnx             → streaming T-one CTC.
	//   - model.int8.onnx        → offline NeMo CTC (GigaAM family).
	resolvedCfg := cfg
	resolvedCfg.STTModel = resolvedSTT
	sttBackend, kindName, err := buildSTTBackend(resolvedCfg, sttDir, tokensPath, vadPath)
	if err != nil {
		return nil, nil, nil, err
	}
	slog.Info("stt backend resolved",
		"model_name", resolvedSTT,
		"kind", kindName,
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

	mtEngine, mtCache, err := buildMTWithCache(cfg)
	if err != nil {
		slog.Warn("mt unavailable; running passthrough", "err", err)
		mtEngine = mt.Disabled{}
		mtCache = nil
	}
	if mtCache != nil {
		st := mtCache.Stats()
		slog.Info("translation memory loaded",
			"entries", st.Size, "pinned", st.Pinned)
	}

	sess, err := rstapp.New(rstapp.Config{
		Source:        cfg.SourceLang,
		Target:        cfg.TargetLang,
		STTBackend:    sttBackend,
		STTSampleRate: 16000,
		TTS:           ttsCfg,
		MT:            mtEngine,
		TTSEnabled:    ttsEnabled,
		Logger:        slog.Default(),
	})
	if err != nil {
		_ = sttBackend.Close()
		return nil, nil, nil, err
	}
	return sess, nativeFallback, mtCache, nil
}

// buildSTTBackend picks the right stt.Backend by inspecting the
// model directory on disk and returns it ready to Run.
func buildSTTBackend(cfg config.Settings, sttDir, tokensPath, vadPath string) (stt.Backend, string, error) {
	switch {
	case fileExists(filepath.Join(sttDir, "encoder.onnx")):
		// Streaming Zipformer transducer.
		for _, leaf := range []string{"decoder.onnx", "joiner.onnx"} {
			if !fileExists(filepath.Join(sttDir, leaf)) {
				return nil, "", fmt.Errorf("%w: stt/%s/%s", errModelsMissing, cfg.STTModel, leaf)
			}
		}
		c := stt.DefaultConfig()
		c.Kind = stt.ModelTransducer
		c.Encoder = filepath.Join(sttDir, "encoder.onnx")
		c.Decoder = filepath.Join(sttDir, "decoder.onnx")
		c.Joiner = filepath.Join(sttDir, "joiner.onnx")
		c.Tokens = tokensPath
		c.VADModel = vadPath
		c.NumThreads = cfg.Threads
		if cfg.VADThreshold > 0 {
			c.VADThreshold = cfg.VADThreshold
		}
		e, err := stt.New(c)
		if err != nil {
			return nil, "", err
		}
		return e, "transducer (streaming Zipformer)", nil

	case fileExists(filepath.Join(sttDir, "model.onnx")):
		// Streaming T-one CTC.
		c := stt.DefaultToneCtcConfig()
		c.Kind = stt.ModelToneCtc
		c.ToneCtcModel = filepath.Join(sttDir, "model.onnx")
		c.Tokens = tokensPath
		c.VADModel = vadPath
		c.NumThreads = cfg.Threads
		if cfg.VADThreshold > 0 {
			c.VADThreshold = cfg.VADThreshold
		}
		e, err := stt.New(c)
		if err != nil {
			return nil, "", err
		}
		return e, "tone_ctc (T-one streaming)", nil

	case fileExists(filepath.Join(sttDir, "model.int8.onnx")):
		// Offline NeMo CTC (GigaAM v3 punct, GigaAM v2, etc.).
		c := stt.DefaultVadNemoConfig()
		c.NemoCTCModel = filepath.Join(sttDir, "model.int8.onnx")
		c.Tokens = tokensPath
		c.VADModel = vadPath
		c.NumThreads = cfg.Threads
		if cfg.VADThreshold > 0 {
			c.VADThreshold = cfg.VADThreshold
		}
		e, err := stt.NewVadNemo(c)
		if err != nil {
			return nil, "", err
		}
		return e, "vad_nemo (offline NeMo CTC)", nil
	}
	return nil, "", fmt.Errorf("%w: stt/%s/(encoder|model|model.int8).onnx", errModelsMissing, cfg.STTModel)
}

// fileExists is the obvious helper; the os.Stat dance reads poorly
// in switch arms.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// resolveSTTModel returns the concrete STT directory name to load.
// When cfg.STTModel is anything other than "" or "auto" the user's
// explicit choice wins. Otherwise we pick the best installed model
// for cfg.SourceLang from the priority table below; "auto" source
// language falls back to the overall priority list.
//
// Priority lists are deliberately short and hard-coded — they match
// the entries in internal/models/catalog.go. Anything not on the
// list that the user installed manually is still picked up by the
// "any installed" fallback.
func resolveSTTModel(cfg config.Settings) (name, reason string) {
	if cfg.STTModel != "" && cfg.STTModel != "auto" {
		return cfg.STTModel, ""
	}
	root := modelsSubdir("stt")
	installed := map[string]bool{}
	for _, n := range listDirs(root) {
		installed[n] = true
	}
	if len(installed) == 0 {
		return "", ""
	}

	type priority []string
	var order priority
	switch strings.ToLower(cfg.SourceLang) {
	case "ru":
		order = priority{
			"nemo-ctc-punct-giga-am-v3-russian",
			"nemo-ctc-giga-am-v3-russian",
			"nemo-ctc-giga-am-v2-russian",
			"streaming-t-one-russian",
		}
	case "en":
		order = priority{
			"zipformer-streaming-en",
		}
	default: // "auto" or anything else — quality-first global priority.
		order = priority{
			"nemo-ctc-punct-giga-am-v3-russian",
			"zipformer-streaming-en",
			"nemo-ctc-giga-am-v3-russian",
			"nemo-ctc-giga-am-v2-russian",
			"streaming-t-one-russian",
		}
	}
	for _, candidate := range order {
		if installed[candidate] {
			return candidate, fmt.Sprintf("source=%s, best installed match", nonEmpty(cfg.SourceLang, "auto"))
		}
	}
	// Last resort: any installed model.
	for n := range installed {
		return n, "no preferred match; fell back to first installed"
	}
	return "", ""
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

// buildMTWithCache wraps the resolved MT engine in a disk-backed
// translation-memory cache. On boot we restore previous translations
// from <data>/translation_memory.json so repeated phrases hit
// instantly; the cache also persists user corrections (see Fix-
// last-translation dialog) which Override() never evicts.
//
// Returns the cache-wrapped Engine plus the underlying *mt.Cached so
// the UI can call Override() and Stats() directly.
func buildMTWithCache(cfg config.Settings) (mt.Engine, *mt.Cached, error) {
	base, err := buildMT(cfg)
	if err != nil {
		return nil, nil, err
	}
	tmPath, err := paths.TranslationMemory()
	if err != nil {
		return base, nil, fmt.Errorf("tm path: %w", err)
	}
	cached, err := mt.LoadCached(base, 1024, tmPath)
	if err != nil {
		// Disk read failures are non-fatal — start fresh.
		slog.Warn("tm load failed; starting empty cache", "err", err)
		cached = mt.NewCached(base, 1024).(*mt.Cached)
	}
	return cached, cached, nil
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
	fixBtn        *widget.Button
	transcript    *widget.List
	transcriptD   []string
	translation   *widget.List
	translationD  []string
	currentStatus string

	// Last STT Final + MT Translation seen, used by the Fix-last-
	// translation dialog. Guarded by lastSrcMu because they are
	// written from the event-pump goroutine and read from the
	// button callback on the UI thread.
	lastSrcMu sync.Mutex
	lastSrc   string
	lastTgt   string
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
	ctl.fixBtn = widget.NewButton("Fix last translation", nil)

	top := container.NewVBox(
		ctl.header,
		ctl.statusLine,
		ctl.statsLine,
		ctl.lagLine,
	)

	center := container.NewBorder(nil, nil, nil, ctl.speakingDot, ctl.viz)

	buttons := container.NewGridWithColumns(2, ctl.startBtn, ctl.fixBtn)

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
		"v%s   prev_mt: %s   prev_tts: %s   TM: %d entries (%d pinned)   hits=%d miss=%d (%.0f%%)",
		version, fmtMs(st.MTLast), fmtMs(st.TTSLast),
		st.TMSize, st.TMPinned, st.TMHits, st.TMMisses, st.TMHitRate*100,
	))
}

// newFixDialog assembles the Fix-last-translation modal: shows the
// source line, lets the user edit the target, calls onSave on Save.
func newFixDialog(w fyne.Window, src fyne.CanvasObject, target *widget.Entry, onSave func(string)) dialog.Dialog {
	body := container.NewVBox(
		widget.NewLabelWithStyle("Source", fyne.TextAlignLeading, fyne.TextStyle{Italic: true}),
		src,
		widget.NewLabelWithStyle("Translation (edit and save to pin)", fyne.TextAlignLeading, fyne.TextStyle{Italic: true}),
		target,
	)
	d := dialog.NewCustomConfirm("Fix last translation", "Save", "Cancel", body, func(ok bool) {
		if !ok {
			return
		}
		onSave(target.Text)
	}, w)
	d.Resize(fyne.NewSize(560, 320))
	return d
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
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
func pumpEvents(ctx context.Context, s *rstapp.Session, ctl *liveControls, nativeTTS *native.Engine, cfg *config.Settings, cache *mt.Cached) {
	statusTick := time.NewTicker(500 * time.Millisecond)
	defer statusTick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-statusTick.C:
			st := s.Stats()
			// Mix in TM cache stats — cmd owns the cache pointer.
			if cache != nil {
				cs := cache.Stats()
				st.TMSize = cs.Size
				st.TMPinned = cs.Pinned
				st.TMHits = cs.Hits
				st.TMMisses = cs.Misses
				st.TMHitRate = cs.HitRate
			}
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
				ctl.lastSrcMu.Lock()
				ctl.lastSrc = e.Text
				ctl.lastSrcMu.Unlock()
				line := fmt.Sprintf("[%s] %s", nonEmpty(cfg.SourceLang, "src"), e.Text)
				fyne.Do(func() {
					ctl.partial.SetText("")
					appendTranscript(ctl, line)
				})

			case rstapp.Translation:
				ctl.lastSrcMu.Lock()
				ctl.lastTgt = e.Target
				ctl.lastSrcMu.Unlock()
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
