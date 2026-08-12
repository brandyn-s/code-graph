# LLVM `code_localize` efficiency — 2026-08-12

`code_localize` was profiled against a preserved LLVM graph containing 729,010
nodes and 2,308,049 edges from 113,959 files / 35,968,701 lines. The released
implementation decoded every edge property and every node property before
filtering to traversable edge types and substring seed fields.

The candidate adds lightweight store projections: typed edge endpoints without
properties and seed-candidate node fields without properties. Ranking,
traversal, and the result contract are unchanged.

| Measurement | Released | Candidate | Change |
|---|---:|---:|---:|
| Repeat median latency | 12.95 s | 3.02 s | 4.29× faster |
| Fresh-process latency | 16.10 s | 3.12 s | 5.16× faster |
| Fresh-process peak RSS | 1.784 GB | 0.627 GB | 64.9% lower |

The normalized core response SHA-256 was identical before and after:
`f50060acf519a7096f8f8c91abc17657de39566eeb18ea3a7e4cd90321100d4b`.

This result applies to `code_localize` query execution on the measured graph.
It does not reduce index size or prove distributed/organizational scale.
