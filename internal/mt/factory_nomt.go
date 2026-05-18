//go:build !mt

package mt

import "fmt"

// build is the no-op variant compiled when the binary lacks the
// `mt` build tag. It always returns ErrBackendUnavailable so the
// caller can fall back to Disabled with a single error check.
func build(cfg FactoryConfig) (Engine, error) {
	return nil, fmt.Errorf("%w: backend %q requested", ErrBackendUnavailable, cfg.Backend)
}
