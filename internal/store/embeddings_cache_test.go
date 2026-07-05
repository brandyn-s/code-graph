package store

import (
	"fmt"
	"sync"
	"testing"
)

// seedProjectWithEmbedding inserts one embeddable node + its embedding so a
// project has a non-empty, loadable embedding cache.
func seedProjectWithEmbedding(t *testing.T, s *Store, project string, vec []float32) {
	t.Helper()
	if err := s.UpsertProject(project, "/tmp/"+project); err != nil {
		t.Fatalf("UpsertProject %s: %v", project, err)
	}
	id, err := s.UpsertNode(&Node{
		Project:       project,
		Label:         "Function",
		Name:          "f_" + project,
		QualifiedName: project + ".f",
		FilePath:      project + "/a.go",
	})
	if err != nil {
		t.Fatalf("UpsertNode %s: %v", project, err)
	}
	if err := s.UpsertEmbedding(id, "voyage-code-3", vec); err != nil {
		t.Fatalf("UpsertEmbedding %s: %v", project, err)
	}
}

// TestCosineSearchNoCrossProjectBleed pins the TOCTOU fix: a CosineSearch
// for project A must never return project B's nodes, even under concurrent
// cross-project searches that ping-pong the single-slot cache. Before the
// fix, the load-then-relock gap let a concurrent load of B slip in between
// A's load and A's read lock, so A's search scored over B's vectors and
// returned B's nodes with no error. Run with -race for the strongest signal.
func TestCosineSearchNoCrossProjectBleed(t *testing.T) {
	s, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer s.Close()

	seedProjectWithEmbedding(t, s, "projA", []float32{1, 0, 0})
	seedProjectWithEmbedding(t, s, "projB", []float32{0, 1, 0})

	var wg sync.WaitGroup
	errCh := make(chan error, 200)
	check := func(project, wantQN string, vec []float32) {
		defer wg.Done()
		res, err := s.CosineSearch(project, vec, 10)
		if err != nil {
			return // a bounded-contention error is acceptable; a wrong-project result is not
		}
		for _, r := range res {
			if r.QName != wantQN {
				errCh <- fmt.Errorf("%s search returned %q, want %q", project, r.QName, wantQN)
				return
			}
		}
	}
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go check("projA", "projA.f", []float32{1, 0, 0})
		go check("projB", "projB.f", []float32{0, 1, 0})
	}
	wg.Wait()
	close(errCh)
	for e := range errCh {
		t.Fatal(e)
	}
}
