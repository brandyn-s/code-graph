"""Classify CBMCalls extracted-but-not-emitted as CALLS edges.

Phase A'''' of the ABC future-arcs roadmap (2026-05-14). For each
extracted CBMCall, decide:
  emitted               — the call appears as a CALLS edge in the DB
  unemitted_external    — callee_name looks like an external crate path
                          (well-known top-level crate, or `::std::`, etc.)
                          → resolver-side drop is CORRECT
  unemitted_internal    — callee_name looks like an internal-project
                          symbol (capitalized type, internal method,
                          or :: form with a known internal crate)
                          → resolver-side drop is INCORRECT
                          (= the BUG class A'''' targets)
  unemitted_unknown     — neither pattern matches

Reports aggregate counts + top-N samples per class.

Usage:
  cbm-call-audit --project PREFIX --dir REPO --detail > audit.jsonl
  python classify_dropped_cbm_calls.py \\
      --audit audit.jsonl \\
      --db ~/.cache/codebase-memory-mcp/PREFIX.db \\
      --project-prefix PREFIX \\
      --internal-crates assetman,libnet,common,api \\
      --out classification.json
"""
from __future__ import annotations

import argparse
import json
import re
import sqlite3
import sys
from collections import Counter, defaultdict
from pathlib import Path

# Well-known external crate prefixes seen in Rust workspaces. Any callee
# whose first :: segment matches one of these is external by inference.
EXTERNAL_CRATE_PREFIXES = {
    "std", "core", "alloc",
    "tokio", "async_std", "futures", "futures_util",
    "anyhow", "thiserror", "serde", "serde_json", "serde_yaml",
    "tracing", "log", "env_logger", "slog",
    "reqwest", "hyper", "axum", "actix_web", "warp", "tower",
    "diesel", "sqlx", "sea_orm", "r2d2",
    "aws_config", "aws_sdk_s3", "aws_sdk_dynamodb", "aws_sdk_kms",
    "aws_smithy_async", "aws_smithy_runtime", "aws_smithy_types",
    "clap", "structopt",
    "rand", "uuid", "chrono", "time",
    "regex", "url", "base64", "hex",
    "rayon", "crossbeam",
    "itertools", "once_cell", "lazy_static",
    "axum_test", "tower_http",
    "redis", "mongodb",
    "prometheus", "metrics",
    "tokio_util", "tokio_tungstenite",
    "tonic", "prost",
    "openssl", "rustls", "ring",
    "tempfile", "walkdir", "globset", "ignore",
    "k8s_openapi", "kube",
    "restate_sdk",
    "release_image_store",
    "object_store",
    "release_state",
    "release_state_codec",
    "git2",
}

# Common stdlib types whose `Type::method` calls are external by definition.
# These appear as "Type::new" / "Type::from_*" / "Type::default" in Rust
# source and should NOT be classified as internal-suspect.
STDLIB_TYPES = {
    "Vec", "String", "Box", "Arc", "Rc", "Cell", "RefCell",
    "Option", "Result", "Mutex", "RwLock",
    "Duration", "Instant", "SystemTime",
    "HashMap", "HashSet", "BTreeMap", "BTreeSet", "VecDeque",
    "Path", "PathBuf", "OsString", "OsStr",
    "Range", "RangeInclusive",
    "Pin", "Cow",
    "Error",  # commonly std::error::Error or anyhow::Error
    "Utc", "Local", "DateTime", "NaiveDate", "NaiveDateTime",  # chrono types
    "Uuid",  # uuid crate
    "Json", "Form", "Path", "Query", "Data",  # actix-web extractors
    "App", "HttpRequest", "HttpResponse", "ServiceConfig", "Scope",  # actix
    "TcpListener", "TcpStream", "UdpSocket",
    "RngCore", "ThreadRng",
    "PgConnection", "Connection",
    "JoinHandle", "JoinSet",
    "Semaphore", "Notify",
    "BufReader", "BufWriter",
    "TempDir", "TempFile",
}

