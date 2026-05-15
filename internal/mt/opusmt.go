package mt

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/alex60217101990/realtime-speech-translator/internal/mt/ct2"
	"github.com/alex60217101990/realtime-speech-translator/internal/mt/sp"
)

// OPUSMTConfig configures the OPUS-MT backend.
type OPUSMTConfig struct {
	// ModelsRoot holds one subdirectory per language pair, each named
	// "{src}-{dst}" (e.g. "ru-en"), containing the CTranslate2 export
	// plus source.spm / target.spm SentencePiece models.
	ModelsRoot string

	BeamSize          int
	MaxDecodingLength int
	Threads           int
	ComputeType       ct2.ComputeType
}

// DefaultOPUSMTConfig returns a Config tuned for low-latency CPU.
func DefaultOPUSMTConfig(modelsRoot string) OPUSMTConfig {
	return OPUSMTConfig{
		ModelsRoot:        modelsRoot,
		BeamSize:          1,
		MaxDecodingLength: 256,
		ComputeType:       ct2.ComputeInt8,
	}
}

// pairEngine holds the CTranslate2 model and its two SentencePiece
// tokenizers for one language pair.
type pairEngine struct {
	tr     *ct2.Translator
	srcTok *sp.Processor
	dstTok *sp.Processor
}

func (p *pairEngine) close() error {
	var firstErr error
	if err := p.tr.Close(); err != nil {
		firstErr = err
	}
	if err := p.srcTok.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := p.dstTok.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// OPUSMT is a lazy-loading per-pair Helsinki-NLP backend. Pairs are
// loaded on first use and held until Close.
type OPUSMT struct {
	cfg OPUSMTConfig

	mu    sync.Mutex
	pairs map[string]*pairEngine
}

// NewOPUSMT constructs an OPUS-MT engine. Models are not loaded until
// the first Translate call.
func NewOPUSMT(cfg OPUSMTConfig) (*OPUSMT, error) {
	if cfg.ModelsRoot == "" {
		return nil, fmt.Errorf("opusmt: empty ModelsRoot")
	}
	return &OPUSMT{cfg: cfg, pairs: map[string]*pairEngine{}}, nil
}

// Translate maps the {srcLang, dstLang} pair to a model directory under
// ModelsRoot and runs CTranslate2 + SentencePiece end-to-end.
func (o *OPUSMT) Translate(src, srcLang, dstLang string) (string, error) {
	if srcLang == "" || dstLang == "" {
		return "", fmt.Errorf("%w: missing src/dst", ErrUnsupportedLang)
	}
	key := strings.ToLower(srcLang) + "-" + strings.ToLower(dstLang)

	p, err := o.loadPair(key)
	if err != nil {
		return "", err
	}

	srcPieces, err := p.srcTok.EncodePieces(strings.TrimSpace(src))
	if err != nil {
		return "", fmt.Errorf("opusmt[%s]: encode: %w", key, err)
	}
	outPieces, err := p.tr.Translate(srcPieces, ct2.TranslateOptions{
		BeamSize:          o.cfg.BeamSize,
		MaxDecodingLength: o.cfg.MaxDecodingLength,
	})
	if err != nil {
		return "", fmt.Errorf("opusmt[%s]: translate: %w", key, err)
	}
	text, err := p.dstTok.DecodePieces(outPieces)
	if err != nil {
		return "", fmt.Errorf("opusmt[%s]: decode: %w", key, err)
	}
	return strings.TrimSpace(text), nil
}

// loadPair returns the (cached) pairEngine for the given "{src}-{dst}"
// key, loading it from ModelsRoot/<key>/ on first use.
func (o *OPUSMT) loadPair(key string) (*pairEngine, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if p, ok := o.pairs[key]; ok {
		return p, nil
	}
	dir := filepath.Join(o.cfg.ModelsRoot, key)
	tr, err := ct2.New(dir, ct2.Options{
		ComputeType: o.cfg.ComputeType,
		Threads:     o.cfg.Threads,
	})
	if err != nil {
		return nil, fmt.Errorf("opusmt[%s]: load ct2: %w", key, err)
	}
	srcTok, err := sp.Load(filepath.Join(dir, "source.spm"))
	if err != nil {
		_ = tr.Close()
		return nil, fmt.Errorf("opusmt[%s]: load src spm: %w", key, err)
	}
	dstTok, err := sp.Load(filepath.Join(dir, "target.spm"))
	if err != nil {
		_ = tr.Close()
		_ = srcTok.Close()
		return nil, fmt.Errorf("opusmt[%s]: load dst spm: %w", key, err)
	}
	p := &pairEngine{tr: tr, srcTok: srcTok, dstTok: dstTok}
	o.pairs[key] = p
	return p, nil
}

// Close releases every loaded pair.
func (o *OPUSMT) Close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	var firstErr error
	for _, p := range o.pairs {
		if err := p.close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	o.pairs = nil
	return firstErr
}
