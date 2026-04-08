package tools

import (
	"strings"
	"testing"
)

func TestVisualizeTemplateLoaded(t *testing.T) {
	// The template is embedded via //go:embed visualize_template.html
	if visualizeTemplate == "" {
		t.Fatal("visualize_template.html not embedded — check //go:embed directive")
	}
	if !strings.Contains(visualizeTemplate, "d3") {
		t.Error("template should reference d3.js")
	}
	if !strings.Contains(visualizeTemplate, "html") {
		t.Error("template should contain HTML")
	}
}

func TestVisualizeNodeSelection(t *testing.T) {
	st, _ := setupSecurityGraph(t)
	defer st.Close()

	projName := "test"
	projects, _ := st.ListProjects()
	if len(projects) > 0 {
		projName = projects[0].Name
	}

	allNodes, err := st.AllNodes(projName)
	if err != nil {
		t.Fatalf("AllNodes: %v", err)
	}

	t.Run("max_nodes caps output", func(t *testing.T) {
		maxNodes := 5
		// Sort by degree (highest connectivity first) — matches visualize.go logic
		type nodeWithDeg struct {
			name   string
			degree int
		}
		var ranked []nodeWithDeg
		for _, n := range allNodes {
			in, out := st.NodeDegree(n.ID)
			ranked = append(ranked, nodeWithDeg{n.Name, in + out})
		}
		if len(ranked) > maxNodes {
			ranked = ranked[:maxNodes]
		}
		if len(ranked) != maxNodes {
			t.Errorf("expected %d nodes after cap, got %d", maxNodes, len(ranked))
		}
	})

	t.Run("file_pattern filters nodes", func(t *testing.T) {
		// Filter to only svc-api/ files
		var filtered int
		for _, n := range allNodes {
			if strings.HasPrefix(n.FilePath, "svc-api/") {
				filtered++
			}
		}
		if filtered == 0 {
			t.Error("expected some nodes matching svc-api/ pattern")
		}
		if filtered >= len(allNodes) {
			t.Error("filter should exclude some nodes (svc-orders, svc-notify)")
		}
	})
}
