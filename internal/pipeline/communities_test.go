package pipeline

import (
	"sort"
	"strings"
	"testing"
)

func canonicalNamedCommunities(
	communities map[int][]int64,
	names map[int64]string,
) string {
	groups := make([]string, 0, len(communities))
	for _, members := range communities {
		group := make([]string, 0, len(members))
		for _, id := range members {
			group = append(group, names[id])
		}
		sort.Strings(group)
		groups = append(groups, strings.Join(group, ","))
	}
	sort.Strings(groups)
	return strings.Join(groups, "|")
}

func namedGraph(
	names []string,
	ids []int64,
	edges [][2]string,
) (map[int64]map[int64]bool, map[int64]bool, map[int64]string, []int64) {
	byName := make(map[string]int64, len(names))
	nameByID := make(map[int64]string, len(names))
	all := make(map[int64]bool, len(names))
	for i, name := range names {
		byName[name] = ids[i]
		nameByID[ids[i]] = name
		all[ids[i]] = true
	}
	adj := make(map[int64]map[int64]bool, len(names))
	for _, edge := range edges {
		a, b := byName[edge[0]], byName[edge[1]]
		if adj[a] == nil {
			adj[a] = make(map[int64]bool)
		}
		if adj[b] == nil {
			adj[b] = make(map[int64]bool)
		}
		adj[a][b] = true
		adj[b][a] = true
	}
	ordered := append([]int64(nil), ids...)
	return adj, all, nameByID, ordered
}

func TestLouvainDeterministicAcrossDatabaseIDs(t *testing.T) {
	names := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	edges := [][2]string{
		{"a", "b"}, {"a", "c"}, {"b", "c"}, {"c", "d"},
		{"d", "e"}, {"e", "f"}, {"d", "f"}, {"f", "g"},
		{"g", "h"}, {"h", "a"},
	}
	adjA, allA, namesA, orderA := namedGraph(
		names, []int64{1, 2, 3, 4, 5, 6, 7, 8}, edges,
	)
	adjB, allB, namesB, orderB := namedGraph(
		names, []int64{81, 12, 73, 24, 65, 36, 57, 48}, edges,
	)

	gotA := canonicalNamedCommunities(
		louvainCommunitiesInOrder(adjA, allA, orderA), namesA,
	)
	gotB := canonicalNamedCommunities(
		louvainCommunitiesInOrder(adjB, allB, orderB), namesB,
	)
	if gotA != gotB {
		t.Fatalf("same named graph changed communities after ID churn: %q != %q", gotA, gotB)
	}
}

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
