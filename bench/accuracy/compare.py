"""Compare oracle ground-truth edges vs code-graph extraction.

Computes TP/FP/FN/P/R/F1 per edge type for a fixture. Emits:
    baselines/<date>-<fixture>-report.md    — human readable
    baselines/<date>-<fixture>-report.json  — machine readable (for regression diffing)

Qualified-name normalization
----------------------------
code-graph stores QNs like `<project>.<service>.<file>.<func>` where `<project>` is
the file-system-derived project name (e.g. `c-Users-user-Documents-GitHub-mcp-servers`).
Oracle QNs (from PyCG, ast) are already project-relative — they start with a
service directory or a cross-service module (`airlock.foo`, `shared.bar`).

Alignment rule:
1. Strip code-graph's project prefix before comparison.
2. PyCG occasionally prefixes cross-service refs with the wrong service
   (see oracle_pycg.py note) — we handle this via a secondary "suffix-match"
   metric alongside the strict exact-match metric. If suffix >> exact, that's
   a known alignment artifact, not a code-graph bug.

Both metrics are reported so the QN-drift source is visible.
"""
from __future__ import annotations

import argparse
import datetime
import json
import subprocess
import sys
from collections import defaultdict
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from common import (  # noqa: E402
    BASELINES_DIR,
    CACHE_DIR,
    Edge,
    get_fixture,
    read_edges,
    verify_fixture_sha,
)


def _resolve_code_graph_binary() -> Path:
    """Locate the code-graph binary across Windows (.exe) and Linux/macOS.

    Local-dev builds emit `bin/codebase-memory-mcp.exe`; the CI workflow
    builds `bin/codebase-memory-mcp` (no extension). Try the .exe form
    first to preserve historical local-dev behavior, then fall back to
    the no-extension form for CI / Linux / macOS.
    """
    bin_dir = Path(__file__).resolve().parents[2] / "bin"
    for name in ("codebase-memory-mcp.exe", "codebase-memory-mcp"):
        candidate = bin_dir / name
        if candidate.exists():
            return candidate
    # Default to .exe so error messages match historical expectations
    # for the platform that ships the binary as such.
    return bin_dir / "codebase-memory-mcp.exe"


CODE_GRAPH_BINARY = _resolve_code_graph_binary()


def check_index_freshness(projects: list[str]) -> list[str]:
    """Warn if any per-project DB was indexed before the binary was built.

    Detects stale-index drift — the failure mode that produced the
    F1=0.860 → 0.791 surprise on 2026-05-02 (per-subset DBs predated the
    Janusian penalty commit; the metric was measuring an obsolete operating
    point). One git-mtime-equivalent check would have caught it.

    Returns a list of warning strings (one per stale project). Empty list
    means all DBs are fresh enough.

    Heuristic: each project's indexed_at must be >= the binary mtime.
    The binary is the latest production code; if a DB predates it, the
    DB reflects an older code path. False positives (binary rebuilt for
    unrelated reasons) are tolerable — the warning is informational, not
    blocking.
    """
    import sqlite3

    if not CODE_GRAPH_BINARY.exists():
        return []  # No binary to compare against; skip silently.
    binary_mtime = CODE_GRAPH_BINARY.stat().st_mtime
    cache_dir = Path.home() / ".cache" / "codebase-memory-mcp"
    if not cache_dir.exists():
        return []

    warnings: list[str] = []
    for project in projects:
        db_path = cache_dir / f"{project}.db"
        if not db_path.exists():
            warnings.append(
                f"  ! project DB missing: {project}.db (was the project ever indexed?)"
            )
            continue
        try:
            conn = sqlite3.connect(f"file:{db_path}?mode=ro", uri=True)
            cur = conn.cursor()
            cur.execute(
                "SELECT indexed_at FROM projects WHERE name = ?", (project,)
            )
            row = cur.fetchone()
            conn.close()
        except sqlite3.Error:
            continue
        if not row or not row[0]:
            continue
        # indexed_at is ISO 8601; convert to epoch
        try:
            iat_dt = datetime.datetime.fromisoformat(row[0].replace("Z", "+00:00"))
            iat_epoch = iat_dt.timestamp()
        except (ValueError, AttributeError):
            continue
        if iat_epoch < binary_mtime - 60:  # 60s tolerance for clock skew
            age_hours = (binary_mtime - iat_epoch) / 3600
            warnings.append(
                f"  ! stale index: {project} (indexed_at={row[0]}, "
                f"binary {age_hours:.1f}h newer) — re-index to reflect current code"
            )
    return warnings


def _sanitize_path(path: str) -> str:
    """Mirror code-graph's `pipeline.ProjectNameFromPath` sanitization."""
    s = path.replace("\\", "/")
    if len(s) >= 2 and s[1] == ":":
        s = s[0].lower() + s[1:]
    s = s.replace("/", "-").replace(":", "-")
    while "--" in s:
        s = s.replace("--", "-")
    return s.lstrip("-") or "root"


def project_name_for_fixture(fixture: dict) -> str:
    """code-graph derives project name from the indexed path. Mirror that.

    For single-project fixtures (no 'subset' key), this returns one project
    name. For multi-subset fixtures (Rust/Go with crate subsets), use
    `projects_for_fixture()` instead — each subset is indexed as its own
    project by code-graph.
    """
    return _sanitize_path(fixture["path"])


def projects_for_fixture(fixture: dict) -> list[str]:
    """For multi-project fixtures (subset-based), return one sanitized project
    name per subset entry. For single-project fixtures, return a 1-element list.
    """
    subsets = fixture.get("subset")
    if not subsets:
        return [project_name_for_fixture(fixture)]
    base = Path(fixture["path"])
    return [_sanitize_path(str((base / s).resolve())) for s in subsets]


def strip_project_prefix(qn: str, project: str) -> str:
    prefix = project + "."
    if qn.startswith(prefix):
        return qn[len(prefix):]
    return qn


def _run_cypher(project: str, cypher: str, max_rows: int = 10000) -> list[dict]:
    """Execute one Cypher query against code-graph and return the row list."""
    args_json = json.dumps({"project": project, "query": cypher, "max_rows": max_rows})
    proc = subprocess.run(
        [str(CODE_GRAPH_BINARY), "cli", "--raw", "query_graph", args_json],
        capture_output=True,
        timeout=120,
    )
    if proc.returncode != 0:
        raise SystemExit(
            f"code-graph query_graph failed (rc={proc.returncode}): "
            f"{proc.stderr.decode('utf-8', errors='replace')[:500]}"
        )
    try:
        payload = json.loads(proc.stdout.decode("utf-8", errors="replace"))
    except json.JSONDecodeError as e:
        raise SystemExit(f"code-graph returned non-JSON: {e}")
    rows = payload.get("rows") or payload.get("data") or []
    if not isinstance(rows, list):
        rows = payload.get("result", [])
    return rows if isinstance(rows, list) else []


def query_code_graph_edges(project: str, edge_type: str, caller_shards: list[str]) -> list[Edge]:
    """Pull edges of `edge_type` from code-graph, sharded by caller prefix.

    code-graph's `query_graph` caps unfiltered responses at ~200 rows
    (documented in codebase-memory-reference). WHERE clauses push down to
    SQL and bypass the cap. We shard by `a.qualified_name CONTAINS '<shard>'`
    so each per-shard query returns its full SQL result set, then union.

    `caller_shards` is typically the distinct top-level service directories
    in the fixture (airlock, claude-proxy, shared, ...). The oracle derives
    these from its analyzed-caller set.
    """
    edges: list[Edge] = []
    seen_keys: set[tuple[str, str]] = set()

    for shard in caller_shards:
        # Use CONTAINS with the shard segment wrapped in dots so "airlock"
        # matches "<project>.airlock.*" but not "airlock-foo" elsewhere.
        # Every real QN has the form `<project>.<shard>.<rest>`, so matching
        # `.<shard>.` is sufficient and specific.
        cypher = (
            f"MATCH (a)-[r:{edge_type}]->(b) "
            f'WHERE a.qualified_name CONTAINS ".{shard}." '
            f"RETURN a.qualified_name AS f, b.qualified_name AS t, "
            f"a.file_path AS file, a.start_line AS line LIMIT 100000"
        )
        rows = _run_cypher(project, cypher)
        for r in rows:
            if not isinstance(r, dict):
                continue
            f = r.get("f") or ""
            t = r.get("t") or ""
            if not (f and t):
                continue
            key = (f, t)
            if key in seen_keys:
                continue
            seen_keys.add(key)
            edges.append(
                Edge(
                    from_qn=strip_project_prefix(f, project),
                    to_qn=strip_project_prefix(t, project),
                    type=edge_type,
                    file=r.get("file", "") or "",
                    line=int(r.get("line", 0) or 0),
                    source="code-graph",
                )
            )
    return edges


def query_edges_single_project(project: str, edge_type: str) -> list[Edge]:
    """Query all edges of `edge_type` for a single project, no sharding.

    Used for Rust/Go fixtures where each subset is its own small project and
    the project-scoped query fits within the 10000-row absoluteMaxRows cap.
    Returns project-prefix-stripped edges so match keys align with the
    oracle's stripped form.
    """
    cypher = (
        f"MATCH (a)-[r:{edge_type}]->(b) "
        f"RETURN a.qualified_name AS f, b.qualified_name AS t, "
        f"a.file_path AS file, a.start_line AS line LIMIT 100000"
    )
    rows = _run_cypher(project, cypher, max_rows=10000)
    edges: list[Edge] = []
    for r in rows:
        if not isinstance(r, dict):
            continue
        f = r.get("f") or ""
        t = r.get("t") or ""
        if not (f and t):
            continue
        edges.append(
            Edge(
                from_qn=f,
                to_qn=t,
                type=edge_type,
                file=r.get("file", "") or "",
                line=int(r.get("line", 0) or 0),
                source="code-graph",
            )
        )
    return edges


def query_edges_multi_project(projects: list[str], edge_type: str) -> list[Edge]:
    """Union edges across multiple projects (Rust/Go subset fixtures).

    Each subset was indexed as its own project, so we query each and combine.
    Edges keep their full QN (including per-subset project prefix) so they
    match the oracle's aligned QN form.
    """
    out: list[Edge] = []
    seen: set[tuple[str, str, str]] = set()
    for p in projects:
        for e in query_edges_single_project(p, edge_type):
            key = e.match_key()
            if key in seen:
                continue
            seen.add(key)
            out.append(e)
    return out


