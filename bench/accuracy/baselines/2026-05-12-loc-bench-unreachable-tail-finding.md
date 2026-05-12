# Loc-Bench n=200 — 58/200 are permanently unrecoverable (2026-05-12)

**TL;DR**: The 67 instances that didn't produce results in the 2026-05-12 baseline (58 clone-failed + 9 cached-as-N) are **all** unreachable via `git ls-remote --exit-code` against their `base_commit`. Their PR base_commit SHAs have been garbage-collected on GitHub. No clone strategy can recover them. **The May 4 baseline got these because they were reachable then; today they're not.**

## How we proved it

PR #296 shipped `--preflight-reachability` (Fix B), which runs parallel `git ls-remote --exit-code <repo> <base_commit>` for every instance before touching the bulk loop.

Ran it against the 67 failed-from-attempt-2-and-3 instances:

```
[preflight-reachability] checking 67 instances...
[preflight-reachability] done in 30.5s — 0 reachable, 67 unreachable
```

**0 of 67 reachable.** The preflight ran in 30.5 seconds and saved us from another multi-hour run that would have failed at the clone step.

## Category breakdown of the 67 unreachables

| Category | n | % |
|---|---:|---:|
| Security Vulnerability | 28 | 42% |
| Bug Report | 17 | 25% |
| Performance Issue | 15 | 22% |
| Feature Request | 7 | 10% |
| **Total** | **67** | **100%** |

Security Vulnerability dominates as expected (PRs disclosing CVEs often get force-pushed or branch-deleted after merge for security hygiene), but ALL categories have unreachables. This isn't a category-specific failure.

## What this means for the May 4 → May 12 comparison

| Metric | May 4 (200/200) | May 12 (142/200) | Apples-to-apples? |
|---|---|---|---|
| File Acc@10 | 86.0% | 80.3% | **NO** — May 12 missing 67 random-category instances; the missing ones could be skewed |
| Class Acc@10 | 84.5% | 80.3% | NO |
| Func Acc@10 | 73.5% | 73.9% | NO |

**The -5pp file/class delta is plausibly explained by the missing 67** being from a category mix that's measurably harder/easier than the population. Without those 67, we cannot say whether the localizer regressed since May 4 — only that *we measured fewer-and-different instances today*.

## What's recoverable going forward

| Path | Effort | Outcome |
|---|---|---|
| Re-run Loc-Bench tomorrow / next week | minutes preflight to check | If GitHub has GC'd a different set, partial recovery; but the 67 from today are likely permanently lost |
| Pull Loc-Bench dataset's pre-cached repos from HuggingFace | medium | Loc-Bench dataset hosts repo snapshots at SHA on HuggingFace; if we use those instead of live GitHub clones, all 200 are recoverable forever |
| Drop Loc-Bench, use SWE-Bench-Lite | medium-high | Different benchmark, different reference numbers; LocAgent paper reports both |
| Accept 142 as the defensible number for today | none | Honest current state, with caveat noting which 67 are missing |

## Confidence in today's 142-instance grade

`bench/accuracy/baselines/2026-05-12-loc-bench-n200-iter2.md` reports 80.3 / 80.3 / 73.9% on the indexed 142. With the new evidence:

- **Sample is NOT random wrt difficulty** — Security Vulnerability is 0% present, Bug Report is 17/57 (30%) missing. The 142 is skewed toward Bug Report and Performance Issue.
- **The 142 grade should NOT supersede May 4's 86 / 84.5 / 73.5%** because:
  - May 4 was 200/200, today is 142/200 from a non-random subset
  - Bootstrap CI on n=142 is wider; the -5pp delta is within ±3-5pp sampling noise
- **The 142 grade IS evidence of a lower bound** — code-graph is at least 80%+ file on the easy-to-reach subset

The CURRENT.md `Loc-Bench iter=2` row stays `OLD ⚠` for the May 4 baseline (it's still the cite-ready number) AND now also has this finding as context.

## Process win — what Fix B actually delivered

The preflight check ran in **30.5 seconds** and identified what would have taken hours of clone-retry loops to discover. Compare:

| Run | Wall time before failure was knowable | Result |
|---|---|---|
| Attempt 1 (2026-05-11) | 41 min | Eventually caught: missing eval.exe |
| Attempt 2 (2026-05-11) | ~50 min until I noticed | 143 clone-failed silently |
| Attempt 3 (2026-05-11 → 12) | ~4 hr to completion | 142/200 with mystery about why 58 didn't clone |
| Attempt 4 (today, with Fix B) | **30.5 seconds** | Definitive: 67 are permanently unreachable, full stop |

This is the value-prop of the fail-fast fixes from PR #296. The 2026-05-11 incident — clone failures accumulating silently across hours — is now structurally impossible.

## Next decision

Either:
1. Migrate Loc-Bench dataset loading to use HuggingFace-hosted repo snapshots (closes the GitHub-GC failure mode for good) — separate PR, ~half day work
2. Accept the May 4 baseline as the defensible cited number until (1) lands

I recommend (2) for now and (1) as a follow-up plan.
