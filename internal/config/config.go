// Package config persists user-facing settings to a YAML file in the
// platform's configuration directory. The schema is intentionally flat
// — the UI binds individual fields directly to widgets.
package config

import (
	"errors"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/alex60217101990/realtime-speech-translator/internal/paths"
)

// Settings is the persisted application configuration. Fields map to
// the new sherpa-onnx architecture: streaming Zipformer STT, sherpa
// in-process Piper TTS, optional CT2 MT (small100 / opusmt) gated by
// the `mt` build tag.
type Settings struct {
	// SourceLang ISO-639-1 (or "auto" — currently treated as a hint
	// for MT only; the streaming Zipformer is fundamentally
	// multilingual or per-language depending on the chosen model).
	SourceLang string `yaml:"source_lang"`
	// TargetLang ISO-639-1.
	TargetLang string `yaml:"target_lang"`

	// STTModel is the directory name under <data>/models/stt/. The
	// directory must contain encoder.onnx, decoder.onnx, joiner.onnx
	// and tokens.txt for a streaming Zipformer transducer.
	STTModel string `yaml:"stt_model"`

	// MTBackend selects the translation engine. Valid values:
	//   "small100" — distilled M2M-100, one model for 100 languages.
	//   "opusmt"   — per-pair Helsinki-NLP, fastest.
	//   "off"      — passthrough (mt.Disabled).
	// When the binary was built without `-tags mt`, the latter is
	// the only working option; the others fall back to "off" at
	// session-construction time with a logged warning.
	MTBackend string `yaml:"mt_backend"`

	// TTSVoice is the directory name under <data>/models/tts/. The
	// directory must contain model.onnx + tokens.txt + espeak-ng-data/
	// (the standard Piper VITS layout).
	TTSVoice string `yaml:"tts_voice"`

	// Threads is the per-engine thread cap; 0 = runtime.NumCPU().
	Threads int `yaml:"threads"`

	// VADThreshold is the Silero VAD speech probability gate. 0.0
	// passes everything; 1.0 nothing. 0.5 is the published default.
	VADThreshold float32 `yaml:"vad_threshold"`

	// OutputDevice is a name substring matched by vmic; empty = first
	// detected virtual mic.
	OutputDevice string `yaml:"output_device"`

	// TTSEnabled toggles synthesis + playback. When false the
	// pipeline still emits Translation events; nothing is spoken.
	TTSEnabled bool `yaml:"tts_enabled"`

	// Theme: light|dark|system.
	Theme string `yaml:"theme"`
}

// Default returns the factory defaults used on first launch.
func Default() Settings {
	return Settings{
		SourceLang:   "ru",
		TargetLang:   "en",
		// "auto" lets the cmd layer pick the best installed STT
		// model for the configured SourceLang. Users can override
		// in Settings to lock to a specific model name.
		STTModel:     "auto",
		MTBackend:    "m2m100",
		TTSVoice:     "piper-en-amy-low",
		Threads:      0,
		VADThreshold: 0.5,
		OutputDevice: "",
		TTSEnabled:   true,
		Theme:        "system",
	}
}

// Path returns the absolute path to config.yaml. The directory is
// created on demand.
func Path() (string, error) {
	dir, err := paths.Config()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.yaml"), nil
}

// Load reads config.yaml. Missing file is treated as a first launch:
// Default() is returned without error.
func Load() (Settings, error) {
	p, err := Path()
	if err != nil {
		return Settings{}, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Settings{}, err
	}
	s := Default()
	if err := yaml.Unmarshal(b, &s); err != nil {
		return Settings{}, err
	}
	normalise(&s)
	return s, nil
}

// normalise rewrites legacy MT backend names from the previous
// architecture so a config.yaml carried over from an older binary
// does not log "backend unavailable" on every launch. Unknown
// values fall back to Default's m2m100 since that's the only MT
// we currently ship as a downloadable release asset.
func normalise(s *Settings) {
	switch s.MTBackend {
	case "m2m100", "small100", "opusmt", "off":
		// already valid
	default:
		s.MTBackend = "m2m100"
	}
}

// Save writes the settings to config.yaml using atomic rename.
func Save(s Settings) error {
	p, err := Path()
	if err != nil {
		return err
	}
	b, err := yaml.Marshal(s)
	if err != nil {
		return err
	}
	tmp := p + ".part"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
