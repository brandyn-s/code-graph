"""Gate alpha probe for A-2: weighted PageRank on 5-node hand-verified graph.

Graph:
  A <-> B  (mutual link)
  A -> C, B -> C
  C -> D
  D -> E

Query-weight on C = 1.0 (injected personalization), everyone else = 0.
Hand-computed expected ranking: C > A ~= B > D > E (C is highest due to direct
personalization and inbound A/B; A/B get lifted by the A<->B cycle; D/E trail).

Pass criterion: ranking order matches C, {A, B}, D, E.
"""
import numpy as np

nodes = ['A', 'B', 'C', 'D', 'E']
idx = {n: i for i, n in enumerate(nodes)}
edges = [('A','B'), ('B','A'), ('A','C'), ('B','C'), ('C','D'), ('D','E')]
n = len(nodes)
M = np.zeros((n, n))
for src, dst in edges:
    M[idx[dst], idx[src]] += 1
col_sum = M.sum(axis=0)
col_sum[col_sum == 0] = 1
M = M / col_sum
p = np.zeros(n); p[idx['C']] = 1.0
d = 0.85
rank = np.ones(n) / n
for _ in range(50):
    rank = d * M @ rank + (1 - d) * p
ordered = sorted(zip(nodes, rank), key=lambda x: -x[1])
for name, r in ordered:
    print(f"{name}: {r:.4f}")