def query_caller_node_kinds(
    projects: list[str], edge_type: str
) -> dict[tuple[str, str, str], str]:
    """Return a (from_qn, to_qn, type) -> caller_node_kind map for `edge_type`.

    Reads `caller_node_kind` out of edge properties (populated by the
    resolver — see internal/pipeline/caller_kind.go for the decision
    rules). Edges from indexes that predate the property always return
    NULL via json_extract; we filter those out so callers can detect
    "feature not yet emitted" by getting an empty dict back.

    Multi-project flavor for Rust/Go subset fixtures. The keys use
    full project-prefixed QNs to match `query_edges_multi_project`'s
    output exactly.

    Step 3 of the 2026-05-02 plateau-2 plan. Used by compute_metrics
    to stratify precision by caller-shape.
    """
    # Note: code-graph's Cypher engine does not implement `IS NOT NULL`,
    # so we return all rows including kind=None and filter on the Python
    # side. This was intentionally NOT pushed into Cypher to keep the
    # query language simple — null-detection is rare elsewhere.
    out: dict[tuple[str, str, str], str] = {}
    for project in projects:
        cypher = (
            f"MATCH (a)-[r:{edge_type}]->(b) "
            f"RETURN a.qualified_name AS f, b.qualified_name AS t, "
            f"r.caller_node_kind AS kind LIMIT 100000"
        )
        rows = _run_cypher(project, cypher, max_rows=10000)
        for r in rows:
            if not isinstance(r, dict):
                continue
            f = r.get("f") or ""
            t = r.get("t") or ""
            kind = r.get("kind") or ""
            if not (f and t and kind):
                continue
            out[(f, t, edge_type)] = kind
    return out


def query_caller_node_kinds_sharded(
    project: str, edge_type: str, caller_shards: list[str]
) -> dict[tuple[str, str, str], str]:
    """Single-project sharded variant of query_caller_node_kinds.

    Mirrors the sharding in `query_code_graph_edges`. Used by the Python
    fixture path. Keys use project-prefix-stripped QNs so they match
    the Edge.match_key() form returned by query_code_graph_edges.
    """
    # See note in `query_caller_node_kinds`: NULL filter happens in
    # Python, not Cypher.
    out: dict[tuple[str, str, str], str] = {}
    for shard in caller_shards:
        cypher = (
            f"MATCH (a)-[r:{edge_type}]->(b) "
            f'WHERE a.qualified_name CONTAINS ".{shard}." '
            f"RETURN a.qualified_name AS f, b.qualified_name AS t, "
            f"r.caller_node_kind AS kind LIMIT 100000"
        )
        rows = _run_cypher(project, cypher, max_rows=10000)
        for r in rows:
            if not isinstance(r, dict):
                continue
            f = r.get("f") or ""
            t = r.get("t") or ""
            kind = r.get("kind") or ""
            if not (f and t and kind):
                continue
            out[(strip_project_prefix(f, project), strip_project_prefix(t, project), edge_type)] = kind
    return out


def query_resolver_rules(
    projects: list[str], edge_type: str
) -> dict[tuple[str, str, str], str]:
    """Return a (from_qn, to_qn, type) -> resolver_rule map for `edge_type`.

    Reads `resolver_rule` out of edge properties (populated by the
    resolver — see internal/pipeline/resolver_rule.go for the taxonomy).
    Edges from indexes that predate the property always return NULL via
    json_extract; we filter those out so callers can detect "feature
    not yet emitted" by getting an empty dict back.

    Multi-project flavor for Rust/Go subset fixtures. The keys use
    full project-prefixed QNs to match `query_edges_multi_project`'s
    output exactly.

    Step 4 of the 2026-05-02 plateau-2 plan. Used by compute_metrics
    to stratify precision by resolver pathway.
    """
    out: dict[tuple[str, str, str], str] = {}
    for project in projects:
        cypher = (
            f"MATCH (a)-[r:{edge_type}]->(b) "
            f"RETURN a.qualified_name AS f, b.qualified_name AS t, "
            f"r.resolver_rule AS rule LIMIT 100000"
        )
        rows = _run_cypher(project, cypher, max_rows=10000)
        for r in rows:
            if not isinstance(r, dict):
                continue
            f = r.get("f") or ""
            t = r.get("t") or ""
            rule = r.get("rule") or ""
            if not (f and t and rule):
                continue
            out[(f, t, edge_type)] = rule
    return out


def query_resolver_rules_sharded(
    project: str, edge_type: str, caller_shards: list[str]
) -> dict[tuple[str, str, str], str]:
    """Single-project sharded variant of query_resolver_rules.

    Mirrors the sharding in `query_caller_node_kinds_sharded`. Used by
    the Python fixture path. Keys use project-prefix-stripped QNs so
    they match the Edge.match_key() form returned by
    query_code_graph_edges.
    """
    out: dict[tuple[str, str, str], str] = {}
    for shard in caller_shards:
        cypher = (
            f"MATCH (a)-[r:{edge_type}]->(b) "
            f'WHERE a.qualified_name CONTAINS ".{shard}." '
            f"RETURN a.qualified_name AS f, b.qualified_name AS t, "
            f"r.resolver_rule AS rule LIMIT 100000"
        )
        rows = _run_cypher(project, cypher, max_rows=10000)
        for r in rows:
            if not isinstance(r, dict):
                continue
            f = r.get("f") or ""
            t = r.get("t") or ""
            rule = r.get("rule") or ""
            if not (f and t and rule):
                continue
            out[(strip_project_prefix(f, project), strip_project_prefix(t, project), edge_type)] = rule
    return out


def query_confidence_bands(
    projects: list[str], edge_type: str
) -> dict[tuple[str, str, str], str]:
    """Return a (from_qn, to_qn, type) -> confidence_band map for `edge_type`.

    Reads `confidence_band` out of edge properties (set by the resolver
    based on the resolution's confidence score, plus a special override
    `speculative-janusian` for edges that survived the Janusian-ambiguity
    softening introduced 2026-05-02 — see internal/pipeline/pipeline_cbm.go).

    Multi-project flavor for Rust/Go subset fixtures. Used by compute_metrics
    to compute `scope_aligned_high_confidence` (excludes speculative-janusian).
    """
    out: dict[tuple[str, str, str], str] = {}
    for project in projects:
        cypher = (
            f"MATCH (a)-[r:{edge_type}]->(b) "
            f"RETURN a.qualified_name AS f, b.qualified_name AS t, "
            f"r.confidence_band AS band LIMIT 100000"
        )
        rows = _run_cypher(project, cypher, max_rows=10000)
        for r in rows:
            if not isinstance(r, dict):
                continue
            f = r.get("f") or ""
            t = r.get("t") or ""
            band = r.get("band") or ""
            if not (f and t and band):
                continue
            out[(f, t, edge_type)] = band
    return out


def query_candidate_sets(
    projects: list[str], edge_type: str
) -> dict[tuple[str, str, str], int]:
    """Return a (from_qn, to_qn, type) -> candidate_set_size map for `edge_type`.

    Reads `candidate_set_size` out of edge properties (populated by the
    resolver — see internal/pipeline/candidate_set.go). Edges from
    indexes that predate the property always return NULL via
    json_extract; we filter those out so callers can detect "feature
    not yet emitted" by getting an empty dict back.

    Multi-project flavor for Rust/Go subset fixtures. The keys use
    full project-prefixed QNs to match `query_edges_multi_project`'s
    output exactly.

    Step 5 of the 2026-05-02 plateau-2 plan. Used by compute_metrics
    to stratify precision by call-site ambiguity (Janusian split).
    """
    out: dict[tuple[str, str, str], int] = {}
    for project in projects:
        cypher = (
            f"MATCH (a)-[r:{edge_type}]->(b) "
            f"RETURN a.qualified_name AS f, b.qualified_name AS t, "
            f"r.candidate_set_size AS size LIMIT 100000"
        )
        rows = _run_cypher(project, cypher, max_rows=10000)
        for r in rows:
            if not isinstance(r, dict):
                continue
            f = r.get("f") or ""
            t = r.get("t") or ""
            size = r.get("size")
            # NULL/None — pre-migration row OR an emit path that didn't
            # populate the property; skip so callers can detect "not yet
            # emitted." Keep 0 / -1 sentinels (resolver-bug or
            # explicit-unknown signals) so the harness can surface them.
            if not (f and t) or size is None:
                continue
            try:
                size_int = int(size)
            except (TypeError, ValueError):
                continue
            out[(f, t, edge_type)] = size_int
    return out


def query_candidate_sets_sharded(
    project: str, edge_type: str, caller_shards: list[str]
) -> dict[tuple[str, str, str], int]:
    """Single-project sharded variant of query_candidate_sets.

    Mirrors the sharding in `query_resolver_rules_sharded`. Used by the
    Python fixture path. Keys use project-prefix-stripped QNs so they
    match the Edge.match_key() form returned by query_code_graph_edges.
    """
    out: dict[tuple[str, str, str], int] = {}
    for shard in caller_shards:
        cypher = (
            f"MATCH (a)-[r:{edge_type}]->(b) "
            f'WHERE a.qualified_name CONTAINS ".{shard}." '
            f"RETURN a.qualified_name AS f, b.qualified_name AS t, "
            f"r.candidate_set_size AS size LIMIT 100000"
        )
        rows = _run_cypher(project, cypher, max_rows=10000)
        for r in rows:
            if not isinstance(r, dict):
                continue
            f = r.get("f") or ""
            t = r.get("t") or ""
            size = r.get("size")
            if not (f and t) or size is None:
                continue
            try:
                size_int = int(size)
            except (TypeError, ValueError):
                continue
            out[(strip_project_prefix(f, project), strip_project_prefix(t, project), edge_type)] = size_int
    return out


def _derive_caller_shards(oracle_edges: list[Edge]) -> list[str]:
    """Pull the set of top-level-dir prefixes from the oracle's callers.

    Every oracle caller QN is project-relative, starting with `<service>.`,
    so the first segment IS the shard key.
    """
    shards: set[str] = set()
    for e in oracle_edges:
        first = e.from_qn.split(".", 1)[0]
        if first:
            shards.add(first)
    return sorted(shards)


