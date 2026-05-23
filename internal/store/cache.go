package store

import (
	"sync"
	"time"
)

// QueryCache is a thread-safe LRU cache for query results.
// Eviction order is maintained by a doubly-linked list so promote-to-MRU
// and oldest-eviction are O(1) regardless of cache size. LRU is updated
// on Set (re-insert promotes), not on Get — preserving prior semantics.
type QueryCache struct {
	mu      sync.RWMutex
	entries map[string]*cacheEntry
	head    *cacheEntry // oldest (LRU end)
	tail    *cacheEntry // newest (MRU end)
	maxSize int
	ttl     time.Duration
}

type cacheEntry struct {
	key       string
	result    any
	timestamp time.Time
	prev      *cacheEntry
	next      *cacheEntry
}

// NewQueryCache creates a cache with the given max entries and TTL.
func NewQueryCache(maxSize int, ttl time.Duration) *QueryCache {
	return &QueryCache{
		entries: make(map[string]*cacheEntry, maxSize),
		maxSize: maxSize,
		ttl:     ttl,
	}
}

// Get returns a cached result and true if found and not expired.
// Get does NOT promote — promotion happens on Set only.
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

	if e, ok := c.entries[key]; ok {
		e.result = result
		e.timestamp = time.Now()
		c.moveToTail(e)
		return
	}

	if len(c.entries) >= c.maxSize {
		if oldest := c.head; oldest != nil {
			c.unlink(oldest)
			delete(c.entries, oldest.key)
		}
	}

	e := &cacheEntry{key: key, result: result, timestamp: time.Now()}
	c.entries[key] = e
	c.appendTail(e)
}

// Invalidate clears all cached entries.
func (c *QueryCache) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]*cacheEntry, c.maxSize)
	c.head = nil
	c.tail = nil
}

// Len returns the number of cached entries.
func (c *QueryCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}

// appendTail links e as the new MRU end. Caller holds c.mu.
func (c *QueryCache) appendTail(e *cacheEntry) {
	e.prev = c.tail
	e.next = nil
	if c.tail != nil {
		c.tail.next = e
	}
	c.tail = e
	if c.head == nil {
		c.head = e
	}
}

// unlink removes e from the list. Caller holds c.mu.
func (c *QueryCache) unlink(e *cacheEntry) {
	if e.prev != nil {
		e.prev.next = e.next
	} else {
		c.head = e.next
	}
	if e.next != nil {
		e.next.prev = e.prev
	} else {
		c.tail = e.prev
	}
	e.prev = nil
	e.next = nil
}

// moveToTail promotes e to the MRU end. Caller holds c.mu.
func (c *QueryCache) moveToTail(e *cacheEntry) {
	if e == c.tail {
		return
	}
	c.unlink(e)
	c.appendTail(e)
}
