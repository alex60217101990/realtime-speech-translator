package mt

import (
	"fmt"
	"strings"

	"github.com/alex60217101990/realtime-speech-translator/internal/mt/ct2"
	"github.com/alex60217101990/realtime-speech-translator/internal/mt/sp"
)

// MADLADConfig configures the MADLAD-400 backend.
type MADLADConfig struct {
	// ModelDir is the path to the CTranslate2 export of MADLAD-400.
	ModelDir string
	// SentencePieceModel is the path to MADLAD's sentencepiece.model
	// (lives alongside ModelDir for HF / ct2-fast publications).
	SentencePieceModel string

	BeamSize          int
	MaxDecodingLength int
	Threads           int
	ComputeType       ct2.ComputeType
}

// DefaultMADLADConfig returns a Config tuned for low-latency CPU
// inference on the 3B int8 export.
func DefaultMADLADConfig(modelDir, spModel string) MADLADConfig {
	return MADLADConfig{
		ModelDir:           modelDir,
		SentencePieceModel: spModel,
		BeamSize:           1,
		MaxDecodingLength:  256,
		Threads:            0,
		ComputeType:        ct2.ComputeInt8,
	}
}

// MADLAD is a thin Engine over CTranslate2 + SentencePiece configured
// for the MADLAD-400 multilingual model. It expects the target language
// to be encoded as a literal prefix token "<2xx>" prepended to the
// source (this is how MADLAD was trained: the source is wrapped with a
// task tag).
type MADLAD struct {
	cfg   MADLADConfig
	tr    *ct2.Translator
	tok   *sp.Processor
}

// NewMADLAD constructs a MADLAD engine.
func NewMADLAD(cfg MADLADConfig) (*MADLAD, error) {
	if cfg.ModelDir == "" {
		return nil, fmt.Errorf("madlad: empty model dir")
	}
	if cfg.SentencePieceModel == "" {
		return nil, fmt.Errorf("madlad: empty sentencepiece model")
	}
	t, err := ct2.New(cfg.ModelDir, ct2.Options{
		ComputeType: cfg.ComputeType,
		Threads:     cfg.Threads,
	})
	if err != nil {
		return nil, fmt.Errorf("madlad: load ct2: %w", err)
	}
	tok, err := sp.Load(cfg.SentencePieceModel)
	if err != nil {
		_ = t.Close()
		return nil, fmt.Errorf("madlad: load sp: %w", err)
	}
	return &MADLAD{cfg: cfg, tr: t, tok: tok}, nil
}

// Translate runs MADLAD over a single source sentence. dstLang is
// expected as ISO-639-1 (e.g. "en", "ru"); we map it to MADLAD's
// "<2xx>" tag automatically. srcLang is ignored — MADLAD detects
// implicitly from the source pieces.
func (m *MADLAD) Translate(src, _ string, dstLang string) (string, error) {
	tag := madladTargetTag(dstLang)
	if tag == "" {
		return "", fmt.Errorf("%w: %q", ErrUnsupportedLang, dstLang)
	}
	// MADLAD source format: "<2xx> hello world". CTranslate2 consumes
	// piece strings (not ids) — sp.Processor.EncodePieces gives us the
	// right form directly.
	srcPieces, err := m.tok.EncodePieces(tag + " " + strings.TrimSpace(src))
	if err != nil {
		return "", fmt.Errorf("madlad: tokenize: %w", err)
	}

	outPieces, err := m.tr.Translate(srcPieces, ct2.TranslateOptions{
		BeamSize:          m.cfg.BeamSize,
		MaxDecodingLength: m.cfg.MaxDecodingLength,
	})
	if err != nil {
		return "", fmt.Errorf("madlad: translate: %w", err)
	}
	text, err := m.tok.DecodePieces(outPieces)
	if err != nil {
		return "", fmt.Errorf("madlad: decode: %w", err)
	}
	return strings.TrimSpace(text), nil
}

// Close releases the underlying CT2 + SP resources.
func (m *MADLAD) Close() error {
	var firstErr error
	if err := m.tr.Close(); err != nil {
		firstErr = err
	}
	if err := m.tok.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// madladTargetTag maps an ISO-639-1 code to MADLAD's target-language
// tag. Only the most common languages are listed; extend per release.
func madladTargetTag(iso string) string {
	if iso == "" {
		return ""
	}
	iso = strings.ToLower(iso)
	tag, ok := madladTags[iso]
	if !ok {
		return ""
	}
	return tag
}

// madladTags is a hand-maintained subset of MADLAD's 419 languages,
// keyed by the ISO-639-1 codes the UI exposes. The literal token form
// is "<2xx>" where xx matches the MADLAD vocabulary.
var madladTags = map[string]string{
	"en": "<2en>",
	"ru": "<2ru>",
	"es": "<2es>",
	"de": "<2de>",
	"fr": "<2fr>",
	"it": "<2it>",
	"pt": "<2pt>",
	"nl": "<2nl>",
	"pl": "<2pl>",
	"uk": "<2uk>",
	"zh": "<2zh>",
	"ja": "<2ja>",
	"ko": "<2ko>",
	"ar": "<2ar>",
	"hi": "<2hi>",
	"tr": "<2tr>",
	"vi": "<2vi>",
	"id": "<2id>",
	"sv": "<2sv>",
	"fi": "<2fi>",
	"da": "<2da>",
	"no": "<2no>",
	"cs": "<2cs>",
	"el": "<2el>",
	"he": "<2he>",
	"ro": "<2ro>",
	"hu": "<2hu>",
	"th": "<2th>",
}
