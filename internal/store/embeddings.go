package store

import (
	"context"
	"encoding/binary"
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
	mu       sync.RWMutex
	project  string
	nodeIDs  []int64
	names    []string
	qnames   []string
	labels   []string
	files    []string
	vectors  [][]float32 // [N][dim]
	dim      int
	loaded   bool
}

var embedCache = &embeddingCache{}

// UpsertEmbedding stores or updates the embedding vector for a node.
func (s *Store) UpsertEmbedding(nodeID int64, model string, vec []float32) error {
	blob := float32sToBlob(vec)
	_, err := s.db.ExecContext(context.Background(),
		`INSERT INTO node_embeddings (node_id, model, embedding)
		 VALUES (?, ?, ?)
		 ON CONFLICT(node_id) DO UPDATE SET model=excluded.model, embedding=excluded.embedding`,
		nodeID, model, blob)
	return err
}

// UpsertEmbeddingBatch stores embeddings for multiple nodes in a single transaction.
func (s *Store) UpsertEmbeddingBatch(nodeIDs []int64, model string, vecs [][]float32) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(
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
	return tx.Commit()
}

// CosineSearch finds the top-k nodes most similar to the query vector.
// Uses in-memory dot product (vectors are L2-normalized).
func (s *Store) CosineSearch(project string, queryVec []float32, limit int) ([]EmbeddingResult, error) {
	embedCache.mu.RLock()
	if !embedCache.loaded || embedCache.project != project {
		embedCache.mu.RUnlock()
		if err := s.loadEmbeddingCache(project); err != nil {
			return nil, err
		}
		embedCache.mu.RLock()
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

// InvalidateEmbeddingCache clears the in-memory cache (call after reindex).
func (s *Store) InvalidateEmbeddingCache() {
	embedCache.mu.Lock()
	defer embedCache.mu.Unlock()
	embedCache.loaded = false
	embedCache.vectors = nil
	embedCache.nodeIDs = nil
}

// EmbeddingCount returns the number of embeddings stored for a project.
func (s *Store) EmbeddingCount(project string) (int, error) {
	var count int
	err := s.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM node_embeddings ne
		 JOIN nodes n ON ne.node_id = n.id
		 WHERE n.project = ?`, project).Scan(&count)
	return count, err
}

// loadEmbeddingCache loads all embeddings for a project into memory.
func (s *Store) loadEmbeddingCache(project string) error {
	embedCache.mu.Lock()
	defer embedCache.mu.Unlock()

	rows, err := s.db.QueryContext(context.Background(),
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
