package sessionbuild

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/alex60217101990/realtime-speech-translator/internal/config"
	"github.com/alex60217101990/realtime-speech-translator/internal/mt"
	"github.com/alex60217101990/realtime-speech-translator/internal/paths"
)

// buildMT constructs the MT engine but does not wrap it in the
// translation-memory cache. Returns the engine plus the effective
// backend name (which can differ from cfg.MTBackend if auto-
// resolved). Callers usually want buildMTWithCache instead.
func buildMT(cfg config.Settings) (mt.Engine, string, error) {
	root, err := paths.OPUSMTRoot()
	if err != nil {
		return nil, "", err
	}
	sm, err := paths.MTDir("small100-int8")
	if err != nil {
		return nil, "", err
	}
	m2m, err := paths.MTDir("m2m100-418m-int8")
	if err != nil {
		return nil, "", err
	}

	backend := resolveMTBackend(cfg.MTBackend, sm, m2m, root, cfg.SourceLang, cfg.TargetLang)
	if backend != cfg.MTBackend {
		slog.Info("sessionbuild: mt backend auto-resolved",
			"requested", cfg.MTBackend, "using", backend)
	}

	eng, err := mt.Build(mt.FactoryConfig{
		Backend:          backend,
		M2M100ModelDir:   m2m,
		M2M100SPModel:    filepath.Join(m2m, "sentencepiece.bpe.model"),
		SMaLL100ModelDir: sm,
		SMaLL100SPModel:  filepath.Join(sm, "sentencepiece.bpe.model"),
		OPUSMTRoot:       root,
		Threads:          cfg.Threads,
	})
	return eng, backend, err
}

// buildMTWithCache wraps the resolved MT engine in a disk-backed
// translation-memory cache. Returns the cache-wrapped Engine, the
// underlying *mt.Cached so the UI can call Override() / Sync(),
// and the effective backend name.
func buildMTWithCache(cfg config.Settings) (mt.Engine, *mt.Cached, string, error) {
	base, backend, err := buildMT(cfg)
	if err != nil {
		return nil, nil, backend, err
	}
	tmPath, err := paths.TranslationMemory()
	if err != nil {
		return base, nil, backend, fmt.Errorf("tm path: %w", err)
	}
	cached, err := mt.LoadCached(base, 1024, tmPath)
	if err != nil {
		slog.Warn("sessionbuild: tm load failed; starting empty cache", "err", err)
		cached = mt.NewCached(base, 1024).(*mt.Cached)
	}
	return cached, cached, backend, nil
}

// resolveMTBackend falls back to whatever MT model is actually
// usable on disk when the requested backend has no files. For
// opusmt the check is per-language-pair: a bare opusmt/ directory
// (created opportunistically by paths.OPUSMTPair) does not count
// — only an opusmt/<src>-<tgt>/model.bin file does. This keeps a
// user that flipped to opusmt without installing the en-es pair
// from getting "Unable to open file model.bin" on every translate.
func resolveMTBackend(requested, smDir, m2mDir, opusRoot, srcLang, tgtLang string) string {
	want := strings.ToLower(strings.TrimSpace(requested))
	if want == "off" {
		return "off"
	}
	usable := func(b string) bool {
		switch b {
		case "m2m100":
			return fileExists(filepath.Join(m2mDir, "model.bin"))
		case "small100":
			return fileExists(filepath.Join(smDir, "model.bin"))
		case "opusmt":
			pair := strings.ToLower(strings.TrimSpace(srcLang)) +
				"-" + strings.ToLower(strings.TrimSpace(tgtLang))
			return fileExists(filepath.Join(opusRoot, pair, "model.bin"))
		}
		return false
	}
	if want != "" && want != "auto" && usable(want) {
		return want
	}
	for _, b := range []string{"m2m100", "small100", "opusmt"} {
		if usable(b) {
			return b
		}
	}
	return "off"
}