def suffix_match_key(qn: str, min_segments: int = 3) -> str | None:
    """Return the last `min_segments` of a dotted QN, or None if too short.

    Used for permissive matching when QN prefixes drift (e.g., PyCG
    mislabels cross-service refs with the wrong service prefix).
    """
    parts = qn.split(".")
    if len(parts) < min_segments:
        return None
    return ".".join(parts[-min_segments:])


def normalize_generics(qn: str) -> str:
    """Strip type-parameter brackets from each dotted segment of a QN.

    THEME E (2026-05-02). Rust-specific. Code-graph extracts `Foo<S>`
    from impl-block `type` fields verbatim (full source text), while
    syn-based callees often arrive without the parameter list. The two
    forms refer to the same definition; treating them as different
    inflates the FP/FN count by a small but recurring margin (4 FPs
    on assetman post-THEME-D).

    Examples:
      `pkg.src.file.Foo<S>.bar` -> `pkg.src.file.Foo.bar`
      `pkg.src.file.Foo.bar`    -> `pkg.src.file.Foo.bar` (unchanged)
      `pkg.src.Map<K,V>.iter`   -> `pkg.src.Map.iter`
    """
    out_parts: list[str] = []
    for part in qn.split("."):
        idx = part.find("<")
        if idx > 0 and part.endswith(">"):
            out_parts.append(part[:idx])
        else:
            out_parts.append(part)
    return ".".join(out_parts)


def normalize_impl_suffix(qn: str) -> str:
    """Strip `Impl` suffix from the penultimate segment of a dotted QN.

    Rust-specific. redacted's convention (and Rust's common idiom) is
    `trait Foo { fn bar() }` implemented by `struct FooImpl { ... }` with
    `impl Foo for FooImpl { fn bar() { ... } }`. Code-graph's resolver
    picks the trait form (`X.Foo.bar`) for method dispatch; the
    syn-based oracle picks the impl form (`X.FooImpl.bar`) because the
    self-type at the call site is the impl.

    Both QNs refer to the same underlying function (unless there are
    multiple impls of the same trait — rare in this codebase). Stripping
    `Impl` from the penultimate segment normalizes to a shared form so
    matching reflects function identity, not naming-convention artifacts.

    Examples:
      `pkg.src.file.FooImpl.bar` -> `pkg.src.file.Foo.bar`
      `pkg.src.file.Foo.bar`     -> `pkg.src.file.Foo.bar` (unchanged)
      `pkg.src.file.FooImplA.bar`-> `pkg.src.file.FooImplA.bar` (not exact Impl)
    """
    parts = qn.split(".")
    if len(parts) < 2:
        return qn
    if parts[-2].endswith("Impl") and len(parts[-2]) > 4:
        parts[-2] = parts[-2][:-4]
    return ".".join(parts)


def compute_per_project_metrics(
    oracle: list[Edge], measured: list[Edge], projects: list[str]
) -> list[dict]:
    """Compute scope-aligned F1 per project so aggregate variance is visible.

    For each project prefix, restrict both the oracle and code-graph edge
    sets to callers starting with that project prefix. If the headline F1
    is a mean of [0.45, 0.92, 0.88, 0.90, 0.85], we want to know that so
    the 0.45 project can be investigated independently.
    """
    per_project: list[dict] = []
    for project in projects:
        prefix = project + "."
        o = [e for e in oracle if e.from_qn.startswith(prefix)]
        m = [e for e in measured if e.from_qn.startswith(prefix)]
        o_keys = {(e.from_qn, e.to_qn, e.type) for e in o}
        m_keys = {(e.from_qn, e.to_qn, e.type) for e in m}
        o_callers = {e.from_qn for e in o}
        o_scoped = {k for k in o_keys if k[0] in o_callers}
        m_scoped = {k for k in m_keys if k[0] in o_callers}
        tp = len(o_scoped & m_scoped)
        fp = len(m_scoped - o_scoped)
        fn = len(o_scoped - m_scoped)
        precision = tp / (tp + fp) if (tp + fp) else 0.0
        recall = tp / (tp + fn) if (tp + fn) else 0.0
        f1 = 2 * precision * recall / (precision + recall) if (precision + recall) else 0.0
        per_project.append({
            "project": project,
            "oracle_count": len(o),
            "measured_count": len(m),
            "tp": tp,
            "fp": fp,
            "fn": fn,
            "precision": round(precision, 4),
            "recall": round(recall, 4),
            "f1": round(f1, 4),
        })
    return per_project


def _is_test_file(file_path: str) -> bool:
    """Detect test files across supported languages.

    Used to filter test-file callers from the scope-aligned metric.
    The Go AST oracle records constructor calls inside test functions
    but systematically misses receiver-method calls (e.g., `cache.Get`
    after `cache := NewQueryCache(...)`) because it doesn't fully type-
    infer test-local variables. Code-graph emits all those calls
    correctly, producing 350+ phantom FPs that are real edges. The
    asymmetric filter (keep TPs, drop FPs from test callers) aligns
    the metric to oracle coverage. See bench/accuracy/research notes
    A5 (2026-04-30) for the population analysis.
    """
    if not file_path:
        return False
    f = file_path.replace("\\", "/").lower()
    # Go: foo_test.go ; Python: test_foo.py / foo_test.py ; Rust: tests/ ;
    # JS/TS: foo.test.ts / foo.spec.ts
    return (
        f.endswith("_test.go")
        or f.endswith("_test.py") or "/test_" in f or f.startswith("test_")
        or "/tests/" in f
        or ".test." in f or ".spec." in f
    )


def compute_caller_kind_metrics(
    tp_scoped: set[tuple[str, str, str]],
    fp_scoped: set[tuple[str, str, str]],
    fn_scoped: set[tuple[str, str, str]],
    caller_kinds: dict[tuple[str, str, str], str] | None,
) -> dict:
    """Stratify scope-aligned precision by caller AST scope.

    Three signals (Step 3 of the 2026-05-02 plateau-2 plan):

    1. caller_kind_precision[kind]: tp / (tp + fp) per caller-kind
       cell, restricted to kinds with support >= 20 (smaller cells
       have too much sampling variance to act on).
    2. pkg_block_caller_FP_rate: share of scope-aligned FPs whose
       caller_node_kind is in the package-block family
       (file-block / package-init-block / type-decl / var-init).
       Alarm at > 5% — this is the "ghost caller" defect class
       converged on by 9 of 15 personas in the discovery dispatch.
    3. caller_kind_complement_legitimacy: share of all scope-aligned
       edges (TPs + FPs + FNs) whose caller_node_kind is in
       {function-body, method-body}. Direct measurement of how much
       of the population represents "real-callable-emits-call"
       semantics, the complement of the ghost-caller class.

    If `caller_kinds` is None or empty, returns an empty dict and a
    `note` field signaling the property isn't yet emitted (e.g. the
    binary running this comparison predates the resolver change).
    """
    if not caller_kinds:
        return {
            "caller_kind_precision": {},
            "pkg_block_caller_FP_rate": {"value": 0.0, "alarm": False},
            "caller_kind_complement_legitimacy": {"value": 0.0},
            "note": "caller_node_kind not yet emitted by the indexed binary; metrics skipped.",
        }

    PKG_BLOCK_KINDS = {"file-block", "package-init-block", "type-decl", "var-init"}
    LEGITIMATE_KINDS = {"function-body", "method-body"}

    # Per-kind precision from TPs and FPs.
    by_kind_tp: dict[str, int] = defaultdict(int)
    by_kind_fp: dict[str, int] = defaultdict(int)
    for k in tp_scoped:
        kind = caller_kinds.get(k, "unknown")
        by_kind_tp[kind] += 1
    for k in fp_scoped:
        kind = caller_kinds.get(k, "unknown")
        by_kind_fp[kind] += 1

    per_kind: dict[str, dict] = {}
    for kind in sorted(set(by_kind_tp) | set(by_kind_fp)):
        tp = by_kind_tp[kind]
        fp = by_kind_fp[kind]
        support = tp + fp
        if support < 20:
            continue
        precision = tp / support if support else 0.0
        per_kind[kind] = {
            "tp": tp,
            "fp": fp,
            "precision": round(precision, 4),
            "support": support,
        }

    # Package-block caller FP rate.
    fp_total = len(fp_scoped)
    pkg_block_fps = sum(
        1 for k in fp_scoped if caller_kinds.get(k, "unknown") in PKG_BLOCK_KINDS
    )
    pkg_block_rate = pkg_block_fps / fp_total if fp_total else 0.0

    # Complement legitimacy: share of (TP ∪ FP ∪ FN) whose caller is a
    # function or method body. FNs were never emitted by the resolver so
    # they have no caller_node_kind; we count them but they contribute 0
    # to the legitimate share unless the oracle also sees them as
    # function/method callers (which we can't verify here without
    # re-running the oracle), so we use a conservative denominator that
    # includes FNs as "unknown."
    population = tp_scoped | fp_scoped | fn_scoped
    pop_total = len(population)
    legitimate_count = sum(
        1 for k in population if caller_kinds.get(k, "unknown") in LEGITIMATE_KINDS
    )
    legitimacy_rate = legitimate_count / pop_total if pop_total else 0.0

    return {
        "caller_kind_precision": per_kind,
        "pkg_block_caller_FP_rate": {
            "value": round(pkg_block_rate, 4),
            "alarm": pkg_block_rate > 0.05,
            "fp_count": pkg_block_fps,
            "fp_total": fp_total,
        },
        "caller_kind_complement_legitimacy": {
            "value": round(legitimacy_rate, 4),
            "legitimate_count": legitimate_count,
            "population_total": pop_total,
        },
    }


# Go stdlib package prefixes used to classify callee_package_relation.
# A QN whose first dot-segment is one of these is a Go stdlib reference
# (e.g. "fmt.Println", "strings.HasPrefix", "encoding/json.Marshal" —
# only the bare-name form lands as a single segment, but the path-form
# also lands here when the resolver emitted the import as the prefix).
# Conservative list — covers the most common stdlib packages seen in
# resolver output. False negatives just bucket as
# cross-package-imported, which is fine.
_GO_STDLIB_PREFIXES = frozenset({
    "archive", "bufio", "builtin", "bytes", "cmp", "compress",
    "container", "context", "crypto", "database", "debug", "embed",
    "encoding", "errors", "expvar", "flag", "fmt", "go", "hash",
    "html", "image", "index", "io", "iter", "log", "maps", "math",
    "mime", "net", "os", "path", "plugin", "reflect", "regexp",
    "runtime", "slices", "sort", "strconv", "strings", "structs",
    "sync", "syscall", "testing", "text", "time", "unicode",
    "unique", "unsafe",
})


