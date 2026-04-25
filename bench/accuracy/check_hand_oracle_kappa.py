#!/usr/bin/env python3
"""Cohen's Kappa inter-annotator agreement checker for hand-oracle JSON files.

Motivation: single-annotator hand-oracles carry self-bias risk. When we expand
hand-oracle coverage to new languages (Rust CALLS, Python CALLS, etc.), we should
have >= 2 independent annotators per fixture and compute Cohen's Kappa to
quantify agreement. Standard bar (Code Debloating arXiv 2604.17717, CLEVER
NeurIPS 2025): kappa >= 0.81 = "almost perfect agreement".

Schema expectations
-------------------

**Single-annotator legacy path** (no kappa computed, exits 0):

    {
      "services": [
        {"service": "canstatd", "pub_topics": ["canstatd"], "sub_topics": []},
        ...
      ]
    }

**Multi-annotator path** (kappa computed, exits non-zero if kappa < 0.81):

    {
      "_annotators": ["brandyn-2026-04-24", "reviewer-tbd"],
      "services": [
        {
          "service": "canstatd",
          "source_file": "...",
          "annotations": {
            "brandyn-2026-04-24": {
              "pub_topics": ["canstatd"],
              "sub_topics": []
            },
            "reviewer-tbd": {
              "pub_topics": ["canstatd"],
              "sub_topics": []
            }
          }
        }
      ]
    }

Kappa is computed over the universe of (service, topic, role) tuples, with each
annotator's labels = {"yes" if the tuple is in that annotator's pub/sub list,
"no" otherwise}. Standard Cohen's Kappa formula is applied.

Usage
-----

    python check_hand_oracle_kappa.py path/to/hand_oracle.json
    python check_hand_oracle_kappa.py path/to/hand_oracle.json --threshold 0.81

Exit codes
----------

    0  - single annotator (kappa not applicable) OR kappa >= threshold
    1  - multi-annotator with kappa < threshold (CI-gate failure)
    2  - schema error or file not found
"""

from __future__ import annotations

import argparse
import json
import math
import sys
from collections import defaultdict
from pathlib import Path


def _cohen_kappa(labels_a: list[str], labels_b: list[str]) -> float:
    """Pure-Python Cohen's Kappa for two raters, categorical labels.

    kappa = (p_o - p_e) / (1 - p_e), where p_o is observed agreement and p_e
    is expected agreement by chance.
    """
    if len(labels_a) != len(labels_b):
        raise ValueError(f"Label vectors differ in length: {len(labels_a)} vs {len(labels_b)}")
    if not labels_a:
        return 1.0  # Vacuous agreement on empty set.

    categories = sorted(set(labels_a) | set(labels_b))
    n = len(labels_a)

    # Observed agreement.
    agree = sum(1 for a, b in zip(labels_a, labels_b) if a == b)
    p_o = agree / n

    # Expected agreement.
    count_a: dict[str, int] = defaultdict(int)
    count_b: dict[str, int] = defaultdict(int)
    for a, b in zip(labels_a, labels_b):
        count_a[a] += 1
        count_b[b] += 1
    p_e = sum((count_a[c] / n) * (count_b[c] / n) for c in categories)

    if math.isclose(p_e, 1.0):
        return 1.0  # Perfect chance agreement; kappa undefined — treat as 1.0.
    return (p_o - p_e) / (1 - p_e)


def _build_label_vectors(annotations_by_annotator: dict[str, dict]) -> dict[str, list[str]]:
    """Collect labels per annotator over the full universe of (service, topic, role) tuples.

    Returns: dict[annotator_id -> list[str]] where each list is aligned by tuple order.
    """
    # Discover full universe of (service, topic, role) tuples across all annotators.
    universe: set[tuple[str, str, str]] = set()
    for annotator, services in annotations_by_annotator.items():
        for svc_name, svc_ann in services.items():
            for topic in svc_ann.get("pub_topics", []):
                universe.add((svc_name, topic, "pub"))
            for topic in svc_ann.get("sub_topics", []):
                universe.add((svc_name, topic, "sub"))
    ordered = sorted(universe)

    # For each annotator, produce a label vector of "yes"/"no" per tuple.
    vectors: dict[str, list[str]] = {}
    for annotator, services in annotations_by_annotator.items():
        vec = []
        for svc_name, topic, role in ordered:
            svc_ann = services.get(svc_name, {})
            topics = svc_ann.get(f"{role}_topics", [])
            vec.append("yes" if topic in topics else "no")
        vectors[annotator] = vec
    return vectors


