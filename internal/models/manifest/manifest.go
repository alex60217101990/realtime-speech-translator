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

// Tier classifies a model by its realtime suitability. The UI surfaces
// this as a badge ("realtime" / "balanced" / "quality") so the user
// knows which entries are appropriate for live translation versus
// offline / accuracy-first batch use.
type Tier string

const (
	TierRealtime Tier = "realtime"
	TierBalanced Tier = "balanced"
	TierQuality  Tier = "quality"
)

// Whisper describes a Whisper ggml-format model.
type Whisper struct {
	URL     string `yaml:"url"`
	SHA256  string `yaml:"sha256"`
	SizeMB  int    `yaml:"size_mb"`
	Tier    Tier   `yaml:"tier,omitempty"`
	Default bool   `yaml:"default,omitempty"`
}

// Voice describes a Piper TTS voice (onnx + json sidecar).
type Voice struct {
	ONNXURL string `yaml:"onnx_url"`
	JSONURL string `yaml:"json_url"`
	SizeMB  int    `yaml:"size_mb"`
	Lang    string `yaml:"lang"`
}

// MT describes a CTranslate2-format MT model directory. Most CT2
// exports require these files in the same directory:
//
//   - model.bin                 (URL)
//   - config.json               (ConfigURL)
//   - shared_vocabulary.json    (VocabURL) — sometimes .txt instead
//   - sentencepiece.model       (TokenizerURL)
//   - target.spm                (Tokenizer2URL, OPUS-MT only)
//
// Any field left empty is skipped by the downloader; OPUS-MT style
// models that ship a separate target tokenizer use Tokenizer2URL.
//
// Backend tags the loader kind: "madlad", "m2m100", "opusmt". The UI
// groups entries by backend and lets the user switch via Settings.
//
// Pair (OPUS-MT only) is the "src-dst" language pair this model serves;
// the application picks the right entry from the active source/target
// language at translate time.
type MT struct {
	URL           string `yaml:"url"`
	ConfigURL     string `yaml:"config_url"`
	VocabURL      string `yaml:"vocab_url"`
	TokenizerURL  string `yaml:"tokenizer_url"`
	Tokenizer2URL string `yaml:"tokenizer2_url,omitempty"`
	VocabName     string `yaml:"vocab_name"` // default: shared_vocabulary.json
	SHA256        string `yaml:"sha256"`
	SizeMB        int    `yaml:"size_mb"`
	Backend       string `yaml:"backend,omitempty"` // madlad | m2m100 | opusmt
	Pair          string `yaml:"pair,omitempty"`    // OPUS-MT pair, e.g. ru-en
	Tier          Tier   `yaml:"tier,omitempty"`
	Default       bool   `yaml:"default,omitempty"`

	// Manual marks entries the user has to install by hand because no
	// reliable public mirror exists (e.g. CT2-converted m2m100 / OPUS-MT
	// were either pulled from HuggingFace or made gated). The Models
	// Manager renders these rows without a Download button and points
	// at the README's manual-install instructions instead.
	Manual bool `yaml:"manual,omitempty"`

	// Archive flips the downloader into "fetch one tarball, unpack
	// into the model directory" mode. URL points at a .tar.gz; the
	// archive is expected to contain model.bin / config.json /
	// sentencepiece.* alongside each other. Used by our GitHub
	// Releases-hosted CT2 conversions so the user no longer has to
	// stand up a Python + torch + ctranslate2 toolchain.
	Archive bool `yaml:"archive,omitempty"`
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
