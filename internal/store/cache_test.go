package store

import (
	"testing"
	"time"
)

func TestQueryCacheHitAndMiss(t *testing.T) {
	c := NewQueryCache(100, 5*time.Minute)

	// Miss
	_, hit := c.Get("key1")
	if hit {
		t.Fatal("expected miss on empty cache")
	}

	// Set and hit
	c.Set("key1", []Node{{Name: "authenticate"}})
	result, hit := c.Get("key1")
	if !hit {
		t.Fatal("expected hit after Set")
	}
	nodes := result.([]Node)
	if len(nodes) != 1 || nodes[0].Name != "authenticate" {
		t.Fatalf("unexpected result: %v", nodes)
	}
}

func TestQueryCacheExpiration(t *testing.T) {
	c := NewQueryCache(100, 10*time.Millisecond)
	c.Set("key1", "value1")

	time.Sleep(20 * time.Millisecond)

	_, hit := c.Get("key1")
	if hit {
		t.Fatal("expected miss after TTL expiration")
	}
}

func TestQueryCacheEviction(t *testing.T) {
	c := NewQueryCache(2, 5*time.Minute)
	c.Set("key1", "val1")
	c.Set("key2", "val2")
	c.Set("key3", "val3") // should evict key1

	_, hit := c.Get("key1")
	if hit {
		t.Fatal("expected key1 evicted")
	}
	_, hit = c.Get("key2")
	if !hit {
		t.Fatal("expected key2 still present")
	}
	_, hit = c.Get("key3")
	if !hit {
		t.Fatal("expected key3 present")
	}
}

func TestQueryCacheInvalidate(t *testing.T) {
	c := NewQueryCache(100, 5*time.Minute)
	c.Set("key1", "val1")
	c.Set("key2", "val2")

	c.Invalidate()

	if c.Len() != 0 {
		t.Fatalf("expected empty cache after invalidate, got %d", c.Len())
	}
	_, hit := c.Get("key1")
	if hit {
		t.Fatal("expected miss after invalidate")
	}
}

func TestQueryCacheLRUOrder(t *testing.T) {
	c := NewQueryCache(2, 5*time.Minute)
	c.Set("key1", "val1")
	c.Set("key2", "val2")

	// Re-Set key1 to make it recently used
	c.Set("key1", "val1_updated")

	// key2 should be evicted (oldest), not key1
	c.Set("key3", "val3")

	_, hit := c.Get("key1")
	if !hit {
		t.Fatal("expected key1 still present (recently updated)")
	}
	_, hit = c.Get("key2")
	if hit {
		t.Fatal("expected key2 evicted (oldest)")
	}
	_, hit = c.Get("key3")
	if !hit {
		t.Fatal("expected key3 present")
	}
}
