#!/usr/bin/env python3
"""One-shot code-search query helper for the SweRank pre-filter pilot (Arm C).

Runs INSIDE code-search's own venv (it imports that repo's search modules);
the pilot orchestrator (armC_retrieval.py) shells out to it so neither
environment needs the other's dependencies.

stdout: JSON list of {relative_path, name, parent_name, chunk_type, score}
in rank order. Reranker selection follows code-search production defaults
(RERANKER env, default "sonnet").
"""
from __future__ import annotations

import argparse
import json
import os
import sys
from pathlib import Path


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--code-search-root", required=True)
    ap.add_argument("--storage-dir", required=True, help="storage dir used at index time")
    ap.add_argument("--query", required=True)
    ap.add_argument("--k", type=int, default=50)
    args = ap.parse_args()

    sys.path.insert(0, args.code_search_root)
    from embeddings.embedder import CodeEmbedder  # noqa: E402
    from search.indexer import CodeIndexManager  # noqa: E402
    from search.searcher import IntelligentSearcher  # noqa: E402

    storage = Path(args.storage_dir)
    index_manager = CodeIndexManager(str(storage / "index"))
    embedder = CodeEmbedder(cache_dir=str(storage / "models"))
    searcher = IntelligentSearcher(index_manager, embedder)

    results = searcher.search(args.query, k=args.k, search_mode="hybrid")
    out = [
        {
            "relative_path": r.relative_path,
            "name": r.name,
            "parent_name": r.parent_name,
            "chunk_type": r.chunk_type,
            "score": r.similarity_score,
        }
        for r in results
    ]
    json.dump(out, sys.stdout)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
