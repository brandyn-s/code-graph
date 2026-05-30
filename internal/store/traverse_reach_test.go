package store

import "testing"

// buildReachGraph builds:
//
//	src ─CALLS→ a ─CALLS→ sink
//	src ─CALLS→ b ─CALLS→ sink   (sanitizer-free alternative when a is excluded)
//	src ─CALLS→ deep ─CALLS→ d2 ─CALLS→ d3   (for depth-bound tests)
func buildReachGraph(t *testing.T) (*Store, map[string]int64) {
	t.Helper()
	st, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	if err := st.UpsertProject("test", "/tmp/test"); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}
	ids := map[string]int64{}
	for _, name := range []string{"src", "a", "b", "sink", "deep", "d2", "d3"} {
		id, upErr := st.UpsertNode(&Node{Project: "test", Label: "Function", Name: name, QualifiedName: "test." + name})
		if upErr != nil {
			t.Fatalf("UpsertNode %s: %v", name, upErr)
		}
		ids[name] = id
	}
	for _, e := range [][2]string{
		{"src", "a"}, {"a", "sink"},
		{"src", "b"}, {"b", "sink"},
		{"src", "deep"}, {"deep", "d2"}, {"d2", "d3"},
	} {
		if _, insErr := st.InsertEdge(&Edge{Project: "test", SourceID: ids[e[0]], TargetID: ids[e[1]], Type: "CALLS"}); insErr != nil {
			t.Fatalf("InsertEdge %s->%s: %v", e[0], e[1], insErr)
		}
	}
	return st, ids
}

func TestReachableExcluding(t *testing.T) {
	st, ids := buildReachGraph(t)
	defer st.Close()
	ct := []string{"CALLS"}

	t.Run("reachable with no exclusions", func(t *testing.T) {
		ok, err := st.ReachableExcluding(ids["src"], ids["sink"], "outbound", ct, 4, nil)
		if err != nil || !ok {
			t.Fatalf("expected reachable, got ok=%v err=%v", ok, err)
		}
	})

	t.Run("excluding only one branch still reachable via the other", func(t *testing.T) {
		ok, err := st.ReachableExcluding(ids["src"], ids["sink"], "outbound", ct, 4, map[int64]bool{ids["a"]: true})
		if err != nil || !ok {
			t.Fatalf("expected reachable via b, got ok=%v err=%v", ok, err)
		}
	})

	t.Run("excluding both intermediaries cuts all paths", func(t *testing.T) {
		ok, err := st.ReachableExcluding(ids["src"], ids["sink"], "outbound", ct, 4, map[int64]bool{ids["a"]: true, ids["b"]: true})
		if err != nil || ok {
			t.Fatalf("expected unreachable when both branches cut, got ok=%v err=%v", ok, err)
		}
	})

	t.Run("respects maxDepth", func(t *testing.T) {
		// src->deep->d2->d3 is 3 hops; depth 2 cannot reach d3.
		ok, _ := st.ReachableExcluding(ids["src"], ids["d3"], "outbound", ct, 2, nil)
		if ok {
			t.Error("d3 should be unreachable within depth 2")
		}
		ok, _ = st.ReachableExcluding(ids["src"], ids["d3"], "outbound", ct, 3, nil)
		if !ok {
			t.Error("d3 should be reachable within depth 3")
		}
	})

	t.Run("inbound direction", func(t *testing.T) {
		ok, err := st.ReachableExcluding(ids["sink"], ids["src"], "inbound", ct, 4, nil)
		if err != nil || !ok {
			t.Fatalf("expected sink to reach src inbound, got ok=%v err=%v", ok, err)
		}
	})
}

func TestShortestPath(t *testing.T) {
	st, ids := buildReachGraph(t)
	defer st.Close()
	ct := []string{"CALLS"}

	t.Run("finds a 2-hop path", func(t *testing.T) {
		path, err := st.ShortestPath(ids["src"], ids["sink"], "outbound", ct, 4)
		if err != nil {
			t.Fatalf("ShortestPath: %v", err)
		}
		if len(path) != 3 || path[0] != ids["src"] || path[2] != ids["sink"] {
			t.Fatalf("expected [src, *, sink], got %v", path)
		}
		if path[1] != ids["a"] && path[1] != ids["b"] {
			t.Errorf("middle node should be a or b, got %d", path[1])
		}
	})

	t.Run("nil when beyond depth bound", func(t *testing.T) {
		path, err := st.ShortestPath(ids["src"], ids["d3"], "outbound", ct, 2)
		if err != nil {
			t.Fatalf("ShortestPath: %v", err)
		}
		if path != nil {
			t.Errorf("expected nil path beyond depth, got %v", path)
		}
	})
}
