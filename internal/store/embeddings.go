package store

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"sync"
)

// EmbeddingResult holds a node ID and its cosine similarity score.
type EmbeddingResult struct {
	NodeID   int64   `json:"node_id"`
	Name     string  `json:"name"`
	QName    string  `json:"qualified_name"`
	Label    string  `json:"label"`
	FilePath string  `json:"file_path"`
	Score    float64 `json:"score"`
}

// embeddingCache is an in-memory cache of all embeddings for a project.
// Loaded lazily on first semantic search, invalidated on reindex.
type embeddingCache struct {
	mu      sync.RWMutex
	project string
	nodeIDs []int64
	names   []string
	qnames  []string
	labels  []string
	files   []string
	vectors [][]float32 // [N][dim]
	dim     int
	loaded  bool
}

var embedCache = &embeddingCache{}

// acquireEmbedCache returns with embedCache.mu READ-LOCKED and the
// single-slot cache loaded for project; the caller must RUnlock (defer).
// On error, no lock is held.
//
// The naive load-then-relock sequence this replaces was a TOCTOU race:
// between loadEmbeddingCache(project) returning and the read lock being
// re-acquired, a concurrent call could load a DIFFERENT project into the
// slot — the caller then computed similarity over the wrong project's
// vectors and returned those nodes as results, with no error. Re-checking
// the loaded project under the read lock closes the race; the bounded
// retry converts a pathological ping-pong between concurrent cross-project
// callers into an explicit error instead of a livelock.
func (s *Store) acquireEmbedCache(project string) error {
	const maxAttempts = 5
	for attempt := 0; attempt < maxAttempts; attempt++ {
		embedCache.mu.RLock()
		if embedCache.loaded && embedCache.project == project {
			return nil // read lock intentionally held for the caller
		}
		embedCache.mu.RUnlock()
		if err := s.loadEmbeddingCache(project); err != nil {
			return err
		}
	}
	return fmt.Errorf("embedding cache contention: project %q displaced %d times during acquisition", project, maxAttempts)
}

// UpsertEmbedding stores or updates the embedding vector for a node.
// Uses s.q (the active Querier) so this can run inside Store.WithTransaction
// without deadlocking on the single-connection pool — same pattern as
// UpsertEmbeddingBatch (see comment there for the deadlock story).
func (s *Store) UpsertEmbedding(nodeID int64, model string, vec []float32) error {
	blob := float32sToBlob(vec)
	_, err := s.q.ExecContext(context.Background(),
		`INSERT INTO node_embeddings (node_id, model, embedding)
		 VALUES (?, ?, ?)
		 ON CONFLICT(node_id) DO UPDATE SET model=excluded.model, embedding=excluded.embedding`,
		nodeID, model, blob)
	return err
}

