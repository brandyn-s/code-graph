#!/usr/bin/env bash
# Reproduce the n=200 Loc-Bench code_localize_agent baseline.
#
# Usage:
#   bench/public/locbench/run.sh [--budget-usd N] [--out DIR] [--smoke]
#
# Requires: go, python3 with `anthropic pandas pyarrow datasets` installed,
# git, and ANTHROPIC_API_KEY (unless --smoke). VOYAGE_API_KEY is optional.
set -euo pipefail

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd)"
PIN="$ROOT/bench/accuracy/baselines/data/2026-06-12-matched-depth-n200/locbench-n200-pin.json"
PARQUET="$ROOT/bench/research/locbench.parquet"
BUDGET="${BUDGET_USD:-60}"
OUT="${OUT_DIR:-$ROOT/bench/public/locbench/results/$(date -u +%Y%m%dT%H%M%SZ)}"
SMOKE=0

while [ $# -gt 0 ]; do
    case "$1" in
        --budget-usd) BUDGET="$2"; shift 2 ;;
        --out) OUT="$2"; shift 2 ;;
        --smoke) SMOKE=1; shift ;;
        -h|--help) sed -n '2,9p' "$0"; exit 0 ;;
        *) echo "unknown argument: $1" >&2; exit 2 ;;
    esac
done

cd "$ROOT"
mkdir -p "$OUT"

echo "== verifying pin"
expected="$(cut -d' ' -f1 "$PIN.sha256")"
actual="$(shasum -a 256 "$PIN" | cut -d' ' -f1)"
[ "$expected" = "$actual" ] || { echo "pin digest mismatch: $actual != $expected" >&2; exit 1; }
# Write the harness input next to the results so the run directory is
# self-contained; the canonical pin's digest goes into provenance.json.
python3 - "$PIN" "$OUT/pin-ids.json" <<'PY'
import json, sys
pin = json.load(open(sys.argv[1]))
entries = pin["pinned_instance_ids"]
ids = [e["instance_id"] if isinstance(e, dict) else e for e in entries]
assert len(ids) == len(set(ids)) == pin["n"] == 200, (len(ids), pin.get("n"))
json.dump({"pinned_instance_ids": ids, "derived_from": "locbench-n200-pin.json"}, open(sys.argv[2], "w"), indent=1)
print(f"pin ok: {len(ids)} instances, dataset={pin.get('dataset')}")
PY

echo "== building engine and harness"
CGO_ENABLED=1 go build -o bin/code-graph.exe ./cmd/code-graph/
CGO_ENABLED=1 go build -o bench/research/eval_rank_localize/eval.exe ./bench/research/eval_rank_localize/
ENGINE_COMMIT="$(git rev-parse HEAD)"
BINARY_SHA="$(shasum -a 256 bin/code-graph.exe | cut -d' ' -f1)"

if [ ! -f "$PARQUET" ]; then
    echo "== downloading Loc-Bench parquet"
    python3 -c "from datasets import load_dataset; load_dataset('czlll/Loc-Bench_V1', split='test').to_parquet('$PARQUET')"
fi
DATASET_SHA="$(shasum -a 256 "$PARQUET" | cut -d' ' -f1)"

python3 - "$OUT/provenance.json" "$ENGINE_COMMIT" "$BINARY_SHA" "$DATASET_SHA" "$actual" <<'PY'
import json, sys, datetime
out, commit, binary, dataset, pin = sys.argv[1:]
json.dump({
    "schema_version": 1,
    "started_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
    "engine_commit": commit,
    "binary_sha256": binary,
    "dataset_sha256": dataset,
    "pin_sha256": pin,
    "baseline": {"file_acc10": 0.860, "class_acc10": 0.845, "func_acc10": 0.735, "date": "2026-05-04"},
}, open(out, "w"), indent=2)
print(f"provenance written to {out}")
PY

if [ "$SMOKE" = 1 ]; then
    echo "== smoke complete (no agent calls made)"
    exit 0
fi

: "${ANTHROPIC_API_KEY:?ANTHROPIC_API_KEY is required for the agent run}"
echo "== running the pinned n=200 batch (budget \$$BUDGET)"
LOCAGENT_ITERATIONS=2 python3 bench/research/eval_locbench_batch.py \
    --instances "$OUT/pin-ids.json" \
    --budget-usd "$BUDGET" \
    --workdir "$OUT/workdir" \
    --output "$OUT/report.md" \
    --per-case-json "$OUT/cases.json"
echo "== done: $OUT/report.md"