def _package_prefix(qn: str) -> str:
    """Return the package portion of a QN — everything up to the last
    dot-segment, which is conventionally the symbol name.

    For `proj.foo.bar.Func` returns `proj.foo.bar`; for `Func` (no
    dots) returns "". Used to compare same-package vs cross-package.
    """
    idx = qn.rfind(".")
    if idx < 0:
        return ""
    return qn[:idx]


def _callee_package_relation(caller_qn: str, callee_qn: str) -> str:
    """Classify the package-relationship between caller and callee.

    Buckets:
      same-package            — caller and callee share package prefix
      cross-package-imported  — different packages, both project-internal
      cross-package-stdlib    — callee's first segment matches a Go
                                stdlib package
      external-stub           — callee QN looks synthesized
                                (no dots, OR matches an external pattern)
      unknown                 — caller_qn or callee_qn missing

    Step 4 of the 2026-05-02 plateau-2 plan. Derived at metric time
    from the QN strings; not stored on the edge.
    """
    if not caller_qn or not callee_qn:
        return "unknown"

    # Stdlib check: callee's first dot-segment matches a known Go
    # stdlib package. Catches both `fmt.Println` and richer paths
    # like `encoding/json.Marshal` if the resolver collapses them.
    callee_first = callee_qn.split(".", 1)[0] if "." in callee_qn else callee_qn
    # Strip any path-style prefix (e.g. `encoding/json` → `encoding`)
    callee_first_root = callee_first.split("/", 1)[0]
    if callee_first_root in _GO_STDLIB_PREFIXES:
        return "cross-package-stdlib"

    # External-stub heuristic: callee QN with no dots is a bare name —
    # typically a resolver synthesized stub or unresolved external. The
    # CALLS_EXTERNAL modal classification covers most of these via
    # resolver_rule = modal-external; the package-relation derivation
    # is a fallback for callers that look at this dimension without
    # the modal join.
    if "." not in callee_qn:
        return "external-stub"

    caller_pkg = _package_prefix(caller_qn)
    callee_pkg = _package_prefix(callee_qn)
    if not caller_pkg or not callee_pkg:
        return "unknown"
    if caller_pkg == callee_pkg:
        return "same-package"
    return "cross-package-imported"


def _gini(shares: list[float]) -> float:
    """Compute the Gini coefficient of a non-negative share vector.

    Uses the standard formula: G = sum_i sum_j |x_i - x_j| / (2*n*sum_i x_i).
    Returns 0.0 for an empty or zero-sum vector. High Gini ≈ one share
    dominates (concentration); low Gini ≈ shares spread evenly.
    """
    if not shares:
        return 0.0
    total = sum(shares)
    if total <= 0:
        return 0.0
    n = len(shares)
    if n == 1:
        return 0.0
    pair_sum = 0.0
    for i in range(n):
        for j in range(n):
            pair_sum += abs(shares[i] - shares[j])
    return pair_sum / (2 * n * total)


def compute_modality_metrics(
    tp_scoped: set[tuple[str, str, str]],
    fp_scoped: set[tuple[str, str, str]],
    resolver_rules: dict[tuple[str, str, str], str] | None,
    caller_kinds: dict[tuple[str, str, str], str] | None = None,
    projects: list[str] | None = None,
) -> dict:
    """Stratify scope-aligned precision by resolver_rule and joint
    (caller_kind × callee_package_relation) cells.

    Three signals (Step 4 of the 2026-05-02 plateau-2 plan):

    1. modality_precision[rule]: for each rule with support >= 50 in
       scope-aligned set, {tp, fp, precision, support}. Alarm fires on
       precision < 0.6 AND support > 50 — a low-precision dominant
       rule is the signal Step 2's LLM-Judge taxonomy predicts will
       expose `cross_package_heuristic_overreach`.

    2. modality_mix_gini: Gini coefficient of resolver-rule share,
       computed per project. Reported as {project: gini_value, ...}.
       High Gini = a project relies heavily on one rule
       (concentration); low Gini = mix is spread out. Computed only
       on the rule labels actually emitted; projects with empty
       resolver-rule data return 0.0.

    3. caller_context_tuple_precision[(caller_kind, callee_package_relation)]:
       cells of the (caller_kind × callee_package_relation) table.
       caller_kind comes from Step 3. callee_package_relation is
       derived at metric time from caller_qn / callee_qn (values:
       same-package, cross-package-imported, cross-package-stdlib,
       external-stub, unknown). Cell: {tp, fp, precision, support},
       only emitted when support >= 20.

    If `resolver_rules` is None or empty, returns an empty dict and a
    `note` field signaling the property isn't yet emitted (e.g. the
    binary running this comparison predates the resolver change).
    """
    if not resolver_rules:
        return {
            "modality_precision": {},
            "modality_mix_gini": {},
            "caller_context_tuple_precision": {},
            "modality_note": "resolver_rule not yet emitted by the indexed binary; metrics skipped.",
        }

    # 1. Per-rule precision from TPs and FPs.
    by_rule_tp: dict[str, int] = defaultdict(int)
    by_rule_fp: dict[str, int] = defaultdict(int)
    for k in tp_scoped:
        rule = resolver_rules.get(k, "unknown")
        by_rule_tp[rule] += 1
    for k in fp_scoped:
        rule = resolver_rules.get(k, "unknown")
        by_rule_fp[rule] += 1

    # Support threshold for emitting a per-rule cell. Default 50 keeps the
    # headline reports terse on the strong fixtures (mcp-servers, cobra,
    # code-graph, PSM-rust). For small-N adversarial fixtures (flask=86,
    # requests=76 scope-aligned edges) the default filters every cell.
    # Override via MODALITY_MIN_SUPPORT env var.
    import os as _os_modality
    try:
        modality_min_support = int(_os_modality.environ.get("MODALITY_MIN_SUPPORT", "50"))
    except ValueError:
        modality_min_support = 50

    per_rule: dict[str, dict] = {}
    rule_alarms: list[str] = []
    for rule in sorted(set(by_rule_tp) | set(by_rule_fp)):
        tp = by_rule_tp[rule]
        fp = by_rule_fp[rule]
        support = tp + fp
        if support < modality_min_support:
            continue
        precision = tp / support if support else 0.0
        cell = {
            "tp": tp,
            "fp": fp,
            "precision": round(precision, 4),
            "support": support,
        }
        if support > 50 and precision < 0.6:
            cell["alarm"] = True
            rule_alarms.append(rule)
        per_rule[rule] = cell

    # 2. Per-project Gini of resolver-rule mix. Project membership is
    #    inferred from QN prefix (caller QN's first segment when keys
    #    use full project-prefixed form, OR all-keys-as-one when
    #    `projects` is None or empty). The harness passes `projects`
    #    when it has the multi-project key form (Rust/Go subsets).
    gini_by_project: dict[str, float] = {}
    population = tp_scoped | fp_scoped
    if projects:
        for project in projects:
            prefix = project + "."
            project_rules: list[str] = []
            for k in population:
                caller = k[0]
                if caller.startswith(prefix):
                    rule = resolver_rules.get(k)
                    if rule:
                        project_rules.append(rule)
            counts: dict[str, int] = defaultdict(int)
            for r in project_rules:
                counts[r] += 1
            shares = [float(c) for c in counts.values()]
            gini_by_project[project] = round(_gini(shares), 4)
    else:
        # No project list provided — compute one Gini for the whole
        # population. Used by Python fixture path where keys are
        # already project-prefix-stripped.
        all_rules = [
            resolver_rules[k] for k in population if k in resolver_rules
        ]
        counts = defaultdict(int)
        for r in all_rules:
            counts[r] += 1
        shares = [float(c) for c in counts.values()]
        gini_by_project["__all__"] = round(_gini(shares), 4)

    # 3. (caller_kind, callee_package_relation) tuple precision.
    #    caller_kinds may be None — in that case the caller_kind axis
    #    becomes "unknown" for all cells, which is still actionable.
    by_tuple_tp: dict[tuple[str, str], int] = defaultdict(int)
    by_tuple_fp: dict[tuple[str, str], int] = defaultdict(int)
    for k in tp_scoped:
        kind = (caller_kinds or {}).get(k, "unknown")
        relation = _callee_package_relation(k[0], k[1])
        by_tuple_tp[(kind, relation)] += 1
    for k in fp_scoped:
        kind = (caller_kinds or {}).get(k, "unknown")
        relation = _callee_package_relation(k[0], k[1])
        by_tuple_fp[(kind, relation)] += 1

    tuple_cells: dict[str, dict] = {}
    for cell_key in sorted(set(by_tuple_tp) | set(by_tuple_fp)):
        tp = by_tuple_tp[cell_key]
        fp = by_tuple_fp[cell_key]
        support = tp + fp
        if support < 20:
            continue
        precision = tp / support if support else 0.0
        # JSON-friendly key: "kind|relation". Tuple keys don't survive
        # json.dumps; this stringification keeps the report readable.
        tuple_cells[f"{cell_key[0]}|{cell_key[1]}"] = {
            "caller_kind": cell_key[0],
            "callee_package_relation": cell_key[1],
            "tp": tp,
            "fp": fp,
            "precision": round(precision, 4),
            "support": support,
        }

    out: dict = {
        "modality_precision": per_rule,
        "modality_mix_gini": gini_by_project,
        "caller_context_tuple_precision": tuple_cells,
    }
    if rule_alarms:
        out["modality_alarms"] = rule_alarms
    return out


# Threshold at which a call site is considered "Janusian" (ambiguous).
# A site with >= 2 candidates is one where the resolver picked among
# alternatives — Step 2's LLM-Judge taxonomy predicts these are where
# same_named_method_disambiguation FPs concentrate.
JANUSIAN_AMBIGUITY_THRESHOLD = 2


