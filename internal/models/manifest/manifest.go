// Package manifest defines the model registry — which models the
// application knows about, their download URLs and integrity hashes.
// The manifest is embedded at build time and parsed at startup.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"
)

//go:embed manifest.yaml
var manifestBytes []byte

// File holds the parsed manifest contents.
type File struct {
	Version int                `yaml:"version"`
	Whisper map[string]Whisper `yaml:"whisper"`
	TTS     map[string]Voice   `yaml:"tts"`
	MT      map[string]MT      `yaml:"mt"`
}

// Whisper describes a Whisper ggml-format model.
type Whisper struct {
	URL    string `yaml:"url"`
	SHA256 string `yaml:"sha256"`
	SizeMB int    `yaml:"size_mb"`
	Default bool  `yaml:"default,omitempty"`
}

// Voice describes a Piper TTS voice (onnx + json sidecar).
type Voice struct {
	ONNXURL string `yaml:"onnx_url"`
	JSONURL string `yaml:"json_url"`
	SizeMB  int    `yaml:"size_mb"`
	Lang    string `yaml:"lang"`
}

// MT describes a CTranslate2-format MT model directory.
type MT struct {
	URL          string `yaml:"url"`
	ConfigURL    string `yaml:"config_url"`
	TokenizerURL string `yaml:"tokenizer_url"`
	SHA256       string `yaml:"sha256"`
	SizeMB       int    `yaml:"size_mb"`
	Default      bool   `yaml:"default,omitempty"`
}

// Load parses the embedded manifest.
func Load() (*File, error) {
	var f File
	if err := yaml.Unmarshal(manifestBytes, &f); err != nil {
		return nil, fmt.Errorf("manifest: parse: %w", err)
	}
	if f.Version != 1 {
		return nil, fmt.Errorf("manifest: unsupported version %d", f.Version)
	}
	return &f, nil
}

// DefaultWhisper returns the manifest entry marked as default, or an
// error if none is set.
func (f *File) DefaultWhisper() (name string, w Whisper, err error) {
	for n, w := range f.Whisper {
		if w.Default {
			return n, w, nil
		}
	}
	return "", Whisper{}, errors.New("manifest: no default whisper model")
}

// VerifySHA256 reads the file at path and returns nil iff its SHA256
// matches the provided hex digest. Empty digest is treated as a soft
// "checksum unknown" and accepted.
func VerifySHA256(path string, want string) error {
	if want == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("manifest: open for verify: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("manifest: read for verify: %w", err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != want {
		return fmt.Errorf("manifest: sha256 mismatch: got %s want %s", got, want)
	}
	return nil
}