def _parse_oracle(path: Path) -> tuple[list[str], dict[str, dict]]:
    """Parse oracle JSON. Supports single-annotator legacy and multi-annotator schemas.

    Returns: (annotator_ids, dict[annotator_id -> dict[service_name -> {pub_topics, sub_topics}]])
    """
    data = json.loads(path.read_text(encoding="utf-8"))
    services = data.get("services", [])
    if not services:
        raise ValueError(f"No 'services' array in {path}")

    declared_annotators = data.get("_annotators", [])
    has_multi = any("annotations" in entry for entry in services)

    if has_multi:
        annotator_ids = declared_annotators or sorted(
            {aid for entry in services for aid in entry.get("annotations", {})}
        )
        result: dict[str, dict] = {aid: {} for aid in annotator_ids}
        for entry in services:
            svc = entry["service"]
            ann_map = entry.get("annotations", {})
            for aid in annotator_ids:
                ann = ann_map.get(aid, {})
                result[aid][svc] = {
                    "pub_topics": ann.get("pub_topics", []),
                    "sub_topics": ann.get("sub_topics", []),
                }
        return annotator_ids, result

    # Legacy single-annotator path: top-level pub_topics/sub_topics per service.
    legacy_id = declared_annotators[0] if declared_annotators else "default"
    result = {legacy_id: {}}
    for entry in services:
        svc = entry["service"]
        result[legacy_id][svc] = {
            "pub_topics": entry.get("pub_topics", []),
            "sub_topics": entry.get("sub_topics", []),
        }
    return [legacy_id], result


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("oracle_path", type=Path, help="Path to hand-oracle JSON file")
    parser.add_argument("--threshold", type=float, default=0.81, help="Minimum acceptable kappa (default: 0.81)")
    args = parser.parse_args()

    if not args.oracle_path.exists():
        print(f"ERROR: file not found: {args.oracle_path}", file=sys.stderr)
        return 2

    try:
        annotator_ids, annotations = _parse_oracle(args.oracle_path)
    except (json.JSONDecodeError, ValueError) as e:
        print(f"ERROR: schema parse failed: {e}", file=sys.stderr)
        return 2

    print(f"Oracle file: {args.oracle_path}")
    print(f"Annotators ({len(annotator_ids)}): {', '.join(annotator_ids)}")
    service_count = len(next(iter(annotations.values()), {}))
    print(f"Services: {service_count}")

    if len(annotator_ids) < 2:
        print("Single-annotator oracle — Cohen's Kappa not applicable.")
        print("To enable agreement measurement, add a second annotator (see bench/accuracy/README.md).")
        return 0

    vectors = _build_label_vectors(annotations)
    # Pairwise kappa; report minimum (most conservative) across all pairs.
    min_kappa = 1.0
    min_pair = None
    for i, a in enumerate(annotator_ids):
        for b in annotator_ids[i + 1 :]:
            kappa = _cohen_kappa(vectors[a], vectors[b])
            print(f"  kappa({a}, {b}) = {kappa:.4f}")
            if kappa < min_kappa:
                min_kappa = kappa
                min_pair = (a, b)

    print(f"\nMinimum pairwise kappa: {min_kappa:.4f} (pair: {min_pair})")
    print(f"Threshold: {args.threshold:.4f}")

    if min_kappa < args.threshold:
        print(f"FAIL: kappa {min_kappa:.4f} < {args.threshold:.4f}. Revisit fixture or annotator protocol.")
        return 1
    print(f"PASS: kappa {min_kappa:.4f} >= {args.threshold:.4f} (almost-perfect agreement).")
    return 0


if __name__ == "__main__":
    sys.exit(main())