def compute_ambiguity_metrics(
    tp_scoped: set[tuple[str, str, str]],
    fp_scoped: set[tuple[str, str, str]],
    candidate_sets: dict[tuple[str, str, str], int] | None,
    projects: list[str] | None = None,
) -> dict:
    """Stratify scope-aligned precision by call-site Janusian ambiguity.

    Two signals (Step 5 of the 2026-05-02 plateau-2 plan):

    1. method_set_ambiguity_index[project]: share of distinct call sites
       in the scope-aligned (TPs ∪ FPs) population whose candidate-set
       size >= JANUSIAN_AMBIGUITY_THRESHOLD (=2). Reported per-project
       when `projects` is supplied; otherwise reported as a single
       "__all__" key (Python fixture path uses the latter).

       A "call site" is identified by the caller QN (k[0] of an edge
       key). Multiple emitted edges from the same site collapse to one
       site for the index — though today the resolver is single-target
       so this is rarely consequential.

    2. janusian_site_precision_split: precision conditional on whether
       the call site was Janusian. {"ambiguous": {...}, "unambiguous":
       {...}}, each cell with {tp, fp, precision, support}. Hypothesis
       under test: P_ambiguous << P_unambiguous on store; the gap is
       much smaller on cbm. If true, the residual collapse on store is
       a Janusian-ambiguity signal.

    If `candidate_sets` is None or empty, returns an empty dict and a
    `note` field signaling the property isn't yet emitted (e.g. the
    binary running this comparison predates the resolver change).
    """
    if not candidate_sets:
        return {
            "method_set_ambiguity_index": {},
            "janusian_site_precision_split": {},
            "ambiguity_note": (
                "candidate_set_size not yet emitted by the indexed binary; "
                "metrics skipped."
            ),
        }

    # 1. method_set_ambiguity_index per project.
    #    Site identity: caller QN (k[0]). A site is "Janusian" if ANY of
    #    its emitted edges has size >= threshold; in practice the resolver
    #    emits one edge per site so this collapses to a per-edge check
    #    grouped by caller. Caller-QN granularity is intentionally
    #    permissive: if a single function has ten call sites and one of
    #    them is ambiguous, the function is one Janusian site for the
    #    index. Coarser than per-call-site, but the harness can't
    #    distinguish individual call sites without (file, line, col)
    #    metadata which the edge doesn't carry.
    population = tp_scoped | fp_scoped

    def _ambiguity_index_for_keys(keys: set) -> tuple[float, int, int]:
        """Return (index, ambiguous_sites, total_sites) for a key subset."""
        site_max: dict[str, int] = {}
        for k in keys:
            caller = k[0]
            size = candidate_sets.get(k)
            if size is None or size < 1:
                continue
            if size > site_max.get(caller, 0):
                site_max[caller] = size
        total = len(site_max)
        if total == 0:
            return 0.0, 0, 0
        ambiguous = sum(1 for v in site_max.values() if v >= JANUSIAN_AMBIGUITY_THRESHOLD)
        return round(ambiguous / total, 4), ambiguous, total

    index_by_project: dict[str, dict] = {}
    if projects:
        for project in projects:
            prefix = project + "."
            project_keys = {k for k in population if k[0].startswith(prefix)}
            value, amb, total = _ambiguity_index_for_keys(project_keys)
            index_by_project[project] = {
                "value": value,
                "ambiguous_sites": amb,
                "total_sites": total,
            }
    else:
        # Python fixture path / single-project path: one bucket.
        value, amb, total = _ambiguity_index_for_keys(population)
        index_by_project["__all__"] = {
            "value": value,
            "ambiguous_sites": amb,
            "total_sites": total,
        }

    # 2. janusian_site_precision_split: precision conditional on
    #    candidate_set_size bucket (ambiguous vs unambiguous). Cell
    #    granularity is per-EDGE (not per-site) because TP/FP is
    #    defined at edge level — an edge is a TP iff it's in the
    #    oracle set; the candidate-set-size of that edge tells us
    #    whether its emit was Janusian.
    bucket_tp: dict[str, int] = {"ambiguous": 0, "unambiguous": 0}
    bucket_fp: dict[str, int] = {"ambiguous": 0, "unambiguous": 0}

    def _bucket_for(k) -> str | None:
        size = candidate_sets.get(k)
        if size is None or size < 1:
            return None
        return "ambiguous" if size >= JANUSIAN_AMBIGUITY_THRESHOLD else "unambiguous"

    for k in tp_scoped:
        b = _bucket_for(k)
        if b is not None:
            bucket_tp[b] += 1
    for k in fp_scoped:
        b = _bucket_for(k)
        if b is not None:
            bucket_fp[b] += 1

    split: dict[str, dict] = {}
    for bucket in ("ambiguous", "unambiguous"):
        tp = bucket_tp[bucket]
        fp = bucket_fp[bucket]
        support = tp + fp
        precision = tp / support if support else 0.0
        split[bucket] = {
            "tp": tp,
            "fp": fp,
            "precision": round(precision, 4),
            "support": support,
        }

    out: dict = {
        "method_set_ambiguity_index": index_by_project,
        "janusian_site_precision_split": split,
    }

    # Diagnostic gap: the predicted Janusian signal is precision on
    # ambiguous sites significantly LOWER than on unambiguous. Surface
    # the gap as a derived field so the report doesn't require manual
    # subtraction. Negative gap (ambiguous > unambiguous precision)
    # would be evidence against the Step 2 hypothesis.
    if split["ambiguous"]["support"] > 0 and split["unambiguous"]["support"] > 0:
        out["janusian_precision_gap"] = round(
            split["unambiguous"]["precision"] - split["ambiguous"]["precision"], 4
        )

    return out


