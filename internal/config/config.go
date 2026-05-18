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

// Settings is the persisted application configuration.
type Settings struct {
	// SourceLang ISO-639-1 or "auto".
	SourceLang string `yaml:"source_lang"`
	// TargetLang ISO-639-1.
	TargetLang string `yaml:"target_lang"`

	// WhisperModel: tiny|base|small|medium|large.
	WhisperModel string `yaml:"whisper_model"`
	// MTBackend: madlad|opusmt|off.
	MTBackend string `yaml:"mt_backend"`

	// Threads is the per-engine thread cap; 0 = auto.
	Threads int `yaml:"threads"`

	// VADAggressiveness 0..3.
	VADAggressiveness int `yaml:"vad_aggressiveness"`

	// OutputDevice is a name substring matched by vmic; empty =
	// first detected virtual mic.
	OutputDevice string `yaml:"output_device"`

	// TTSEnabled toggles Piper playback.
	TTSEnabled bool `yaml:"tts_enabled"`
	// TTSBinaryPath overrides the PATH-resolved Piper binary.
	TTSBinaryPath string `yaml:"tts_binary_path"`

	// Theme: light|dark|system.
	Theme string `yaml:"theme"`
}

// Default returns the factory defaults used on first launch.
func Default() Settings {
	return Settings{
		SourceLang:        "auto",
		TargetLang:        "en",
		WhisperModel:      "small",
		MTBackend:         "madlad",
		Threads:           0,
		VADAggressiveness: 2,
		OutputDevice:      "",
		TTSEnabled:        true,
		TTSBinaryPath:     "",
		Theme:             "system",
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
	return s, nil
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
