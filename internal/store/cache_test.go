package store

import (
	"fmt"
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
	nodes := result.([]Node) //nolint:errcheck // test: panics on type mismatch
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

// TestQueryCacheLRUOrderingAtScale exercises the linked-list bookkeeping
// across many keys. Catches off-by-one head/tail bugs that the n=2 LRU
// test cannot.
func TestQueryCacheLRUOrderingAtScale(t *testing.T) {
	const cap = 16
	c := NewQueryCache(cap, 5*time.Minute)

	// Fill to capacity in order 0..cap-1.
	for i := 0; i < cap; i++ {
		c.Set(fmt.Sprintf("k%02d", i), i)
	}

	// Touch the oldest half by re-setting them — now they should be MRU
	// and the originally-newest half should be the next eviction victims.
	for i := 0; i < cap/2; i++ {
		c.Set(fmt.Sprintf("k%02d", i), i+1000)
	}

	// Insert cap/2 fresh keys; the originally-newest half (cap/2..cap-1) should evict.
	for i := 0; i < cap/2; i++ {
		c.Set(fmt.Sprintf("new%02d", i), -i)
	}

	// Originally-oldest half (k00..k07) should still be present.
	for i := 0; i < cap/2; i++ {
		key := fmt.Sprintf("k%02d", i)
		v, hit := c.Get(key)
		if !hit {
			t.Fatalf("expected %s present after touch-then-insert", key)
		}
		if v.(int) != i+1000 {
			t.Fatalf("expected updated value %d for %s, got %v", i+1000, key, v)
		}
	}
	// Originally-newest half (k08..k15) should be evicted.
	for i := cap / 2; i < cap; i++ {
		key := fmt.Sprintf("k%02d", i)
		if _, hit := c.Get(key); hit {
			t.Fatalf("expected %s evicted", key)
		}
	}
	if c.Len() != cap {
		t.Fatalf("expected cache full at %d, got %d", cap, c.Len())
	}
}

// TestQueryCacheInvalidateThenRefill verifies that Invalidate clears the
// linked list cleanly so post-invalidate eviction still respects the cap.
func TestQueryCacheInvalidateThenRefill(t *testing.T) {
	c := NewQueryCache(3, 5*time.Minute)
	c.Set("a", 1)
	c.Set("b", 2)
	c.Invalidate()

	c.Set("c", 3)
	c.Set("d", 4)
	c.Set("e", 5)
	c.Set("f", 6) // should evict c
	if _, hit := c.Get("c"); hit {
		t.Fatal("expected c evicted after refill past cap")
	}
	if c.Len() != 3 {
		t.Fatalf("expected len=3 after refill, got %d", c.Len())
	}
}

// BenchmarkQueryCacheSetHot compares promote-to-MRU cost across cache
// sizes. The pre-refactor implementation was O(n) per Set on an existing
// key (linear scan over `order`); this benchmark documents the new O(1)
// behavior so a regression to the slice-based design would show up as
// non-flat ns/op as cache size grows.
func BenchmarkQueryCacheSetHot(b *testing.B) {
	for _, size := range []int{16, 256, 4096} {
		b.Run(fmt.Sprintf("size=%d", size), func(b *testing.B) {
			c := NewQueryCache(size, time.Hour)
			for i := 0; i < size; i++ {
				c.Set(fmt.Sprintf("k%d", i), i)
			}
			hotKey := "k0" // always at LRU end — worst case for linear scan
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				c.Set(hotKey, i)
			}
		})
	}
}