def compute_metrics(
    oracle: list[Edge],
    measured: list[Edge],
    caller_kinds: dict[tuple[str, str, str], str] | None = None,
    resolver_rules: dict[tuple[str, str, str], str] | None = None,
    modality_projects: list[str] | None = None,
    candidate_sets: dict[tuple[str, str, str], int] | None = None,
    confidence_bands: dict[tuple[str, str, str], str] | None = None,
) -> dict:
    oracle_exact = {e.match_key() for e in oracle}
    measured_exact = {e.match_key() for e in measured}

    tp_exact = oracle_exact & measured_exact
    fp_exact = measured_exact - oracle_exact
    fn_exact = oracle_exact - measured_exact

    # Scope-aligned metric: restrict both sides to edges whose caller is in
    # the oracle's analyzed-caller set, then drop FPs from test-file callers.
    #
    # The raw metric above includes code-graph edges from callers the oracle
    # never reached (e.g., service entry-points outside scope) as FPs, which
    # isn't a fair accuracy signal — it's a scope mismatch artifact. The
    # `in_scope` filter (caller in oracle_callers) handles that.
    #
    # The test-caller FP exclusion handles a SECOND oracle-coverage gap:
    # the Go AST oracle records constructor calls inside test functions
    # but systematically misses receiver-method calls inside them (no
    # type inference for test-local variables like `cache.Get` after
    # `cache := NewQueryCache(...)`). Counting code-graph's correctly-
    # emitted method calls as FPs is an oracle gap, not a code-graph
    # bug. The fix is asymmetric: keep oracle-confirmed TPs from test
    # callers (the constructor calls match), drop only the unmatched
    # measured edges from test callers (the phantom FPs). Symmetric
    # exclusion (which would drop the test-caller side from BOTH sets)
    # discards too many real TPs and net-lowers F1.
    oracle_callers = {e.from_qn for e in oracle}
    test_callers = {
        e.from_qn for e in measured if _is_test_file(e.file)
    } | {
        e.from_qn for e in oracle if _is_test_file(e.file)
    }
    oracle_scoped = {k for k in oracle_exact if k[0] in oracle_callers}
    measured_scoped_raw = {k for k in measured_exact if k[0] in oracle_callers}
    # Asymmetric filter: keep all TPs (intersection); from FPs, drop those
    # whose caller is a test-file caller. FNs are unaffected.
    measured_scoped = (measured_scoped_raw & oracle_scoped) | {
        k for k in (measured_scoped_raw - oracle_scoped) if k[0] not in test_callers
    }
    tp_scoped = oracle_scoped & measured_scoped
    fp_scoped = measured_scoped - oracle_scoped
    fn_scoped = oracle_scoped - measured_scoped

    # High-confidence-only filter (2026-05-02): the resolver now emits
    # Janusian-ambiguous edges with confidence_band="speculative-janusian"
    # instead of dropping them (PR follow-up to revert THEME D). Two
    # downstream operating points:
    #   - scope_aligned        — all bands; max recall, lower precision
    #   - scope_aligned_high_confidence — drops speculative-janusian
    #                            from measured side; reflects "do you trust
    #                            this edge?" precision tier
    # Both are reported so consumers pick the operating point matching
    # their use case (high-precision queries vs blast-radius / impact
    # analysis).
    if confidence_bands:
        high_conf_filter = {
            k for k in measured_scoped
            if confidence_bands.get(k) != "speculative-janusian"
        }
        tp_high = oracle_scoped & high_conf_filter
        fp_high = high_conf_filter - oracle_scoped
        fn_high = oracle_scoped - high_conf_filter
    else:
        tp_high, fp_high, fn_high = tp_scoped, fp_scoped, fn_scoped

    # Suffix match: (from_suffix, to_suffix, type)
    def suffix_key(k: tuple[str, str, str]) -> tuple[str, str, str] | None:
        fs = suffix_match_key(k[0])
        ts = suffix_match_key(k[1])
        if fs and ts:
            return (fs, ts, k[2])
        return None

    oracle_suffix = {s for s in (suffix_key(k) for k in oracle_exact) if s is not None}
    measured_suffix = {s for s in (suffix_key(k) for k in measured_exact) if s is not None}
    tp_suffix = oracle_suffix & measured_suffix
    fp_suffix = measured_suffix - oracle_suffix
    fn_suffix = oracle_suffix - measured_suffix

    def pr(tp: int, fp: int, fn: int) -> dict:
        precision = tp / (tp + fp) if (tp + fp) else 0.0
        recall = tp / (tp + fn) if (tp + fn) else 0.0
        f1 = 2 * precision * recall / (precision + recall) if (precision + recall) else 0.0
        return {
            "tp": tp,
            "fp": fp,
            "fn": fn,
            "precision": round(precision, 4),
            "recall": round(recall, 4),
            "f1": round(f1, 4),
        }

    # Impl-normalized (Rust) match: strip `Impl` suffix on penultimate segment
    # symmetrically on both sides. Reflects redacted's trait/impl naming
    # convention where code-graph resolves to trait form and oracle to impl
    # form. Measured separately so the underlying scope-aligned number is
    # preserved — this isn't goalpost shifting, it's an optional metric that
    # reports "F1 when Impl-suffix is treated as equivalent."
    def impl_norm_key(k: tuple[str, str, str]) -> tuple[str, str, str]:
        return (normalize_impl_suffix(k[0]), normalize_impl_suffix(k[1]), k[2])
    oracle_impl = {impl_norm_key(k) for k in oracle_scoped}
    measured_impl = {impl_norm_key(k) for k in measured_scoped}
    tp_impl = oracle_impl & measured_impl
    fp_impl = measured_impl - oracle_impl
    fn_impl = oracle_impl - measured_impl

    # Generic-normalized (Rust) match: strip type-parameter brackets
    # (`<S>`, `<K,V>`) from each segment of from_qn and to_qn
    # symmetrically. THEME E (2026-05-02). Reflects code-graph's
    # impl-block emission of `Foo<S>.method` vs the oracle's bare
    # `Foo.method`. Combined with impl_suffix normalization for the
    # `scope_canonical` metric below.
    def generic_norm_key(k: tuple[str, str, str]) -> tuple[str, str, str]:
        return (normalize_generics(k[0]), normalize_generics(k[1]), k[2])
    oracle_generic = {generic_norm_key(k) for k in oracle_scoped}
    measured_generic = {generic_norm_key(k) for k in measured_scoped}
    tp_generic = oracle_generic & measured_generic
    fp_generic = measured_generic - oracle_generic
    fn_generic = oracle_generic - measured_generic

    # Fully canonical (impl + generic) — THEME D + E composed. Best
    # estimate of "F1 if both naming-convention artifacts were
    # eliminated." If this is meaningfully above scope_aligned but
    # close to scope_impl_normalized, generics aren't the main issue;
    # if it's substantially above scope_impl_normalized, generics
    # are pulling weight that impl-strip alone misses.
    def canonical_key(k: tuple[str, str, str]) -> tuple[str, str, str]:
        return (
            normalize_generics(normalize_impl_suffix(k[0])),
            normalize_generics(normalize_impl_suffix(k[1])),
            k[2],
        )
    oracle_canon = {canonical_key(k) for k in oracle_scoped}
    measured_canon = {canonical_key(k) for k in measured_scoped}
    tp_canon = oracle_canon & measured_canon
    fp_canon = measured_canon - oracle_canon
    fn_canon = oracle_canon - measured_canon

    # Caller-kind stratification (Step 3 of plateau-2 plan, 2026-05-02).
    # Three new metrics: per-kind precision, ghost-caller FP rate, and
    # complement legitimacy. compute_caller_kind_metrics handles the
    # "feature not yet emitted" case (returns a sentinel dict with a
    # note field). See compute_caller_kind_metrics for the full spec.
    caller_kind_metrics = compute_caller_kind_metrics(
        tp_scoped, fp_scoped, fn_scoped, caller_kinds
    )

    # Modality stratification (Step 4 of plateau-2 plan, 2026-05-02).
    # Three new metrics: per-rule precision, per-project Gini of rule
    # mix, and joint (caller_kind × callee_package_relation) cells.
    # compute_modality_metrics handles the "feature not yet emitted"
    # case identically. See compute_modality_metrics for the full spec.
    modality_metrics = compute_modality_metrics(
        tp_scoped, fp_scoped, resolver_rules, caller_kinds, modality_projects
    )

    # Janusian ambiguity stratification (Step 5 of plateau-2 plan,
    # 2026-05-02). Two new metrics: method_set_ambiguity_index per
    # project and precision split conditional on call-site ambiguity.
    # See compute_ambiguity_metrics for the full spec. Handles the
    # "feature not yet emitted" case identically to Step 3/4 metrics.
    ambiguity_metrics = compute_ambiguity_metrics(
        tp_scoped, fp_scoped, candidate_sets, modality_projects
    )

    return {
        "oracle_count": len(oracle),
        "measured_count": len(measured),
        "oracle_caller_count": len(oracle_callers),
        "exact": pr(len(tp_exact), len(fp_exact), len(fn_exact)),
        "suffix_3seg": pr(len(tp_suffix), len(fp_suffix), len(fn_suffix)),
        "scope_aligned": pr(len(tp_scoped), len(fp_scoped), len(fn_scoped)),
        "scope_aligned_high_confidence": pr(len(tp_high), len(fp_high), len(fn_high)),
        "scope_impl_normalized": pr(len(tp_impl), len(fp_impl), len(fn_impl)),
        "scope_generic_normalized": pr(len(tp_generic), len(fp_generic), len(fn_generic)),
        "scope_canonical": pr(len(tp_canon), len(fp_canon), len(fn_canon)),
        "sample_fp_exact": sorted(fp_exact)[:10],
        "sample_fn_exact": sorted(fn_exact)[:10],
        "sample_fp_scoped": sorted(fp_scoped)[:10],
        "sample_fn_scoped": sorted(fn_scoped)[:10],
        # Full FP/FN sets, scope-aligned. Used by post-hoc analyses that
        # need the complete edge lists (per-call-site blast-radius,
        # caller-kind stratification, LLM-judge taxonomy). The "sample_*"
        # slices above remain the human-readable summary in the report;
        # these full lists feed downstream tooling. See
        # bench/accuracy/blast_radius.py.
        "fp_scoped_full": sorted(fp_scoped),
        "fn_scoped_full": sorted(fn_scoped),
        **caller_kind_metrics,
        **modality_metrics,
        **ambiguity_metrics,
    }


