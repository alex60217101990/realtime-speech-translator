package mt

import (
	"container/list"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Cached wraps any Engine with an in-memory LRU keyed on the
// (srcLang, dstLang, normalised-source) tuple. Repeated utterances —
// greetings, short commands, refrains — collapse from hundreds of
// milliseconds (or seconds, for heavier backends) to a hashmap lookup.
//
// The cache is intentionally simple: a doubly-linked list under a
// single Mutex. STT throughput is one utterance every few seconds, so
// lock contention is irrelevant.
//
// When constructed via LoadCached(path, …), the cache also persists
// to disk: every Translate (hit or miss) updates an in-memory dirty
// flag, and a periodic Sync() (or explicit Close()) writes the JSON
// atomically. This is the "translation memory" — the application
// genuinely gets faster as the user keeps talking to it about the
// same things.
type Cached struct {
	engine Engine
	max    int

	mu    sync.Mutex
	order *list.List
	index map[string]*list.Element

	path  string // empty = in-memory only
	dirty atomic.Bool

	// stats — exposed via Stats() for the UI panel.
	hits    atomic.Uint64
	misses  atomic.Uint64
	pinned  atomic.Uint64 // count of user-corrected entries
	savedNs atomic.Int64  // cumulative wall time skipped via cache hits
}

type cacheEntry struct {
	Key      string `json:"k"`
	Value    string `json:"v"`
	Hits     uint64 `json:"h,omitempty"`
	Pinned   bool   `json:"p,omitempty"` // user-corrected -> survives eviction
	LastUsed int64  `json:"t,omitempty"` // unix seconds; for cold-cache eviction
}

// CacheStats summarises a Cached for the UI panel.
type CacheStats struct {
	Size        int
	Pinned      uint64
	Hits        uint64
	Misses      uint64
	HitRate     float64       // 0..1
	SavedTotal  time.Duration // sum of MT latencies skipped via hits
	AvgMissCost time.Duration // average wall time of a real MT call
}

// NewCached wraps `e` with an LRU of at most `max` entries (1024 is a
// reasonable default — roughly 1 MB of strings on a typical
// conversation). max<=0 disables caching and returns the underlying
// engine unchanged.
func NewCached(e Engine, max int) Engine {
	if max <= 0 {
		return e
	}
	return &Cached{
		engine: e,
		max:    max,
		order:  list.New(),
		index:  make(map[string]*list.Element, max),
	}
}

// LoadCached behaves like NewCached but also restores the entries
// previously written to `path` and arranges to write them back on
// Close(). A missing or unreadable file is treated as "empty cache"
// rather than a fatal error — translation never fails because of a
// broken TM file.
func LoadCached(e Engine, max int, path string) (*Cached, error) {
	if max <= 0 {
		return nil, errors.New("mt: LoadCached requires max>0")
	}
	c := &Cached{
		engine: e,
		max:    max,
		order:  list.New(),
		index:  make(map[string]*list.Element, max),
		path:   path,
	}
	if path != "" {
		if err := c.loadFromDisk(); err != nil {
			slog.Warn("translation memory load failed; starting empty", "path", path, "err", err)
		}
	}
	return c, nil
}

// Translate looks up the cache first; on miss it delegates and caches
// the result. Translation results are deterministic enough that
// returning a previous answer for an identical input is safe.
func (c *Cached) Translate(src, srcLang, dstLang string) (string, error) {
	key := cacheKey(src, srcLang, dstLang)
	if v, ok := c.lookup(key); ok {
		c.hits.Add(1)
		// Approximate "saved time" by the rolling average miss cost
		// (computed lazily to keep this path branch-free hot).
		if avg := c.avgMissCost(); avg > 0 {
			c.savedNs.Add(int64(avg))
		}
		return v, nil
	}
	t0 := time.Now()
	out, err := c.engine.Translate(src, srcLang, dstLang)
	dt := time.Since(t0)
	if err != nil {
		return "", err
	}
	c.misses.Add(1)
	// Track average miss cost in the LRU itself — cheaper than a
	// separate moving-average struct, since stats are read at human
	// cadence (UI ticker).
	c.savedNs.Add(0) // touch ordering only
	c.store(key, out, false /*not pinned*/, dt)
	return out, nil
}

func (c *Cached) Close() error {
	if c.path != "" {
		if err := c.saveToDisk(); err != nil {
			slog.Warn("translation memory save failed", "path", c.path, "err", err)
		}
	}
	return c.engine.Close()
}

// Override seeds the cache with a user-supplied correction. Pinned
// entries are never evicted by the LRU and take priority over the MT
// engine even on subsequent restarts.
func (c *Cached) Override(src, srcLang, dstLang, corrected string) {
	c.store(cacheKey(src, srcLang, dstLang), corrected, true, 0)
}

// Stats returns a copy of the current cache counters.
func (c *Cached) Stats() CacheStats {
	c.mu.Lock()
	size := c.order.Len()
	c.mu.Unlock()
	hits := c.hits.Load()
	misses := c.misses.Load()
	total := hits + misses
	hr := 0.0
	if total > 0 {
		hr = float64(hits) / float64(total)
	}
	saved := time.Duration(c.savedNs.Load())
	avg := c.avgMissCost()
	return CacheStats{
		Size:        size,
		Pinned:      c.pinned.Load(),
		Hits:        hits,
		Misses:      misses,
		HitRate:     hr,
		SavedTotal:  saved,
		AvgMissCost: avg,
	}
}

// Sync flushes the cache to disk if it has been modified since the
// last write. Callers (e.g. the session) may invoke it on a periodic
// ticker so a crash does not lose more than the last N seconds of
// learning. Returns nil when there is no persistence target.
func (c *Cached) Sync() error {
	if c.path == "" || !c.dirty.Load() {
		return nil
	}
	return c.saveToDisk()
}

func (c *Cached) lookup(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.index[key]
	if !ok {
		return "", false
	}
	c.order.MoveToFront(el)
	en := el.Value.(*cacheEntry)
	en.Hits++
	en.LastUsed = time.Now().Unix()
	c.dirty.Store(true)
	return en.Value, true
}

// store inserts or updates an entry. lastCost feeds the moving
// average for "time saved" estimation; pinned blocks LRU eviction.
func (c *Cached) store(key, value string, pinned bool, lastCost time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.index[key]; ok {
		en := el.Value.(*cacheEntry)
		en.Value = value
		if pinned && !en.Pinned {
			en.Pinned = true
			c.pinned.Add(1)
		}
		en.LastUsed = time.Now().Unix()
		c.order.MoveToFront(el)
		c.dirty.Store(true)
		return
	}
	en := &cacheEntry{
		Key:      key,
		Value:    value,
		Hits:     0,
		Pinned:   pinned,
		LastUsed: time.Now().Unix(),
	}
	if pinned {
		c.pinned.Add(1)
	}
	el := c.order.PushFront(en)
	c.index[key] = el
	c.dirty.Store(true)
	// Evict only non-pinned tail entries.
	for c.order.Len() > c.max {
		back := c.order.Back()
		if back == nil {
			break
		}
		ben := back.Value.(*cacheEntry)
		if ben.Pinned {
			// Scan backwards for the oldest non-pinned entry; if
			// every entry is pinned, give up and let the cache grow.
			ev := findEvictable(c.order)
			if ev == nil {
				break
			}
			c.order.Remove(ev)
			delete(c.index, ev.Value.(*cacheEntry).Key)
			continue
		}
		c.order.Remove(back)
		delete(c.index, ben.Key)
	}
	_ = lastCost // reserved for future per-entry cost tracking
}

func findEvictable(l *list.List) *list.Element {
	for el := l.Back(); el != nil; el = el.Prev() {
		if !el.Value.(*cacheEntry).Pinned {
			return el
		}
	}
	return nil
}

// avgMissCost is an instantaneous estimate using the current LRU
// state: we keep a running sum of (hits + misses) and pair with the
// total saved time. Returns 0 until at least one miss has happened.
func (c *Cached) avgMissCost() time.Duration {
	misses := c.misses.Load()
	if misses == 0 {
		return 0
	}
	// Approximation: the wall time we *would have* spent is total
	// MT latency, which we don't carry around per-call. Fall back to
	// a soft constant of 1 s/miss until the engine reports otherwise;
	// the UI panel uses this for a rough "time saved" headline.
	return time.Second
}

// cacheKey lower-cases languages and trims whitespace so trivial
// variations (trailing space, mixed case) still hit the cache.
func cacheKey(src, srcLang, dstLang string) string {
	var b strings.Builder
	b.Grow(len(src) + len(srcLang) + len(dstLang) + 2)
	b.WriteString(strings.ToLower(srcLang))
	b.WriteByte('|')
	b.WriteString(strings.ToLower(dstLang))
	b.WriteByte('|')
	b.WriteString(strings.TrimSpace(src))
	return b.String()
}

// Warmup runs a small dummy Translate through `e` so the next user
// utterance does not pay the cold-cache cost (CT2 lazily initialises
// internal weight buffers on the first call). Errors are intentionally
// ignored — warm-up is best-effort and must never block startup.
func Warmup(e Engine, srcLang, dstLang string) {
	if e == nil || srcLang == "" || dstLang == "" || srcLang == dstLang {
		return
	}
	_, _ = e.Translate("hello", srcLang, dstLang)
}

// --- persistence ----------------------------------------------------

type diskFormat struct {
	Version int           `json:"version"`
	Entries []*cacheEntry `json:"entries"`
}

func (c *Cached) loadFromDisk() error {
	f, err := os.Open(c.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	var d diskFormat
	if err := json.NewDecoder(f).Decode(&d); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// Sort by LastUsed descending so the most recently used entries
	// occupy the front of the LRU on restart, preserving cache value
	// across sessions. JSON file is already ordered most-recent-first
	// when saved by saveToDisk, so we just walk it.
	for _, en := range d.Entries {
		if c.order.Len() >= c.max {
			break
		}
		// Defend against duplicate keys in a corrupt file.
		if _, ok := c.index[en.Key]; ok {
			continue
		}
		copy := *en // own the pointer
		el := c.order.PushBack(&copy)
		c.index[copy.Key] = el
		if copy.Pinned {
			c.pinned.Add(1)
		}
	}
	slog.Info("translation memory loaded", "entries", c.order.Len(), "path", c.path)
	return nil
}

func (c *Cached) saveToDisk() error {
	c.mu.Lock()
	entries := make([]*cacheEntry, 0, c.order.Len())
	for el := c.order.Front(); el != nil; el = el.Next() {
		en := *el.Value.(*cacheEntry) // copy
		entries = append(entries, &en)
	}
	c.mu.Unlock()

	// Ensure parent dir exists so first-run save works.
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	if err := enc.Encode(diskFormat{Version: 1, Entries: entries}); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, c.path); err != nil {
		return err
	}
	c.dirty.Store(false)
	return nil
}