# Builtin-ish callees that aren't crate calls.
BUILTIN_METHODS = {
    "clone", "unwrap", "expect", "ok", "err", "into",
    "to_string", "to_owned", "as_ref", "as_str", "as_bytes",
    "to_vec", "iter", "into_iter", "collect",
    "is_some", "is_none", "is_empty", "len",
    "as_mut", "as_deref", "as_deref_mut",
    "push", "pop", "insert", "remove", "get",
    "await", "send", "spawn",
    "lock", "read", "write",
    "from", "try_from", "into", "try_into",
    "or", "and_then", "or_else", "map", "filter",
    "println", "eprintln", "format", "format_args",
    "info", "warn", "error", "debug", "trace",
}

# Internal crate names — populated from --internal-crates flag.

PARENT_QN_RE = re.compile(r"^[A-Z][A-Za-z0-9_]*$")  # likely a Rust type name


def classify(
    callee_name: str,
    emitted_callee_names: set[str],
    internal_crates: set[str],
    internal_type_names: set[str],
) -> str:
    if callee_name in emitted_callee_names:
        return "emitted"

    # First :: segment
    head = callee_name.split("::", 1)[0]
    if head in EXTERNAL_CRATE_PREFIXES:
        return "unemitted_external"
    if head in STDLIB_TYPES:
        return "unemitted_external"
    # Phase A''''' refinement: only classify as internal if head matches
    # an actual project-internal Type/Class/Struct/Enum definition. This
    # tightens the heuristic that previously over-counted (capitalized
    # type names not in any known external set were flagged internal
    # regardless of whether the project actually defined them).
    if head in internal_type_names:
        return "unemitted_internal"
    if head in internal_crates:
        return "unemitted_internal"
    # Last . segment (method-on-receiver shape after :: -> . normalization
    # is possible, but extractor preserves :: for static dispatch)
    last_dot_seg = callee_name.rsplit(".", 1)[-1] if "." in callee_name else None
    if last_dot_seg and last_dot_seg in BUILTIN_METHODS:
        # Method calls like ".clone()" go through trait dispatch and are
        # almost always external; resolver correctly drops.
        return "unemitted_external"

    # "Type::method" shape with capitalized type that ISN'T any of: known
    # external crate prefix, known stdlib type, internal type definition,
    # internal crate name. Conservative bucket — likely external library
    # type we haven't listed; class as external to avoid false-internal
    # inflation.
    if "::" in callee_name and PARENT_QN_RE.match(head):
        return "unemitted_external"

    # "obj.method" shape with non-builtin method name — probably
    # internal dispatch we can't resolve here.
    if "." in callee_name and last_dot_seg and last_dot_seg not in BUILTIN_METHODS:
        return "unemitted_unknown"

    return "unemitted_unknown"


