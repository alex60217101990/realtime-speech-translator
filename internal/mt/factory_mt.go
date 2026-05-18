//go:build mt

package mt

import (
	"fmt"
	"strings"
)

// build constructs the concrete Engine matching cfg.Backend. The
// returned Engine is wrapped in Serial so callers never need to
// arrange their own decode-side mutex.
func build(cfg FactoryConfig) (Engine, error) {
	switch strings.ToLower(cfg.Backend) {
	case "m2m100":
		c := DefaultM2M100Config(cfg.M2M100ModelDir, cfg.M2M100SPModel)
		c.Threads = cfg.Threads
		e, err := NewM2M100(c)
		if err != nil {
			return nil, fmt.Errorf("mt: build m2m100: %w", err)
		}
		return Serial(e), nil

	case "small100":
		c := DefaultSMaLL100Config(cfg.SMaLL100ModelDir, cfg.SMaLL100SPModel)
		c.Threads = cfg.Threads
		e, err := NewSMaLL100(c)
		if err != nil {
			return nil, fmt.Errorf("mt: build small100: %w", err)
		}
		return Serial(e), nil

	case "opusmt":
		c := DefaultOPUSMTConfig(cfg.OPUSMTRoot)
		c.Threads = cfg.Threads
		e, err := NewOPUSMT(c)
		if err != nil {
			return nil, fmt.Errorf("mt: build opusmt: %w", err)
		}
		return Serial(e), nil

	default:
		return nil, fmt.Errorf("mt: unknown backend %q", cfg.Backend)
	}
}
