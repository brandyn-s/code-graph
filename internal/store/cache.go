package store

import (
	"sync"
	"time"
)

// QueryCache is a thread-safe LRU cache for query results.
type QueryCache struct {
	mu      sync.RWMutex
	entries map[string]*cacheEntry
	order   []string // LRU order: most recent at end
	maxSize int
	ttl     time.Duration
}

type cacheEntry struct {
	result    any
	timestamp time.Time
}

// NewQueryCache creates a cache with the given max entries and TTL.
func NewQueryCache(maxSize int, ttl time.Duration) *QueryCache {
	return &QueryCache{
		entries: make(map[string]*cacheEntry),
		order:   make([]string, 0, maxSize),
		maxSize: maxSize,
		ttl:     ttl,
	}
}

// Get returns a cached result and true if found and not expired.
func (c *QueryCache) Get(key string) (any, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if time.Since(entry.timestamp) > c.ttl {
		return nil, false
	}
	return entry.result, true
}

// Set stores a result in the cache.
func (c *QueryCache) Set(key string, result any) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// If already exists, update
	if _, ok := c.entries[key]; ok {
		c.entries[key] = &cacheEntry{result: result, timestamp: time.Now()}
		c.moveToEnd(key)
		return
	}

	// Evict oldest if at capacity
	if len(c.entries) >= c.maxSize {
		oldest := c.order[0]
		delete(c.entries, oldest)
		c.order = c.order[1:]
	}

	c.entries[key] = &cacheEntry{result: result, timestamp: time.Now()}
	c.order = append(c.order, key)
}

// Invalidate clears all cached entries.
func (c *QueryCache) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]*cacheEntry)
	c.order = c.order[:0]
}

// Len returns the number of cached entries.
func (c *QueryCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}

func (c *QueryCache) moveToEnd(key string) {
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			c.order = append(c.order, key)
			return
		}
	}
}
