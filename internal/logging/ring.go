// Ring buffer for slog records. Used by the in-window terminal log
// pane (internal/uiebt) so the UI can show the same lines slog
// already wrote to disk + stderr without re-parsing the file.
//
// One global ring is wired in by Init so any goroutine can publish
// without taking a handle; the UI calls SubscribeLogs to register a
// fan-out hook + Snapshot to seed its initial buffer.
package logging

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"sync"
)

// RingSize bounds memory for the in-window log buffer. ~500 lines
// at ~120 bytes each ≈ 60 KiB worst case — negligible.
const RingSize = 500

// LogLine is the rendered, immutable view of one slog record kept
// in the ring. Pre-rendered (not slog.Record) so the UI does not
// need a handler clone and so attrs survive without escaping.
type LogLine struct {
	When    int64 // unix nanos
	Level   slog.Level
	Message string
	Attrs   string // " key=val key=val", pre-joined
}

// LogSubscriber receives every new line as it arrives. Called from
// the goroutine that produced the slog call — must not block.
type LogSubscriber func(LogLine)

// ringStore is the shared backing buffer + subscriber list. Every
// derived ringHandler (from WithAttrs / WithGroup) points at the
// same store so attrs threading does not fork the log stream.
type ringStore struct {
	mu          sync.RWMutex
	buf         []LogLine
	subscribers []LogSubscriber
}

var globalStore = &ringStore{buf: make([]LogLine, 0, RingSize)}

// ringHandler is the slog.Handler that stores rendered lines in
// globalStore and fans new lines out to live subscribers. Cheap to
// derive — only `attrs` is copied per WithAttrs call.
type ringHandler struct {
	store *ringStore
	attrs string // accumulated WithAttrs prefix
}

// newRingHandler returns the slog.Handler the multi handler chain
// should include. Internal — call sites stay inside the logging
// package.
func newRingHandler() slog.Handler {
	return &ringHandler{store: globalStore}
}

// Snapshot copies the current ring contents. Safe for the UI to
// hold; the buffer is never mutated in place.
func Snapshot() []LogLine {
	globalStore.mu.RLock()
	defer globalStore.mu.RUnlock()
	out := make([]LogLine, len(globalStore.buf))
	copy(out, globalStore.buf)
	return out
}

// SubscribeLogs registers a callback fired for every new record.
// Returns an unsubscribe func. The callback runs on the producer's
// goroutine — keep it cheap (push to a chan, append to a slice).
func SubscribeLogs(s LogSubscriber) (unsubscribe func()) {
	globalStore.mu.Lock()
	id := len(globalStore.subscribers)
	globalStore.subscribers = append(globalStore.subscribers, s)
	globalStore.mu.Unlock()
	return func() {
		globalStore.mu.Lock()
		defer globalStore.mu.Unlock()
		if id < len(globalStore.subscribers) {
			globalStore.subscribers[id] = nil
		}
	}
}

func (h *ringHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *ringHandler) Handle(_ context.Context, r slog.Record) error {
	line := LogLine{
		When:    r.Time.UnixNano(),
		Level:   r.Level,
		Message: r.Message,
		Attrs:   h.renderAttrs(r),
	}
	h.store.mu.Lock()
	if len(h.store.buf) == cap(h.store.buf) {
		copy(h.store.buf, h.store.buf[1:])
		h.store.buf = h.store.buf[:len(h.store.buf)-1]
	}
	h.store.buf = append(h.store.buf, line)
	subs := append([]LogSubscriber(nil), h.store.subscribers...)
	h.store.mu.Unlock()
	for _, s := range subs {
		if s != nil {
			s(line)
		}
	}
	return nil
}

func (h *ringHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	var sb strings.Builder
	sb.WriteString(h.attrs)
	for _, a := range attrs {
		writeAttr(&sb, a)
	}
	return &ringHandler{store: h.store, attrs: sb.String()}
}

func (h *ringHandler) WithGroup(name string) slog.Handler {
	_ = name // grouping is collapsed into the attrs prefix; no-op
	return h
}

func (h *ringHandler) renderAttrs(r slog.Record) string {
	if r.NumAttrs() == 0 && h.attrs == "" {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(h.attrs)
	r.Attrs(func(a slog.Attr) bool {
		writeAttr(&sb, a)
		return true
	})
	return sb.String()
}

func writeAttr(sb *strings.Builder, a slog.Attr) {
	sb.WriteByte(' ')
	sb.WriteString(a.Key)
	sb.WriteByte('=')
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		s := v.String()
		if strings.ContainsAny(s, " \t\"") {
			sb.WriteString(strconv.Quote(s))
		} else {
			sb.WriteString(s)
		}
	default:
		sb.WriteString(v.String())
	}
}
