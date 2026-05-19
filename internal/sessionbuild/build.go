// Package sessionbuild centralises the "resolve models on disk +
// build an rstapp.Session" logic so both the Fyne shell at
// cmd/translator and the Ebiten shell at cmd/translator-ui can
// reuse it without duplicating ~300 lines of boilerplate.
//
// The Build entry point returns everything a UI needs to bind to:
// the started-on-its-own session, the optional native TTS fallback,
// and the live mt.Cached pointer so the UI can call Override / Sync
// from a Fix-last-translation dialog.
package sessionbuild

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	rstapp "github.com/alex60217101990/realtime-speech-translator/internal/app"
	"github.com/alex60217101990/realtime-speech-translator/internal/config"
	"github.com/alex60217101990/realtime-speech-translator/internal/mt"
	"github.com/alex60217101990/realtime-speech-translator/internal/paths"
	"github.com/alex60217101990/realtime-speech-translator/internal/tts"
	"github.com/alex60217101990/realtime-speech-translator/internal/tts/native"
)

// ErrModelsMissing is returned when one or more model files the
// chosen STT / TTS backend needs is not on disk. Cmd code should
// log and surface a "open the Models tab" message rather than
// crashing.
var ErrModelsMissing = errors.New("sessionbuild: models missing")

// Result bundles the artefacts a UI binds to.
type Result struct {
	Session   *rstapp.Session
	MTCache   *mt.Cached     // nil if MT is mt.Disabled
	NativeTTS *native.Engine // nil if sherpa Piper loaded successfully
	STTModel  string         // resolved STT directory name
	STTKind   string         // "transducer (streaming Zipformer)" etc.
	// MTBackend is the effective backend actually loaded, which can
	// differ from cfg.MTBackend when the requested model is missing
	// (e.g. cfg says small100 but only m2m100 is on disk — the auto-
	// resolver picks the latter). UI uses this to render the real
	// state in Settings instead of the misleading config value.
	MTBackend string
}

// Build resolves model paths from cfg, constructs the STT/MT/TTS
// engines and returns a ready-but-not-yet-Started Session.
// Returns ErrModelsMissing (wrapped) when the chosen STT models
// are missing — UI should redirect the user to the Models tab.
func Build(cfg config.Settings) (*Result, error) {
	resolvedSTT, autoNote := ResolveSTTModel(cfg)
	if resolvedSTT == "" {
		return nil, fmt.Errorf("%w: no STT model installed under <data>/models/stt/", ErrModelsMissing)
	}
	if autoNote != "" {
		slog.Info("sessionbuild: stt auto-select", "picked", resolvedSTT, "reason", autoNote)
	}

	sttDir, err := paths.STTDir(resolvedSTT)
	if err != nil {
		return nil, fmt.Errorf("stt dir: %w", err)
	}
	vadPath, err := paths.VADModel()
	if err != nil {
		return nil, fmt.Errorf("vad path: %w", err)
	}
	tokensPath := filepath.Join(sttDir, "tokens.txt")
	if _, err := os.Stat(tokensPath); err != nil {
		return nil, fmt.Errorf("%w: stt/%s/tokens.txt", ErrModelsMissing, resolvedSTT)
	}
	if _, err := os.Stat(vadPath); err != nil {
		return nil, fmt.Errorf("%w: vad/silero_vad.onnx", ErrModelsMissing)
	}

	resolvedCfg := cfg
	resolvedCfg.STTModel = resolvedSTT
	sttBackend, kindName, err := buildSTTBackend(resolvedCfg, sttDir, tokensPath, vadPath)
	if err != nil {
		return nil, err
	}

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
			slog.Warn("sessionbuild: piper voice not installed; attempting native TTS",
				"voice", cfg.TTSVoice, "err", err)
			ttsEnabled = false
			if n, err := native.New(); err == nil {
				nativeFallback = n
			} else {
				slog.Warn("sessionbuild: no native TTS either; translations will be text-only", "err", err)
			}
		}
	}

	mtEngine, mtCache, mtBackend, err := buildMTWithCache(cfg)
	if err != nil {
		slog.Warn("sessionbuild: mt unavailable; running passthrough", "err", err)
		mtEngine = mt.Disabled{}
		mtCache = nil
		mtBackend = "off"
	}
	if mtCache != nil {
		st := mtCache.Stats()
		slog.Info("sessionbuild: translation memory loaded",
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
		return nil, err
	}
	return &Result{
		Session:   sess,
		MTCache:   mtCache,
		NativeTTS: nativeFallback,
		STTModel:  resolvedSTT,
		STTKind:   kindName,
		MTBackend: mtBackend,
	}, nil
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

// ListInstalled returns the directory names found under
// <data>/models/<kind>/. Stable input for STT / TTS dropdowns.
func ListInstalled(kind string) []string {
	root, err := paths.Models()
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(filepath.Join(root, kind))
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

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
