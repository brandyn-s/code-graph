package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// TestBulkInsertNodesHonorsContextCancellation pins the contract that
// BulkInsertNodes aborts at the next chunk boundary when the supplied
// context is cancelled. Pre-fix the ctx parameter was inert — every
// chunk ran to completion regardless of caller cancellation.
//
// Constructs a payload large enough to require multiple chunks
// (nodesBatchSize = 500 currently), cancels the context after seeding,
// and asserts the call returns context.Canceled.
func TestBulkInsertNodesHonorsContextCancellation(t *testing.T) {
	s, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer s.Close()
	if err := s.UpsertProject("p", "/tmp/p"); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}

	// nodesBatchSize is unexported but small; build 2x that so the loop
	// has at least one chunk boundary to honor cancellation at.
	nodes := make([]*Node, nodesBatchSize*2)
	for i := range nodes {
		nodes[i] = &Node{
			Project: "p", Label: "Function", Name: fmt.Sprintf("F%d", i),
			QualifiedName: fmt.Sprintf("p.F%d", i),
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediately cancel — first ctx.Err() check fires
	err = s.BulkInsertNodes(ctx, nodes)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	// And no nodes should have been inserted (cancel fired before the
	// first chunk).
	n, _ := s.CountNodes("p")
	if n != 0 {
		t.Errorf("expected 0 nodes inserted on immediate-cancel, got %d", n)
	}
}

// TestBulkInsertEdgesHonorsContextCancellation mirrors the nodes test
// for the edges path.
func TestBulkInsertEdgesHonorsContextCancellation(t *testing.T) {
	s, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer s.Close()
	if err := s.UpsertProject("p", "/tmp/p"); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}
	a, _ := s.UpsertNode(&Node{Project: "p", Label: "Function", Name: "A", QualifiedName: "p.A"})
	b, _ := s.UpsertNode(&Node{Project: "p", Label: "Function", Name: "B", QualifiedName: "p.B"})

	edges := make([]*Edge, edgesBatchSize*2)
	for i := range edges {
		edges[i] = &Edge{Project: "p", SourceID: a, TargetID: b, Type: fmt.Sprintf("T%d", i)}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = s.BulkInsertEdges(ctx, edges)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// TestBulkInsertNodesCompletesWhenCtxLive confirms the happy path —
// when ctx is not cancelled, the bulk insert proceeds to completion.
func TestBulkInsertNodesCompletesWhenCtxLive(t *testing.T) {
	s, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer s.Close()
	if err := s.UpsertProject("p", "/tmp/p"); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}
	nodes := []*Node{
		{Project: "p", Label: "Function", Name: "A", QualifiedName: "p.A"},
		{Project: "p", Label: "Function", Name: "B", QualifiedName: "p.B"},
	}
	if err := s.BulkInsertNodes(context.Background(), nodes); err != nil {
		t.Fatalf("BulkInsertNodes: %v", err)
	}
	n, _ := s.CountNodes("p")
	if n != 2 {
		t.Errorf("expected 2 nodes, got %d", n)
	}
}