// UpsertEmbeddingBatch stores embeddings for multiple nodes.
//
// Uses the store's active Querier (s.q) rather than calling s.db.Begin() so
// that when this is invoked from inside Store.WithTransaction (as the whole
// indexing pipeline does), the writes participate in the outer tx instead of
// opening a nested one. A nested tx on the SetMaxOpenConns(1) pool deadlocks
// waiting for the write lock held by the outer tx — this was the root cause
// of TestMemoryStability hanging and of the multi-minute stalls observed at
// "phase=embeddings pct=97" during live indexing (2026-04-22).
//
// When called outside a tx (s.q == s.db), each prepared Exec auto-commits.
// That is fine for this call site: UpsertEmbeddingBatch is invoked from the
// embeddings pass which runs inside the pipeline's WithTransaction; the only
// other caller would be ad-hoc tooling where atomicity across 64 rows is
// not a correctness requirement.
func (s *Store) UpsertEmbeddingBatch(nodeIDs []int64, model string, vecs [][]float32) error {
	stmt, err := s.q.Prepare(
		`INSERT INTO node_embeddings (node_id, model, embedding)
		 VALUES (?, ?, ?)
		 ON CONFLICT(node_id) DO UPDATE SET model=excluded.model, embedding=excluded.embedding`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for i, nodeID := range nodeIDs {
		blob := float32sToBlob(vecs[i])
		if _, err := stmt.Exec(nodeID, model, blob); err != nil {
			return err
		}
	}
	return nil
}

// CosineSearch finds the top-k nodes most similar to the query vector.
// Uses in-memory dot product (vectors are L2-normalized).
func (s *Store) CosineSearch(project string, queryVec []float32, limit int) ([]EmbeddingResult, error) {
	if err := s.acquireEmbedCache(project); err != nil {
		return nil, err
	}
	defer embedCache.mu.RUnlock()

	n := len(embedCache.nodeIDs)
	if n == 0 {
		return nil, nil
	}

	// Normalize query vector
	qNorm := l2Norm(queryVec)
	if qNorm == 0 {
		return nil, nil
	}
	normalizedQ := make([]float32, len(queryVec))
	for i, v := range queryVec {
		normalizedQ[i] = v / qNorm
	}

	// Compute dot products (cosine similarity since vectors are normalized)
	type scored struct {
		idx   int
		score float64
	}
	scores := make([]scored, n)
	for i := 0; i < n; i++ {
		var dot float32
		vec := embedCache.vectors[i]
		for j := 0; j < embedCache.dim && j < len(normalizedQ); j++ {
			dot += vec[j] * normalizedQ[j]
		}
		scores[i] = scored{idx: i, score: float64(dot)}
	}

	// Sort by descending score
	sort.Slice(scores, func(a, b int) bool {
		return scores[a].score > scores[b].score
	})

	// Return top-k
	if limit > n {
		limit = n
	}
	results := make([]EmbeddingResult, limit)
	for i := 0; i < limit; i++ {
		idx := scores[i].idx
		results[i] = EmbeddingResult{
			NodeID:   embedCache.nodeIDs[idx],
			Name:     embedCache.names[idx],
			QName:    embedCache.qnames[idx],
			Label:    embedCache.labels[idx],
			FilePath: embedCache.files[idx],
			Score:    scores[i].score,
		}
	}
	return results, nil
}

// FindSimilarNodes returns the top-k embedded nodes most cosine-similar to
// the given node's own embedding, excluding the node itself. Returns
// nil, nil when the node has no embedding or no other embeddings exist
// for the project.
//
// Reuses the same in-memory, L2-normalized embedding cache that
// CosineSearch uses, so repeated calls across all nodes in a pass do not
// re-scan SQLite. The cost is O(N * dim) per query where N is embedded
// nodes in the project — ~546 nodes × 2048-dim embeddings on rmf-corsair
// measures at <1ms per query on a modern CPU.
func (s *Store) FindSimilarNodes(project string, nodeID int64, limit int) ([]EmbeddingResult, error) {
	if err := s.acquireEmbedCache(project); err != nil {
		return nil, err
	}
	defer embedCache.mu.RUnlock()

	n := len(embedCache.nodeIDs)
	if n < 2 {
		return nil, nil
	}

	// Locate the query node's index in the cache.
	queryIdx := -1
	for i, id := range embedCache.nodeIDs {
		if id == nodeID {
			queryIdx = i
			break
		}
	}
	if queryIdx == -1 {
		return nil, nil // node has no embedding
	}
	queryVec := embedCache.vectors[queryIdx]

	// Dot product against every other vector (cache vectors are already
	// L2-normalized when loaded — see loadEmbeddingCache), skipping self.
	type scored struct {
		idx   int
		score float64
	}
	scores := make([]scored, 0, n-1)
	for i := 0; i < n; i++ {
		if i == queryIdx {
			continue
		}
		var dot float32
		vec := embedCache.vectors[i]
		for j := 0; j < embedCache.dim && j < len(queryVec); j++ {
			dot += vec[j] * queryVec[j]
		}
		scores = append(scores, scored{idx: i, score: float64(dot)})
	}

	sort.Slice(scores, func(a, b int) bool {
		return scores[a].score > scores[b].score
	})

	if limit > len(scores) {
		limit = len(scores)
	}
	results := make([]EmbeddingResult, limit)
	for i := 0; i < limit; i++ {
		idx := scores[i].idx
		results[i] = EmbeddingResult{
			NodeID:   embedCache.nodeIDs[idx],
			Name:     embedCache.names[idx],
			QName:    embedCache.qnames[idx],
			Label:    embedCache.labels[idx],
			FilePath: embedCache.files[idx],
			Score:    scores[i].score,
		}
	}
	return results, nil
}

// IterEmbeddedNodeIDs returns the list of node IDs that currently have
// embeddings for this project, loading the cache if needed. Enables the
// similarity pass to enumerate every embedded node without re-querying
// SQLite.
func (s *Store) IterEmbeddedNodeIDs(project string) ([]int64, error) {
	if err := s.acquireEmbedCache(project); err != nil {
		return nil, err
	}
	defer embedCache.mu.RUnlock()

	out := make([]int64, len(embedCache.nodeIDs))
	copy(out, embedCache.nodeIDs)
	return out, nil
}

// InvalidateEmbeddingCache clears the in-memory cache (call after reindex).
func (s *Store) InvalidateEmbeddingCache() {
	embedCache.mu.Lock()
	defer embedCache.mu.Unlock()
	embedCache.loaded = false
	embedCache.vectors = nil
	embedCache.nodeIDs = nil
}

// EmbeddingCount returns the number of embeddings stored for a project.
// Uses s.q so callers inside Store.WithTransaction don't deadlock on the
// single-connection pool.
func (s *Store) EmbeddingCount(project string) (int, error) {
	var count int
	err := s.q.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM node_embeddings ne
		 JOIN nodes n ON ne.node_id = n.id
		 WHERE n.project = ?`, project).Scan(&count)
	return count, err
}

// loadEmbeddingCache loads all embeddings for a project into memory.
// Uses s.q so callers inside Store.WithTransaction don't deadlock on the
// single-connection pool.
func (s *Store) loadEmbeddingCache(project string) error {
	embedCache.mu.Lock()
	defer embedCache.mu.Unlock()

	rows, err := s.q.QueryContext(context.Background(),
		`SELECT n.id, n.name, n.qualified_name, n.label, n.file_path, ne.embedding
		 FROM node_embeddings ne
		 JOIN nodes n ON ne.node_id = n.id
		 WHERE n.project = ?
		 ORDER BY n.id`, project)
	if err != nil {
		return err
	}
	defer rows.Close()

	var nodeIDs []int64
	var names, qnames, labels, files []string
	var vectors [][]float32
	dim := 0

	for rows.Next() {
		var id int64
		var name, qname, label, file string
		var blob []byte
		if err := rows.Scan(&id, &name, &qname, &label, &file, &blob); err != nil {
			return err
		}
		vec := blobToFloat32s(blob)
		if dim == 0 {
			dim = len(vec)
		}
		// L2-normalize for cosine similarity
		norm := l2Norm(vec)
		if norm > 0 {
			for i := range vec {
				vec[i] /= norm
			}
		}
		nodeIDs = append(nodeIDs, id)
		names = append(names, name)
		qnames = append(qnames, qname)
		labels = append(labels, label)
		files = append(files, file)
		vectors = append(vectors, vec)
	}

	embedCache.project = project
	embedCache.nodeIDs = nodeIDs
	embedCache.names = names
	embedCache.qnames = qnames
	embedCache.labels = labels
	embedCache.files = files
	embedCache.vectors = vectors
	embedCache.dim = dim
	embedCache.loaded = true

	return nil
}

// float32sToBlob converts a float32 slice to a byte slice (little-endian).
func float32sToBlob(vec []float32) []byte {
	blob := make([]byte, len(vec)*4)
	for i, v := range vec {
		bits := math.Float32bits(v)
		binary.LittleEndian.PutUint32(blob[i*4:], bits)
	}
	return blob
}

// blobToFloat32s converts a byte slice back to float32 slice.
func blobToFloat32s(blob []byte) []float32 {
	n := len(blob) / 4
	vec := make([]float32, n)
	for i := 0; i < n; i++ {
		bits := binary.LittleEndian.Uint32(blob[i*4:])
		vec[i] = math.Float32frombits(bits)
	}
	return vec
}

// l2Norm computes the L2 (Euclidean) norm of a vector.
func l2Norm(vec []float32) float32 {
	var sum float32
	for _, v := range vec {
		sum += v * v
	}
	return float32(math.Sqrt(float64(sum)))
}
