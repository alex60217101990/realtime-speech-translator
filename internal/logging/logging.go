// Package logging configures the application-wide log/slog handler and
// rotates the log file under the user data directory.
//
// Output sinks:
//
//	stderr  — pretty (text) handler, level from LOG_LEVEL or info
//	file    — JSON handler, level=debug, rotated by size
//
// Rotation uses a simple "tail-truncate" scheme: when the file exceeds
// MaxBytes it is renamed to <name>.1 and a fresh file is started. Only
// one rotated copy is kept. This is intentionally simpler than
// lumberjack — we want zero extra dependencies and acceptable behaviour
// for a desktop app that rarely produces > 100 MB of logs in a sitting.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/alex60217101990/realtime-speech-translator/internal/paths"
)

// MaxBytes is the size at which the active log file is rotated.
const MaxBytes int64 = 5 << 20

// Options configures Init.
type Options struct {
	// Level overrides LOG_LEVEL. Empty falls back to env, then info.
	Level slog.Level
	// LevelExplicit signals the caller set Level intentionally.
	LevelExplicit bool
	// FilePath overrides the default <data>/translator.log location.
	// Empty uses the default.
	FilePath string
}

// Init resolves the log file path, opens it (rotating if needed),
// wires slog.SetDefault, and returns a Close that flushes and releases
// resources.
func Init(opts Options) (close func() error, err error) {
	level := resolveLevel(opts)

	fp := opts.FilePath
	if fp == "" {
		dir, err := paths.Data()
		if err != nil {
			return nil, fmt.Errorf("logging: data dir: %w", err)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("logging: mkdir data: %w", err)
		}
		fp = filepath.Join(dir, "translator.log")
	}

	if err := rotateIfTooBig(fp); err != nil {
		// non-fatal — fall back to a fresh file
		_ = err
	}
	f, err := os.OpenFile(fp, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("logging: open file: %w", err)
	}

	rotator := &rotatingWriter{path: fp, file: f}
	fileHandler := slog.NewJSONHandler(rotator, &slog.HandlerOptions{Level: slog.LevelDebug})
	stderrHandler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})

	multi := &multiHandler{handlers: []slog.Handler{fileHandler, stderrHandler}}
	slog.SetDefault(slog.New(multi))

	slog.Info("logging initialised",
		"path", fp,
		"stderr_level", level.String(),
	)

	return func() error {
		rotator.mu.Lock()
		defer rotator.mu.Unlock()
		if rotator.file != nil {
			err := rotator.file.Close()
			rotator.file = nil
			return err
		}
		return nil
	}, nil
}

// resolveLevel takes the explicit option first, then LOG_LEVEL env, then
// the slog default (Info). Unknown env values fall back to Info.
func resolveLevel(opts Options) slog.Level {
	if opts.LevelExplicit {
		return opts.Level
	}
	switch strings.ToLower(os.Getenv("LOG_LEVEL")) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}

// rotateIfTooBig renames path → path.1 when its size exceeds MaxBytes,
// or no-ops when the file is missing or small enough.
func rotateIfTooBig(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Size() < MaxBytes {
		return nil
	}
	return os.Rename(path, path+".1")
}

// rotatingWriter wraps an *os.File with a Write that triggers rotation
// when MaxBytes is exceeded. Single-writer assumed.
type rotatingWriter struct {
	mu   sync.Mutex
	path string
	file *os.File
}

func (w *rotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return 0, fmt.Errorf("logging: writer closed")
	}
	n, err := w.file.Write(p)
	if err != nil {
		return n, err
	}
	// Best-effort rotate check; ignore errors to avoid log churn.
	if info, statErr := w.file.Stat(); statErr == nil && info.Size() >= MaxBytes {
		_ = w.file.Close()
		_ = os.Rename(w.path, w.path+".1")
		w.file, _ = os.OpenFile(w.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	}
	return n, nil
}

// multiHandler fans Handler calls out to every wrapped handler. Both
// the JSON and text handlers receive every record; their own level
// filtering decides whether to emit.
type multiHandler struct {
	handlers []slog.Handler
}

func (m *multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range m.handlers {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (m *multiHandler) Handle(ctx context.Context, r slog.Record) error {
	var first error
	for _, h := range m.handlers {
		if h.Enabled(ctx, r.Level) {
			if err := h.Handle(ctx, r.Clone()); err != nil && first == nil {
				first = err
			}
		}
	}
	return first
}

func (m *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		out[i] = h.WithAttrs(attrs)
	}
	return &multiHandler{handlers: out}
}

func (m *multiHandler) WithGroup(name string) slog.Handler {
	out := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		out[i] = h.WithGroup(name)
	}
	return &multiHandler{handlers: out}
}

// Silence the io unused-import lint when we only want the package to
// stay imported for its side-effect-free type aliases below.
var _ io.Writer = (*rotatingWriter)(nil)

// startupMarker is logged with a wall-clock timestamp at Init so it is
// easy to find the boundary between sessions in a long log file.
//
//nolint:unused // referenced from tests in the same package
func startupMarker() time.Time { return time.Now() }
