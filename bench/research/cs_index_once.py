#!/usr/bin/env python3
"""One-shot code-search indexing helper for the SweRank pre-filter pilot (Arm C).

Runs INSIDE code-search's venv. Mirrors the MCP server's component wiring
(CodeIndexManager + CodeEmbedder + MultiLanguageChunker + SnapshotManager →
IncrementalIndexer.incremental_index) but with fully isolated storage —
including the merkle snapshot dir — so pilot runs leave zero residue in the
user's ~/.claude_code_search store.

NOTE: scripts/index_codebase.py is stale against the current chunker API
(chunk_directory now takes directory_path; the chunker binds its root in the
constructor) — discovered 2026-06-11 during the pilot smoke. This helper uses
the production IncrementalIndexer path instead.
"""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--code-search-root", required=True)
    ap.add_argument("--repo", required=True)
    ap.add_argument("--storage-dir", required=True)
    ap.add_argument("--force-full", action="store_true",
                    help="Force a full reindex; omit to let merkle change "
                         "detection embed only the delta vs the snapshot "
                         "(the per-repo cost optimization for multi-instance repos).")
    args = ap.parse_args()

    sys.path.insert(0, args.code_search_root)
    from chunking.multi_language_chunker import MultiLanguageChunker  # noqa: E402
    from embeddings.embedder import CodeEmbedder  # noqa: E402
    from merkle.snapshot_manager import SnapshotManager  # noqa: E402
    from search.incremental_indexer import IncrementalIndexer  # noqa: E402
    from search.indexer import CodeIndexManager  # noqa: E402

    repo = str(Path(args.repo).resolve())
    storage = Path(args.storage_dir)
    storage.mkdir(parents=True, exist_ok=True)

    indexer = IncrementalIndexer(
        indexer=CodeIndexManager(str(storage / "index")),
        embedder=CodeEmbedder(cache_dir=str(storage / "models")),
        chunker=MultiLanguageChunker(repo),
        snapshot_manager=SnapshotManager(storage_dir=storage / "merkle"),
    )
    result = indexer.incremental_index(repo, project_name=Path(repo).name, force_full=args.force_full)
    payload = result.to_dict() if hasattr(result, "to_dict") else {"repr": repr(result)}
    json.dump(payload, sys.stdout)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
