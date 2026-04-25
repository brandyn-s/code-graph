# Filter-Pass Decision Rules — Code-Graph Research-to-Action

This file defines how `/gather-research` and `/gather-intel` outputs are
filtered into a validated action list. Without these rules, recommendations
become a reading list instead of a commit plan.

**Status**: DRAFT — awaiting user approval before Phase B gathers run.

**Scope**: applies only to findings that target code-graph improvements.
Findings outside this scope (general architecture patterns, tangential
tooling) do not pass through this filter — they go straight to the topic
file or skip entirely.

---

## Tier Rubric

| Tier | Meaning | Required evidence | What happens next |
|------|---------|------------------|-------------------|
| **A — IMPLEMENT** | Probe-gated commit within 2 weeks | ≥1 published F1 number OR fits an already-identified gap in our measured F1 (e.g., Go CALLS 0.45 fixture) AND cleared compare-by-need 5 gates AND has a defined probe | Goes into final /superplan (Phase D) for per-item implementation plan |
| **B — PROBE-GATED BACKLOG** | Prototype before committing engineering time | Technique is credible, fits architecture, probe definition is specified (query + fixture + threshold) | Added to `bench/research/probe-backlog.md` with exit criteria |
| **C — AWARENESS** | Monitor, no action | Pre-2024 source, OR production validation absent, OR requires backend/parser change | Appended to research report; surfaces in next gather-research run |
| **D — REJECT** | Do not pursue | Vendor marketing without numbers, duplicate of current architecture, OR fails compare-by-need gate 3 (no verified problem in our system) | Logged with rejection reason; does not appear in action list |

---

## Filter Rules (applied at gather-output consumption)

**R1 — Evidence floor**
  Require ≥1 cited production F1 number OR ≥1 cited academic benchmark.
  Findings without either → Tier C minimum, regardless of how interesting.

**R2 — Implementation cost cap**
  Must fit in one of:
  - 2-day probe (prove value on synthetic fixture + one real-repo sample)
  - 5-day build-and-measure (prototype + F1 on hand-oracle set)
  Longer than 5 days → Tier B at best. Longer than 10 days → Tier C.

**R3 — Storage backend exclusion**
  Reject if requires SQLite-WAL replacement. SQLite WAL is committed
  through ~500K LOC. Backend switches are gate-able only by evidence
  of scale issues (which we don't have yet).

**R4 — Tree-sitter exclusion**
  Reject if replaces tree-sitter. Augmentation only (LSP, LLM validation,
  oracle pairs, dataflow overlay — all fine).

**R5 — Compare-by-need gates (all 5 must pass)**
  1. What already covers this problem in code-graph? (Read, don't assume)
  2. How does the target workflow (docs pipeline) solve this today?
  3. Is there concrete friction? How often? What's the impact?
  4. Adoption cost vs value (context, maintenance, learning curve)
  5. Framed as delta: "X adds Y that current tools don't cover, and Y matters because Z"
  Failing any gate → Tier C or D.

**R6 — DEFER-challenge**
  If filter would downgrade to Tier C based on "no incident," first apply
  the 3 DEFER-challenge questions (compare-by-need.md procedure):
  - Are there incidents with different labels sharing this root cause?
  - Is the gap caught by human oversight that should be caught earlier?
  - Is implementation cost actually high, or inflated to justify inaction?
  If DEFER survives → note "challenged and confirmed." If not → upgrade.

**R7 — Frontier techniques require probe definition for Tier B**
  LLM-augmented (R6 question), probabilistic (R12), GraphRAG (R9),
  incremental indexing (R7), federated (R14) cannot reach Tier B without
  a specific probe: `query=<what to measure>`, `fixture=<where to measure>`,
  `threshold=<pass/fail cutoff>`. No probe → Tier C.

**R8 — LLM-augmented asymmetric confidence bar**
  Any recommendation involving LLM in the extraction or validation path
  requires: published accuracy measurement OR shadow-mode measurement
  target ≥ **90%** accuracy on labeled data before auto-apply. Lower
  accuracy acceptable for "propose, human validates" modes (reduced
  blast radius), but the claim language must reflect it.

---

## Probe-First Gates (applied per Tier-A item in Phase C3)

**Gate α — Prove the instrument**
  Before measuring the technique on real data, build a small synthetic
  fixture with hand-verified ground truth (≤20 units — edges, nodes,
  calls, whatever the technique touches). Measurement harness must
  produce FP=0, FN=0 on it. If instrument gives wrong numbers on the
  fixture, it'll give wrong numbers on everything. Fix instrument first.

**Gate β — Known-positive validation**
  The proposed heuristic/technique must catch ≥1 edge in our actual
  data that we already know should match. If it can't catch a known
  positive, stop — the technique doesn't fit our data shape.

**Gate γ — Bucket the gap**
  If recommendation targets an accuracy gap (e.g., "this fixes Python
  CALLS F1 gap"), enumerate the full FP/FN sets and bucket by pattern.
  Reject the recommendation if its claimed bucket represents <5pp of
  the total gap — low-impact fixes consume engineering time without
  meaningful F1 movement.

**Budget**: hard cap of 30 min per candidate for running α/β/γ inline.
Exceeds the cap → demote to Tier B with reason "too expensive to validate
inline." Building infrastructure to run the probe violates scope-discipline.

---

## Budget Caps

- **Tavily credits**: ~70 total across both gathers (35/skill baseline; extend to 100 if user approves)
- **Per-candidate probe time**: 30 min max (Gate α/β/γ combined)
- **Total filter-pass time**: 2 hours main-thread (C1 merge → C3 validation)
- **Tier A cap**: 5 items maximum. Overflow → Tier B. Over-filling Tier A violates scope discipline.

---

## Output Artifacts

The filter pass produces these files in `bench/research/`:

| File | Content | Created in |
|------|---------|-----------|
| `merged-findings-raw.md` | Both gather outputs merged, no filtering | Phase C1 |
| `filtered-findings.md` | Every finding classified A/B/C/D with rule citation | Phase C2 |
| `validated-actions.md` | Tier-A items with probe evidence (α/β/γ pass/fail) | Phase C3 |
| `probe-backlog.md` | Tier-B items with exit criteria | Phase C3 |

The final `/superplan` (Phase D) reads `validated-actions.md` and produces
per-item implementation plans.

---

## Open Questions (user decides before gathers run)

1. **Gather order**: plan runs intel → research (skill's documented coordination
   pattern lets research cross-reference intel). Confirm, or flip?
2. **Credit budget**: 70 credits default. Raise to 100 for deeper
   `tavily_research(pro)` on R6/R9?
3. **R2 cost threshold**: "2-day probe / 5-day build" for Tier A. Adjust?
4. **R8 LLM accuracy bar**: drafted at 90%. Pick: 85% / 90% / 95%?
5. **Output location**: drafted at `code-graph/bench/research/`. Alternative: `knowledge-base/research/`.
6. **Probe budget for C3**: 30 min per candidate. Tighter (15) or looser (60)?
7. **Intermediate review**: skill's Step 11 already gates per-section approval. Additional review before C1 merge, or trust that?

---

*Decision rules are deliberately strict. If they reject too much, we tune up
after seeing actual gather output. Starting strict ensures the first pass of
"what to build" is defensible.*
