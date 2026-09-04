# Compiler-tier CALLS accuracy — 2026-08-12

A pre-public internal build (source revision `41b84009`) was evaluated against an independent Go
SSA/RTA oracle. The oracle loads source with `go/packages`, builds SSA, runs RTA
with all source functions as roots, and emits caller/callee definition
coordinates. It does not read SCIP, a code-graph database, or code-graph output.

The candidate edges were limited to `CALLS` edges whose resolver is
`scip-ingest` and whose `resolution_artifact_sha256` equals the exact SCIP index
used for that fixture. Dynamic RTA edges are reported but excluded because the
current SCIP ingestion contract emits only statically resolved call sites.

| Fixture | TP | FP | FN | Precision | Recall | F1 |
|---|---:|---:|---:|---:|---:|---:|
| Synthetic hand-enumerated gate | 5 | 0 | 0 | 1.000 | 1.000 | 1.000 |
| code-graph at `41b8400` | 2,643 | 50 | 154 | 0.981 | 0.945 | 0.963 |
| spf13/cobra at `f2878ba` | 504 | 52 | 77 | 0.906 | 0.867 | 0.887 |
| Real-fixture aggregate | 3,147 | 102 | 231 | 0.969 | 0.932 | 0.950 |

The preregistered gates pass: synthetic FP/FN are zero; real-fixture aggregate
precision and recall both exceed 0.90; neither real fixture falls below 0.80
F1. The weaker Cobra result remains visible and prevents a compiler-perfect or
language-general claim. This result supports a strong Go compiler-tier CALLS
grade only; TypeScript automation still requires its own independent oracle.

Reproduce with:

```bash
cd bench/accuracy/tools/oracle-go-rta
go test ./...
go run . /path/to/go/module > oracle.json

python3 bench/accuracy/compare_compiler_calls.py \
  --oracle oracle.json \
  --database /path/to/project.db \
  --project project-name \
  --scip-index /path/to/index.scip \
  --output report.json
```