def main() -> int:
    p = argparse.ArgumentParser()
    p.add_argument("--audit", required=True, help="JSONL audit from cbm-call-audit --detail")
    p.add_argument("--db", required=True, help="Path to indexed code-graph DB")
    p.add_argument("--project-prefix", required=True,
                   help="QN prefix matching audit --project and DB project")
    p.add_argument("--internal-crates", default="",
                   help="Comma-separated internal crate-name prefixes (e.g. 'assetman,libnet')")
    p.add_argument("--out", required=True)
    p.add_argument("--top", type=int, default=15,
                   help="Top-N callee_names per class to print")
    args = p.parse_args()

    internal_crates = {c.strip() for c in args.internal_crates.split(",")
                        if c.strip()}

    # Load emitted edges from DB (callee_name not stored directly; we
    # approximate by listing the target_node's qualified_name's LAST
    # segment, which is what the extractor would have emitted as
    # callee_name in the simplest case). For more robust matching we
    # use the full target QN suffix.
    conn = sqlite3.connect(f"file:{args.db}?mode=ro", uri=True)
    cur = conn.cursor()
    cur.execute("""
        SELECT n.qualified_name
        FROM edges e
        JOIN nodes n ON n.id = e.target_id
        WHERE e.type = 'CALLS'
    """)
    emitted_full = {row[0] for row in cur.fetchall()}

    # Phase A''''' refinement: load internal Type/Class/Struct/Enum/Trait
    # definitions from the indexed DB. Their SIMPLE names (last QN segment)
    # are the set of project-defined types — only callees whose `head`
    # segment is in this set should be classified internal-suspect.
    cur.execute("""
        SELECT n.name
        FROM nodes n
        WHERE n.label IN ('Class','Struct','Type','Enum','Interface','Trait')
    """)
    internal_type_names = {row[0] for row in cur.fetchall() if row[0]}
    print(f"Loaded {len(internal_type_names)} internal type names from DB",
          flush=True)
    # The CBMCall.callee_name is the raw extracted text (e.g.
    # `ReloaddClient::new` or `self.foo.bar`). We can't precisely
    # invert the resolver's QN normalization, so for the "emitted"
    # detection we use a heuristic: if the LAST :: or . segment of
    # callee_name appears as the LAST . segment of any emitted target,
    # AND the callee text contains the parent segment, count emitted.
    # For exactness we'd need the resolver's per-call decision log.
    emitted_last_segs: set[str] = set()
    emitted_pairs: set[tuple[str, str]] = set()
    for qn in emitted_full:
        parts = qn.split(".")
        if parts:
            emitted_last_segs.add(parts[-1])
        if len(parts) >= 2:
            emitted_pairs.add((parts[-2], parts[-1]))

    # Load audit calls
    text = Path(args.audit).read_text(encoding="utf-8")
    decoder = json.JSONDecoder()
    i = 0
    by_class: dict[str, list[dict]] = defaultdict(list)
    while i < len(text):
        while i < len(text) and text[i].isspace():
            i += 1
        if i >= len(text):
            break
        try:
            obj, end = decoder.raw_decode(text, i)
        except json.JSONDecodeError as e:
            print(f"decode error at {i}: {e}", file=sys.stderr)
            break
        i = end
        for c in (obj.get("calls") or []):
            callee = c.get("callee_name", "")
            enclosing = c.get("enclosing_func_qn", "")
            # Heuristic emitted detection: any segment-pair from callee
            # matches an (parent, method) pair in emitted edges.
            is_emitted = False
            for sep in ("::", "."):
                if sep in callee:
                    parts = callee.split(sep)
                    if len(parts) >= 2:
                        pair = (parts[-2], parts[-1])
                        if pair in emitted_pairs:
                            is_emitted = True
                            break
            if not is_emitted and callee in emitted_last_segs:
                is_emitted = True
            cls = "emitted" if is_emitted else classify(
                callee, set(), internal_crates, internal_type_names)
            by_class[cls].append({
                "callee_name": callee,
                "enclosing_func_qn": enclosing,
            })

    total = sum(len(v) for v in by_class.values())
    summary = {
        "total_extracted_calls": total,
        "classes": {k: len(v) for k, v in by_class.items()},
        "internal_crates": sorted(internal_crates),
    }

    # Top-N callee_name patterns per class
    top_per_class: dict[str, list[tuple[str, int]]] = {}
    for cls, rows in by_class.items():
        counts = Counter(r["callee_name"] for r in rows)
        top_per_class[cls] = counts.most_common(args.top)

    Path(args.out).write_text(json.dumps({
        "summary": summary,
        "top_per_class": {k: v for k, v in top_per_class.items()},
        "samples_per_class": {k: v[:50] for k, v in by_class.items()},
    }, indent=2), encoding="utf-8")

    print("=== Classification summary ===")
    print(f"  total extracted calls: {total}")
    for cls, count in sorted(summary["classes"].items(),
                             key=lambda x: -x[1]):
        print(f"  {cls:<30}  {count:>5}  ({count / total:.1%})")
    print()
    print("=== Top callee_names per class (limit {}) ===".format(args.top))
    for cls in ("unemitted_internal", "unemitted_external",
                "unemitted_unknown", "emitted"):
        rows = top_per_class.get(cls, [])
        if not rows:
            continue
        print(f"\n  [{cls}]")
        for name, n in rows[:args.top]:
            print(f"    {n:>4}  {name}")
    print(f"\nWritten to {args.out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