def compare_fixture(fixture_id: str) -> tuple[dict, Path, Path]:
    fixture = get_fixture(fixture_id)
    verify_fixture_sha(fixture)

    # Stale-index detection (added 2026-05-02 after stale-baseline drift
    # surprise). Warn loudly before reporting metrics if any per-project
    # DB predates the binary. Informational — does NOT block the run.
    projects_for_check = projects_for_fixture(fixture)
    if not projects_for_check:
        projects_for_check = [project_name_for_fixture(fixture)]
    stale_warnings = check_index_freshness(projects_for_check)
    if stale_warnings:
        print()
        print("WARN: stale per-project index detected — metrics may not "
              "reflect current code-graph behavior:")
        for w in stale_warnings:
            print(w)
        print()

    today = datetime.date.today().isoformat()
    BASELINES_DIR.mkdir(parents=True, exist_ok=True)
    md_path = BASELINES_DIR / f"{today}-{fixture_id}-report.md"
    json_path = BASELINES_DIR / f"{today}-{fixture_id}-report.json"

    languages = fixture.get("languages") or []
    is_python = "python" in languages
    is_rust = "rust" in languages
    is_go = "go" in languages

    # Multi-project fixtures (Rust/Go subset-based) query one project per
    # subset; single-project fixtures (Python) query one project total and
    # use the existing shard-by-caller-prefix approach.
    projects = projects_for_fixture(fixture)
    single_project = project_name_for_fixture(fixture)

    results: dict[str, dict] = {}

    if is_python:
        # Python path: single project, sharded queries.
        #
        # The CALLS-family is split across three edge types as of PR #121:
        #   CALLS          — real-to-real (precision-relevant)
        #   CALLS_EXTERNAL — real-to-stub (LSP-resolved external symbol)
        #   CALLS_PSEUDO   — synthetic module-default caller (top-level call site)
        #
        # The headline `results["CALLS"]` row uses real-to-real only (the oracle
        # generally excludes external symbols and module-level callers). The
        # `modal_split` sub-key publishes P/R/F1 for each union of types so
        # reviewers can see how each population contributes to the previous
        # undifferentiated CALLS aggregate.
        pycg_cache = CACHE_DIR / f"pycg-{fixture_id}-{fixture['short_sha']}.json"
        if pycg_cache.exists():
            oracle_calls = read_edges(pycg_cache)
            shards = _derive_caller_shards(oracle_calls)
            measured_calls = query_code_graph_edges(single_project, "CALLS", shards)
            measured_external = query_code_graph_edges(single_project, "CALLS_EXTERNAL", shards)
            measured_pseudo = query_code_graph_edges(single_project, "CALLS_PSEUDO", shards)
            # Caller-kind map for stratified precision metrics. Empty
            # dict if the indexed binary predates the resolver change
            # (compute_caller_kind_metrics handles that).
            caller_kinds_calls = query_caller_node_kinds_sharded(single_project, "CALLS", shards)
            resolver_rules_calls = query_resolver_rules_sharded(single_project, "CALLS", shards)
            candidate_sets_calls = query_candidate_sets_sharded(single_project, "CALLS", shards)
            results["CALLS"] = {
                "oracle": "pycg",
                **compute_metrics(
                    oracle_calls, measured_calls, caller_kinds_calls,
                    resolver_rules_calls, candidate_sets=candidate_sets_calls,
                ),
                "modal_split": {
                    "real_only": {
                        "measured_count": len(measured_calls),
                        **compute_metrics(oracle_calls, measured_calls),
                    },
                    "real_plus_external": {
                        "measured_count": len(measured_calls) + len(measured_external),
                        **compute_metrics(oracle_calls, measured_calls + measured_external),
                    },
                    "real_plus_pseudo": {
                        "measured_count": len(measured_calls) + len(measured_pseudo),
                        **compute_metrics(oracle_calls, measured_calls + measured_pseudo),
                    },
                    "all_calls_family": {
                        "measured_count": (
                            len(measured_calls)
                            + len(measured_external)
                            + len(measured_pseudo)
                        ),
                        **compute_metrics(
                            oracle_calls,
                            measured_calls + measured_external + measured_pseudo,
                        ),
                    },
                },
            }
        else:
            print(f"WARN: no PyCG cache at {pycg_cache}; run oracle_pycg.py first")

        ast_cache = CACHE_DIR / f"ast-imports-{fixture_id}-{fixture['short_sha']}.json"
        if ast_cache.exists():
            oracle_imports = read_edges(ast_cache)
            shards = _derive_caller_shards(oracle_imports)
            measured_imports = query_code_graph_edges(single_project, "IMPORTS", shards)
            results["IMPORTS"] = {
                "oracle": "ast",
                **compute_metrics(oracle_imports, measured_imports),
            }
        else:
            print(f"WARN: no AST cache at {ast_cache}; run oracle_ast_imports.py first")

        ensemble_cache = CACHE_DIR / f"ensemble-http-{fixture_id}-{fixture['short_sha']}.json"
        if ensemble_cache.exists():
            oracle_http = read_edges(ensemble_cache)
            shards = _derive_caller_shards(oracle_http)
            measured_http = query_code_graph_edges(single_project, "HTTP_CALLS", shards)
            results["HTTP_CALLS"] = {
                "oracle": "opus+sonnet",
                **compute_metrics(oracle_http, measured_http),
            }
        else:
            results["HTTP_CALLS"] = {
                "oracle": "opus+sonnet (not yet run)",
                "status": "pending",
            }

    if is_rust:
        # Rust path: multi-project. Oracle already emits edges with full
        # sanitized-path prefix, so we do NOT strip project prefix on either
        # side — match on full QN.
        rust_cache = CACHE_DIR / f"rust-syn-{fixture_id}-{fixture['short_sha']}.json"
        if rust_cache.exists():
            oracle_calls = [e for e in read_edges(rust_cache) if e.type == "CALLS"]
            measured_calls = query_edges_multi_project(projects, "CALLS")
            caller_kinds_calls = query_caller_node_kinds(projects, "CALLS")
            resolver_rules_calls = query_resolver_rules(projects, "CALLS")
            candidate_sets_calls = query_candidate_sets(projects, "CALLS")
            confidence_bands_calls = query_confidence_bands(projects, "CALLS")
            results["CALLS"] = {
                "oracle": "syn",
                "per_project": compute_per_project_metrics(oracle_calls, measured_calls, projects),
                **compute_metrics(
                    oracle_calls, measured_calls, caller_kinds_calls,
                    resolver_rules_calls, modality_projects=projects,
                    candidate_sets=candidate_sets_calls,
                    confidence_bands=confidence_bands_calls,
                ),
            }
            oracle_imports = [e for e in read_edges(rust_cache) if e.type == "IMPORTS"]
            # NOTE: Rust oracle drops IMPORTS by design (see oracle_rust_syn.py
            # header): code-graph's Rust IMPORTS resolver emits very few edges
            # in practice (empirically 0 on canstatd, 8 across 260 crates).
            # Reported as a known limitation rather than a measured F1.
            measured_imports = query_edges_multi_project(projects, "IMPORTS")
            results["IMPORTS"] = {
                "oracle": "syn (dropped — resolver limitation)",
                "oracle_count": len(oracle_imports),
                "measured_count": len(measured_imports),
                "status": "known_limitation",
                "note": (
                    "code-graph's Rust IMPORTS resolver emits very few edges "
                    "in practice. Oracle drops IMPORTS to avoid reporting a "
                    "misleading F1. See oracle_rust_syn.py for details."
                ),
            }

    if is_go:
        # Go path: multi-project, AST-based oracle (oracle_go_ast.py).
        # The earlier go-callgraph oracle is still available but emits
        # import-path QNs that don't match code-graph's sanitized-path form;
        # compare.py prefers go-ast if present and falls back to go-callgraph.
        go_cache = CACHE_DIR / f"go-ast-{fixture_id}-{fixture['short_sha']}.json"
        oracle_tag = "go-ast"
        if not go_cache.exists():
            go_cache = CACHE_DIR / f"go-callgraph-{fixture_id}-{fixture['short_sha']}.json"
            oracle_tag = "go-callgraph-rta"
        if go_cache.exists():
            oracle_all = read_edges(go_cache)
            oracle_calls = [e for e in oracle_all if e.type == "CALLS"]
            oracle_imports = [e for e in oracle_all if e.type == "IMPORTS"]
            measured_calls = query_edges_multi_project(projects, "CALLS")
            measured_imports = query_edges_multi_project(projects, "IMPORTS")
            caller_kinds_calls = query_caller_node_kinds(projects, "CALLS")
            resolver_rules_calls = query_resolver_rules(projects, "CALLS")
            candidate_sets_calls = query_candidate_sets(projects, "CALLS")
            results["CALLS"] = {
                "oracle": oracle_tag,
                "per_project": compute_per_project_metrics(oracle_calls, measured_calls, projects),
                **compute_metrics(
                    oracle_calls, measured_calls, caller_kinds_calls,
                    resolver_rules_calls, modality_projects=projects,
                    candidate_sets=candidate_sets_calls,
                ),
            }
            if oracle_imports:
                results["IMPORTS"] = {
                    "oracle": oracle_tag,
                    **compute_metrics(oracle_imports, measured_imports),
                }
            else:
                results["IMPORTS"] = {
                    "oracle": oracle_tag + " (dropped)",
                    "oracle_count": 0,
                    "measured_count": len(measured_imports),
                    "status": "known_limitation",
                    "note": "Go oracle drops IMPORTS until import-path -> internal-file-QN resolution is added.",
                }
        else:
            print(f"WARN: no Go oracle cache; run oracle_go_ast.py first")

    project = single_project  # back-compat for the JSON report field

    # Write JSON
    report = {
        "schema_version": 1,
        "date": today,
        "fixture": fixture_id,
        "sha": fixture["sha"],
        "short_sha": fixture["short_sha"],
        "project_name": project,
        "results": results,
    }
    json_path.write_bytes(json.dumps(report, indent=2).encode("utf-8"))

    # Write markdown
    lines = [
        f"# code-graph accuracy baseline — {fixture_id}",
        "",
        f"- **Date**: {today}",
        f"- **Fixture SHA**: `{fixture['sha']}` (short: `{fixture['short_sha']}`)",
        f"- **Project name**: `{project}`",
        "",
        "## Summary",
        "",
        "Five metrics per edge type:",
        "- **Exact**: strict (from_qn, to_qn, type) equality between oracle and code-graph.",
        "- **Suffix-3**: permissive match on the last 3 QN segments — identifies QN-drift artifacts.",
        "- **Scope-aligned (all bands)**: restricted to edges whose caller is in the oracle's analyzed-caller set, INCLUDING speculative-janusian band emissions (Janusian-ambiguous cross-package edges). Reflects the recall-friendly operating point — what consumers see by default if they don't filter `confidence_band`.",
        "- **Scope-aligned (high-confidence)**: same scope, EXCLUDING `confidence_band=speculative-janusian` edges. Reflects the precision-friendly operating point for consumers who filter to high-trust edges only.",
        "- **Impl-normalized**: Rust-specific. Strips `Impl` suffix from penultimate QN segment symmetrically on both sides — treats `FooImpl.bar` and `Foo.bar` as the same function. Captures code-graph's trait-form vs oracle's impl-form resolution disagreement.",
        "",
        "Two operating points are reported because the resolver emits Janusian-ambiguous edges (multiple cross-package candidates with same simple name) at the speculative-janusian band rather than dropping them. Find-one-function-reliably queries should filter to high-confidence; blast-radius / impact-analysis queries should use all-bands.",
        "",
        "| Edge type | Oracle | Oracle / Measured | Exact P/R/F1 | Scope-aligned P/R/F1 (all) | Scope-aligned P/R/F1 (high-conf) | Impl-normalized P/R/F1 |",
        "|---|---|---|---|---|---|---|",
    ]
    for edge_type, res in results.items():
        if res.get("status") in ("pending", "known_limitation"):
            oracle_tag = res.get("oracle", "—")
            status_note = res.get("note") or res.get("status", "")
            oc = res.get("oracle_count", "—")
            mc = res.get("measured_count", "—")
            lines.append(
                f"| {edge_type} | {oracle_tag} | {oc} / {mc} | — ({status_note[:60]}) | — | — |"
            )
            continue
        e = res["exact"]
        a = res["scope_aligned"]
        h = res.get("scope_aligned_high_confidence", a)  # fallback for older reports
        i = res.get("scope_impl_normalized", a)  # fallback for older reports
        lines.append(
            f"| {edge_type} | {res['oracle']} | "
            f"{res['oracle_count']} / {res['measured_count']} | "
            f"{e['precision']:.3f} / {e['recall']:.3f} / {e['f1']:.3f} | "
            f"{a['precision']:.3f} / {a['recall']:.3f} / {a['f1']:.3f} | "
            f"{h['precision']:.3f} / {h['recall']:.3f} / {h['f1']:.3f} | "
            f"{i['precision']:.3f} / {i['recall']:.3f} / {i['f1']:.3f} |"
        )
    # Per-project breakdown (only present for multi-project Rust/Go fixtures).
    have_per_project = any(res.get("per_project") for res in results.values())
    if have_per_project:
        lines.append("")
        lines.append("## Per-project scope-aligned F1")
        lines.append("")
        lines.append("Aggregate F1 can hide variance across subsets. If the headline scope-aligned F1 is a mean of widely-varying per-subset numbers, investigate the low outliers separately.")
        lines.append("")
        for edge_type, res in results.items():
            pp = res.get("per_project")
            if not pp:
                continue
            lines.append(f"### {edge_type}")
            lines.append("")
            lines.append("| Project | Oracle / Measured | TP | FP | FN | P | R | F1 |")
            lines.append("|---|---|---:|---:|---:|---:|---:|---:|")
            for p in pp:
                # Shorten the project name: drop the sanitized-path prefix up
                # to the last fixture-relevant segment.
                short = p["project"].split("-")[-1] if "-" in p["project"] else p["project"]
                lines.append(
                    f"| {short} | {p['oracle_count']} / {p['measured_count']} | "
                    f"{p['tp']} | {p['fp']} | {p['fn']} | "
                    f"{p['precision']:.3f} | {p['recall']:.3f} | **{p['f1']:.3f}** |"
                )
            lines.append("")
            # Highlight spread.
            f1_values = [p["f1"] for p in pp if p["oracle_count"] > 0]
            if f1_values:
                lines.append(f"**Spread**: min F1 = {min(f1_values):.3f}, max F1 = {max(f1_values):.3f}, range = {max(f1_values) - min(f1_values):.3f}")
                lines.append("")

    # Caller-kind stratified precision (Step 3 of the 2026-05-02
    # plateau-2 plan). Each CALLS edge carries a `caller_node_kind`
    # property telling us which AST scope emitted it. Aggregate F1 is
    # blind to caller-shape — the per-kind breakdown surfaces the
    # "ghost caller" defect class (FPs from package-level scopes
    # rather than function/method bodies).
    have_caller_kind = any(
        res.get("caller_kind_precision") or "caller_kind_complement_legitimacy" in res
        for res in results.values()
    )
    if have_caller_kind:
        lines.append("")
        lines.append("## Caller-kind stratified precision")
        lines.append("")
        lines.append(
            "Each CALLS edge is tagged with the AST scope of its caller "
            "(`function-body`, `method-body`, `file-block`, `package-init-block`, "
            "`var-init`, `type-decl`, `test-body`, `closure`, `unknown`). The "
            "harness reads this property and stratifies precision by it. The "
            "**ghost-caller FP rate** is the share of FPs whose caller is a "
            "package-level scope rather than a real function/method — alarms "
            "above 5%."
        )
        lines.append("")
        for edge_type, res in results.items():
            if res.get("status") in ("pending", "known_limitation"):
                continue
            ck = res.get("caller_kind_precision")
            if ck is None and "caller_kind_complement_legitimacy" not in res:
                continue
            lines.append(f"### {edge_type}")
            lines.append("")
            note = res.get("note")
            if note:
                lines.append(f"> Note: {note}")
                lines.append("")
                continue
            if ck:
                lines.append("| Kind | TP | FP | Precision | Support |")
                lines.append("|---|---:|---:|---:|---:|")
                for kind in sorted(ck):
                    cell = ck[kind]
                    lines.append(
                        f"| `{kind}` | {cell['tp']} | {cell['fp']} | "
                        f"{cell['precision']:.3f} | {cell['support']} |"
                    )
                lines.append("")
            pkg = res.get("pkg_block_caller_FP_rate", {})
            if pkg:
                alarm = " ALARM" if pkg.get("alarm") else ""
                lines.append(
                    f"**Package-block caller FP rate**: "
                    f"{pkg.get('value', 0):.4f} "
                    f"({pkg.get('fp_count', 0)} of {pkg.get('fp_total', 0)} FPs){alarm}"
                )
                lines.append("")
            comp = res.get("caller_kind_complement_legitimacy", {})
            if comp:
                lines.append(
                    f"**Caller-kind complement legitimacy** (function/method-body share of all "
                    f"scope-aligned edges): {comp.get('value', 0):.4f} "
                    f"({comp.get('legitimate_count', 0)} of "
                    f"{comp.get('population_total', 0)})"
                )
                lines.append("")

    # Janusian ambiguity stratification (Step 5 of the 2026-05-02
    # plateau-2 plan). Each CALLS edge carries a `candidate_set_size`
    # property — the resolver's pre-tie-break candidate cardinality at
    # the call site. Aggregate F1 is blind to ambiguity; the per-bucket
    # split surfaces whether precision collapses on multi-candidate
    # ("Janusian") sites.
    have_ambiguity = any(
        res.get("method_set_ambiguity_index") or "janusian_site_precision_split" in res
        for res in results.values()
    )
    if have_ambiguity:
        lines.append("")
        lines.append("## Janusian ambiguity stratified precision")
        lines.append("")
        lines.append(
            "Each CALLS edge carries the resolver's pre-tie-break "
            "candidate cardinality (`candidate_set_size`). A call site "
            f"with >= {JANUSIAN_AMBIGUITY_THRESHOLD} candidates is "
            "**Janusian** — the resolver picked among alternatives. "
            "Step 2's LLM-Judge taxonomy predicted "
            "`same_named_method_disambiguation` (60% of judged FPs) "
            "concentrates on Janusian sites; the precision split below "
            "tests that hypothesis on real-fixture data. LSP-resolved "
            "edges carry size=1 by definition (LSP returns one target "
            "without enumerating alternates), so the Janusian signal "
            "lives in the registry strategies."
        )
        lines.append("")
        for edge_type, res in results.items():
            if res.get("status") in ("pending", "known_limitation"):
                continue
            mai = res.get("method_set_ambiguity_index")
            split = res.get("janusian_site_precision_split")
            if not (mai or split):
                continue
            lines.append(f"### {edge_type}")
            lines.append("")
            note = res.get("ambiguity_note")
            if note:
                lines.append(f"> Note: {note}")
                lines.append("")
                continue
            if mai:
                lines.append("**method_set_ambiguity_index** — share of call sites with >= 2 candidates:")
                lines.append("")
                lines.append("| Project | Ambiguous sites | Total sites | Index |")
                lines.append("|---|---:|---:|---:|")
                for project in sorted(mai):
                    cell = mai[project]
                    short = project.split("-")[-1] if "-" in project else project
                    lines.append(
                        f"| {short} | {cell.get('ambiguous_sites', 0)} | "
                        f"{cell.get('total_sites', 0)} | "
                        f"{cell.get('value', 0):.4f} |"
                    )
                lines.append("")
            if split:
                lines.append("**janusian_site_precision_split** — precision conditional on call-site ambiguity:")
                lines.append("")
                lines.append("| Bucket | TP | FP | Precision | Support |")
                lines.append("|---|---:|---:|---:|---:|")
                for bucket in ("ambiguous", "unambiguous"):
                    cell = split.get(bucket, {})
                    lines.append(
                        f"| `{bucket}` | {cell.get('tp', 0)} | "
                        f"{cell.get('fp', 0)} | "
                        f"{cell.get('precision', 0):.4f} | "
                        f"{cell.get('support', 0)} |"
                    )
                lines.append("")
                gap = res.get("janusian_precision_gap")
                if gap is not None:
                    lines.append(
                        f"**janusian_precision_gap** "
                        f"(unambiguous − ambiguous precision): {gap:+.4f}. "
                        f"Positive = unambiguous sites resolve more accurately, "
                        f"consistent with Step 2's prediction. Negative or "
                        f"near-zero = ambiguity is not the dominant FP driver."
                    )
                    lines.append("")

    lines.append("")
    # Modal split for CALLS-family edges (real / external / pseudo)
    if "CALLS" in results and "modal_split" in results["CALLS"]:
        ms = results["CALLS"]["modal_split"]
        lines.extend([
            "## CALLS modal split — by edge-kind union",
            "",
            "After PR #121, the CALLS family is partitioned into:",
            "- `CALLS` — real-to-real (precision-relevant)",
            "- `CALLS_EXTERNAL` — real-to-stub (LSP-resolved external)",
            "- `CALLS_PSEUDO` — synthetic module-default caller",
            "",
            "Each row below recomputes precision/recall/F1 against the same",
            "PyCG oracle but with a different union on the measured side.",
            "Headline `results.CALLS` is the `real_only` row.",
            "",
            "| Union | Measured | Exact P/R/F1 | Suffix-3 P/R/F1 | Scope-aligned P/R/F1 |",
            "|---|---|---|---|---|",
        ])
        for union_name, metrics in ms.items():
            e = metrics["exact"]
            s = metrics["suffix_3seg"]
            a = metrics["scope_aligned"]
            lines.append(
                f"| {union_name} | {metrics['measured_count']} | "
                f"{e['precision']:.3f} / {e['recall']:.3f} / {e['f1']:.3f} | "
                f"{s['precision']:.3f} / {s['recall']:.3f} / {s['f1']:.3f} | "
                f"{a['precision']:.3f} / {a['recall']:.3f} / {a['f1']:.3f} |"
            )
        lines.append("")
        lines.append(
            "Diverging rows expose how each non-real population dilutes the"
            " aggregate. Most accuracy regressions live in `real_only`; the"
            " other rows are diagnostic."
        )
        lines.append("")
    lines.append("## Samples (first 10 per edge type)")
    for edge_type, res in results.items():
        if res.get("status") in ("pending", "known_limitation"):
            continue
        lines.extend([
            "",
            f"### {edge_type}",
            "",
            f"Oracle analyzed callers: {res.get('oracle_caller_count', 0)}",
            "",
            "**Scope-aligned false positives** (code-graph edge from a PyCG-analyzed caller to a callee PyCG did not record):",
            "```",
            *[f"  {f} --> {t}" for f, t, _ in res.get("sample_fp_scoped", [])],
            "```",
            "",
            "**Scope-aligned false negatives** (oracle recorded, code-graph did NOT):",
            "```",
            *[f"  {f} --> {t}" for f, t, _ in res.get("sample_fn_scoped", [])],
            "```",
            "",
            "**Raw-exact false positives (may include out-of-scope callers)**:",
            "```",
            *[f"  {f} --> {t}" for f, t, _ in res["sample_fp_exact"]],
            "```",
            "",
            "**Raw-exact false negatives**:",
            "```",
            *[f"  {f} --> {t}" for f, t, _ in res["sample_fn_exact"]],
            "```",
        ])
    lines.append("")
    lines.append("## Targets")
    lines.append("")
    lines.append("- CALLS: target ≥85% recall, ≥60% precision (Sui 2020 baseline 0.884 recall adjusted for Python/Go vs Java).")
    lines.append("- IMPORTS: target ≥95% recall, ≥90% precision (imports are simple, high-trust).")
    lines.append("- HTTP_CALLS: target ≥70% recall, ≥60% precision (inherently noisier).")

    md_path.write_bytes("\n".join(lines).encode("utf-8"))
    return report, md_path, json_path


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("fixture", help="fixture id from fixtures.json")
    args = ap.parse_args()
    report, md_path, json_path = compare_fixture(args.fixture)
    print(f"\n[compare] wrote {md_path}")
    print(f"[compare] wrote {json_path}\n")
    # Print summary table inline
    for edge_type, res in report["results"].items():
        if res.get("status") == "pending":
            print(f"  {edge_type}: pending (run ensemble oracle)")
            continue
        if res.get("status") == "known_limitation":
            print(f"  {edge_type}: known limitation — {res.get('note', '')[:80]}")
            continue
        e = res["exact"]
        print(
            f"  {edge_type}: exact P={e['precision']:.3f} R={e['recall']:.3f} "
            f"F1={e['f1']:.3f}  (oracle={res['oracle_count']}, measured={res['measured_count']})"
        )
    return 0


if __name__ == "__main__":
    sys.exit(main())
