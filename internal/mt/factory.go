package mt

import (
	"errors"
	"strings"
)

// FactoryConfig is the request the cmd layer hands to Build. The
// resolved paths come from internal/paths plus config.Settings.
type FactoryConfig struct {
	// Backend selects the implementation. Valid values:
	//   "m2m100"   — facebook/m2m100_418M, original tag scheme
	//                (requires -tags mt).
	//   "small100" — distilled M2M-100 (requires -tags mt).
	//   "opusmt"   — per-pair Helsinki-NLP (requires -tags mt).
	//   "off"      — Disabled passthrough.
	Backend string

	// M2M-100 paths.
	M2M100ModelDir string
	M2M100SPModel  string

	// SMaLL-100 paths.
	SMaLL100ModelDir string
	SMaLL100SPModel  string

	// OPUS-MT root (one subdirectory per language pair).
	OPUSMTRoot string

	// Threads is the CT2 thread cap; 0 = library default.
	Threads int
}

// ErrBackendUnavailable is returned by Build when the requested
// backend exists in source but the binary was compiled without
// `-tags mt`. Cmd code typically logs a warning and falls back to
// Disabled.
var ErrBackendUnavailable = errors.New("mt: backend requires -tags mt build")

// Build resolves the requested backend into a ready-to-use Engine,
// always wrapped in Serial so the audio thread cannot overlap
// decode calls. Callers may further wrap the returned Engine with
// NewCached or LoadCached.
func Build(cfg FactoryConfig) (Engine, error) {
	switch strings.ToLower(cfg.Backend) {
	case "", "off":
		return Disabled{}, nil
	default:
		return build(cfg) // implementation differs by build tag
	}
}
