package tools

import (
	"testing"
)

func TestChangeCouplingClassification(t *testing.T) {
	st, _ := setupSecurityGraph(t)
	defer st.Close()

	projName := "test"
	projects, _ := st.ListProjects()
	if len(projects) > 0 {
		projName = projects[0].Name
	}

	t.Run("finds FILE_CHANGES_WITH edges", func(t *testing.T) {
		edges, err := st.FindEdgesByType(projName, "FILE_CHANGES_WITH")
		if err != nil {
			t.Fatalf("FindEdgesByType: %v", err)
		}
		// We inserted 2 FILE_CHANGES_WITH edges:
		// AppConfig <-> authenticate (score 0.7, same crate svc-api)
		// handle_request <-> process_order (score 0.5, cross-crate svc-api <-> svc-orders)
		if len(edges) < 2 {
			t.Errorf("expected ≥2 FILE_CHANGES_WITH edges, got %d", len(edges))
		}
	})

	t.Run("classifies same-crate as logical", func(t *testing.T) {
		edges, _ := st.FindEdgesByType(projName, "FILE_CHANGES_WITH")

		for _, e := range edges {
			src, _ := st.FindNodeByID(e.SourceID)
			tgt, _ := st.FindNodeByID(e.TargetID)
			if src == nil || tgt == nil {
				continue
			}

			crate1 := extractTopLevelCrate(src.FilePath)
			crate2 := extractTopLevelCrate(tgt.FilePath)

			// AppConfig (svc-api/src/config.rs) <-> authenticate (svc-api/src/auth.rs)
			if src.Name == "AppConfig" && tgt.Name == "authenticate" {
				if crate1 != crate2 {
					t.Errorf("expected same crate for config<->auth, got %q vs %q", crate1, crate2)
				}
				return
			}
		}
		t.Error("did not find AppConfig <-> authenticate edge")
	})

	t.Run("classifies cross-crate as accidental", func(t *testing.T) {
		edges, _ := st.FindEdgesByType(projName, "FILE_CHANGES_WITH")

		for _, e := range edges {
			src, _ := st.FindNodeByID(e.SourceID)
			tgt, _ := st.FindNodeByID(e.TargetID)
			if src == nil || tgt == nil {
				continue
			}

			crate1 := extractTopLevelCrate(src.FilePath)
			crate2 := extractTopLevelCrate(tgt.FilePath)

			// handle_request (svc-api) <-> process_order (svc-orders)
			if src.Name == "handle_request" && tgt.Name == "process_order" {
				if crate1 == crate2 {
					t.Errorf("expected different crates, got both %q", crate1)
				}
				return
			}
		}
		t.Error("did not find handle_request <-> process_order edge")
	})

	t.Run("coupling score extracted from properties", func(t *testing.T) {
		edges, _ := st.FindEdgesByType(projName, "FILE_CHANGES_WITH")

		for _, e := range edges {
			src, _ := st.FindNodeByID(e.SourceID)
			if src == nil {
				continue
			}
			if src.Name == "AppConfig" {
				score := 0.0
				if e.Properties != nil {
					if s, ok := e.Properties["coupling_score"].(float64); ok {
						score = s
					}
				}
				if score != 0.7 {
					t.Errorf("expected coupling_score 0.7, got %f", score)
				}
				return
			}
		}
		t.Error("did not find AppConfig edge to check score")
	})
}

func TestChangeCouplingMinScoreFilter(t *testing.T) {
	st, _ := setupSecurityGraph(t)
	defer st.Close()

	projName := "test"
	projects, _ := st.ListProjects()
	if len(projects) > 0 {
		projName = projects[0].Name
	}

	edges, _ := st.FindEdgesByType(projName, "FILE_CHANGES_WITH")

	t.Run("high min_score filters weak couplings", func(t *testing.T) {
		// score 0.5 should be filtered at min_score 0.6
		count := 0
		for _, e := range edges {
			score := 0.0
			if e.Properties != nil {
				if s, ok := e.Properties["coupling_score"].(float64); ok {
					score = s
				}
			}
			if score >= 0.6 {
				count++
			}
		}
		// Only the 0.7 edge should survive
		if count != 1 {
			t.Errorf("expected 1 edge with score ≥ 0.6, got %d", count)
		}
	})

	t.Run("low min_score includes all", func(t *testing.T) {
		count := 0
		for _, e := range edges {
			score := 0.0
			if e.Properties != nil {
				if s, ok := e.Properties["coupling_score"].(float64); ok {
					score = s
				}
			}
			if score >= 0.1 {
				count++
			}
		}
		if count < 2 {
			t.Errorf("expected ≥2 edges with score ≥ 0.1, got %d", count)
		}
	})
}
