package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/alex60217101990/realtime-speech-translator/internal/logging"
	"github.com/alex60217101990/realtime-speech-translator/internal/models"
	"github.com/alex60217101990/realtime-speech-translator/internal/uiebt"
)

// catalogAdapter bridges internal/models.DefaultCatalog into the
// uiebt.ModelEntry slice the UI renders. It also implements the
// uiebt.ModelInstaller interface so the Install button can fire
// the real downloader.
type catalogAdapter struct {
	app     *uiebt.App
	entries []models.Entry
	byKey   map[string]models.Entry

	mu      sync.Mutex
	running map[string]bool
}

func newCatalogAdapter(app *uiebt.App) *catalogAdapter {
	entries := models.DefaultCatalog()
	by := make(map[string]models.Entry, len(entries))
	for _, e := range entries {
		by[entryKey(e)] = e
	}
	return &catalogAdapter{
		app:     app,
		entries: entries,
		byKey:   by,
		running: map[string]bool{},
	}
}

func entryKey(e models.Entry) string {
	return fmt.Sprintf("%s-%s", strings.ToLower(string(e.Kind)), e.Name)
}

// toUIEntries projects models.Entry → uiebt.ModelEntry once. The
// UI re-reads installed-state separately because IsInstalled hits
// the filesystem.
func (c *catalogAdapter) toUIEntries() []uiebt.ModelEntry {
	out := make([]uiebt.ModelEntry, 0, len(c.entries))
	for _, e := range c.entries {
		out = append(out, uiebt.ModelEntry{
			Key:         entryKey(e),
			Kind:        strings.ToUpper(string(e.Kind)),
			DisplayName: e.DisplayName,
			License:     e.License,
			SizeBytes:   e.SizeBytes,
			Available:   e.URL != "",
		})
	}
	return out
}

// installedSnapshot probes each entry's RequiredFiles to build the
// boolean map the UI consumes. Cheap (stat-only), called once at
// catalog publish time and again after every install completes.
func (c *catalogAdapter) installedSnapshot() map[string]bool {
	out := make(map[string]bool, len(c.entries))
	for _, e := range c.entries {
		out[entryKey(e)] = e.IsInstalled()
	}
	return out
}

// Install satisfies uiebt.ModelInstaller. Fires the download for
// the supplied key in a background goroutine, pushing progress +
// completion state back onto the App.
func (c *catalogAdapter) Install(ctx context.Context, key string) {
	e, ok := c.byKey[key]
	if !ok {
		slog.Warn("catalogAdapter.Install: unknown key", "key", key)
		return
	}
	if e.URL == "" {
		slog.Info("catalogAdapter.Install: skipping URL-less entry", "key", key)
		return
	}
	c.mu.Lock()
	if c.running[key] {
		c.mu.Unlock()
		return
	}
	c.running[key] = true
	c.mu.Unlock()

	c.app.SetModelProgress(key, uiebt.ModelProgressSnapshot{Running: true})
	slog.Info("model install: starting", "key", key, "name", e.DisplayName)

	go func() {
		defer func() {
			c.mu.Lock()
			delete(c.running, key)
			c.mu.Unlock()
		}()
		err := e.Install(ctx, func(done, total int64) {
			c.app.SetModelProgress(key, uiebt.ModelProgressSnapshot{
				Done: done, Total: total, Running: true,
			})
		})
		if err != nil {
			slog.Error("model install failed", "key", key, "err", err)
			c.app.SetModelProgress(key, uiebt.ModelProgressSnapshot{
				Error: err.Error(),
			})
			return
		}
		slog.Info("model install: done", "key", key)
		c.app.SetModelProgress(key, uiebt.ModelProgressSnapshot{
			Done: e.SizeBytes, Total: e.SizeBytes,
		})
		c.app.SetModelInstalled(key, true)
	}()
}

// loggingLogSink is the adapter between internal/logging's ring
// (logging.Snapshot / SubscribeLogs) and uiebt.LogSink. Allows the
// terminal overlay to render real slog records without uiebt
// importing internal/logging directly.
type loggingLogSink struct{}

func (loggingLogSink) Snapshot() []uiebt.LogRecord {
	src := logging.Snapshot()
	out := make([]uiebt.LogRecord, len(src))
	for i, r := range src {
		out[i] = uiebt.LogRecord{
			When:    time.Unix(0, r.When),
			Level:   r.Level,
			Message: r.Message,
			Attrs:   r.Attrs,
		}
	}
	return out
}

func (loggingLogSink) Subscribe(fn func(uiebt.LogRecord)) func() {
	return logging.SubscribeLogs(func(l logging.LogLine) {
		fn(uiebt.LogRecord{
			When:    time.Unix(0, l.When),
			Level:   l.Level,
			Message: l.Message,
			Attrs:   l.Attrs,
		})
	})
}
