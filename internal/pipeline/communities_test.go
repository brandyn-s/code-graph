package pipeline

import "testing"

func TestGroupAndFilterMinSize3(t *testing.T) {
	nodeCommunity := map[int64]int{
		1: 0, 2: 0, 3: 0, // community 0: size 3
		4: 1, 5: 1, // community 1: size 2 (should be filtered)
		6: 2, // community 2: singleton (should be filtered)
	}
	result := groupAndFilterMinSize(nodeCommunity, 3)
	if len(result) != 1 {
		t.Errorf("expected 1 community (min size 3), got %d", len(result))
	}
	// Verify the surviving community has all 3 members
	for _, members := range result {
		if len(members) != 3 {
			t.Errorf("expected surviving community to have 3 members, got %d", len(members))
		}
	}
}

func TestGroupAndFilterMinSize2(t *testing.T) {
	nodeCommunity := map[int64]int{
		1: 0, 2: 0, 3: 0, // community 0: size 3
		4: 1, 5: 1, // community 1: size 2
		6: 2, // community 2: singleton (should be filtered)
	}
	result := groupAndFilterMinSize(nodeCommunity, 2)
	if len(result) != 2 {
		t.Errorf("expected 2 communities (min size 2), got %d", len(result))
	}
}

func TestGroupAndFilterMinSizeAllFiltered(t *testing.T) {
	nodeCommunity := map[int64]int{
		1: 0,
		2: 1,
		3: 2,
	}
	result := groupAndFilterMinSize(nodeCommunity, 2)
	if len(result) != 0 {
		t.Errorf("expected 0 communities (all singletons filtered at min 2), got %d", len(result))
	}
}

func TestLouvainTwoClusters(t *testing.T) {
	// Two clear clusters connected by a single bridge edge (node 4 <-> node 5)
	adj := map[int64]map[int64]bool{
		1: {2: true, 3: true, 4: true},
		2: {1: true, 3: true, 4: true},
		3: {1: true, 2: true, 4: true},
		4: {1: true, 2: true, 3: true, 5: true},
		5: {4: true, 6: true, 7: true, 8: true},
		6: {5: true, 7: true, 8: true},
		7: {5: true, 6: true, 8: true},
		8: {5: true, 6: true, 7: true},
	}
	allNodes := map[int64]bool{}
	for k := range adj {
		allNodes[k] = true
	}

	communities := louvainCommunities(adj, allNodes)
	if len(communities) < 1 || len(communities) > 3 {
		t.Errorf("expected 1-3 communities from 2-cluster graph, got %d", len(communities))
	}
	// Each returned community should have at least 3 members (our new minimum)
	for idx, members := range communities {
		if len(members) < 3 {
			t.Errorf("community %d has %d members, expected at least 3", idx, len(members))
		}
	}
}

func TestLouvainSingleClique(t *testing.T) {
	// A single fully-connected clique of 5 nodes
	adj := map[int64]map[int64]bool{
		1: {2: true, 3: true, 4: true, 5: true},
		2: {1: true, 3: true, 4: true, 5: true},
		3: {1: true, 2: true, 4: true, 5: true},
		4: {1: true, 2: true, 3: true, 5: true},
		5: {1: true, 2: true, 3: true, 4: true},
	}
	allNodes := map[int64]bool{}
	for k := range adj {
		allNodes[k] = true
	}

	communities := louvainCommunities(adj, allNodes)
	if len(communities) != 1 {
		t.Errorf("expected 1 community for single clique, got %d", len(communities))
	}
	if len(communities) == 1 {
		for _, members := range communities {
			if len(members) != 5 {
				t.Errorf("expected 5 members in clique community, got %d", len(members))
			}
		}
	}
}
