"""Mine closed issues + resolving PRs from top-N Loc-Bench source repos.

Output: JSONL with one record per issue at
  ~/.cache/code-graph/episodic-memory/locbench-issues.jsonl

Strategy: GraphQL query per repo fetches (issue, linked PR, changed files)
in one call, paginated up to --max-issues per repo. Excludes eval-set PR
numbers per repo (extracted from Loc-Bench instance_id pattern).

See research/2026-05-04-c1-issue-corpus-strategy.md for design.

Usage:
  python mine_locbench_issues.py --top-n 30 --max-issues 100
  python mine_locbench_issues.py --repos django/django --max-issues 10  # validate
"""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
import time
from pathlib import Path

# GraphQL: fetch merged PRs + changed files + linked issues in one query.
# PRs work uniformly across repos (works even where GitHub Issues are disabled,
# e.g. django/django which uses Trac). Linked issue body, if present, enriches
# the record but is not required.
QUERY = """
query($owner: String!, $name: String!, $cursor: String) {
  repository(owner: $owner, name: $name) {
    pullRequests(states: MERGED, first: 50, after: $cursor, orderBy: {field: CREATED_AT, direction: DESC}) {
      pageInfo { endCursor hasNextPage }
      nodes {
        number
        title
        body
        mergedAt
        files(first: 50) {
          nodes { path }
        }
        closingIssuesReferences(first: 3) {
          nodes {
            number
            title
            body
          }
        }
      }
    }
  }
}
"""


def parse_eval_prs(locbench_repos_json: Path) -> dict[str, set[int]]:
    """Extract per-repo eval PR numbers from instance_id patterns.

    instance_id format: {owner}__{repo}-{pr_number}
    """
    data = json.loads(locbench_repos_json.read_text(encoding="utf-8"))
    eval_issues = data.get("eval_issues_per_repo", {})
    eval_prs: dict[str, set[int]] = {}
    pat = re.compile(r"^[^_]+__[^-]+-(\d+)$")
    for repo, ids in eval_issues.items():
        prs = set()
        for instance_id in ids:
            m = pat.match(instance_id)
            if m:
                prs.add(int(m.group(1)))
        eval_prs[repo] = prs
    return eval_prs


def gh_graphql(query: str, variables: dict) -> dict:
    """Call GitHub GraphQL via gh CLI."""
    cmd = ["gh", "api", "graphql", "-f", f"query={query}"]
    for k, v in variables.items():
        cmd.extend(["-F", f"{k}={v}"])
    result = subprocess.run(
        cmd, capture_output=True, check=False, env={**os.environ, "MSYS_NO_PATHCONV": "1"}
    )
    if result.returncode != 0:
        stderr = result.stderr.decode("utf-8", errors="replace")
        raise RuntimeError(f"gh graphql failed: {stderr}")
    return json.loads(result.stdout.decode("utf-8", errors="replace"))


def mine_repo(
    repo: str,
    max_records: int,
    eval_prs: set[int],
) -> list[dict]:
    """Mine merged PRs from one repo with body, changed files, linked issues."""
    owner, name = repo.split("/", 1)
    cursor: str | None = None
    records: list[dict] = []
    skipped_thin = 0
    skipped_no_files = 0
    skipped_eval = 0

    while len(records) < max_records:
        variables: dict = {"owner": owner, "name": name}
        if cursor:
            variables["cursor"] = cursor
        resp = gh_graphql(QUERY, variables)
        if "errors" in resp:
            print(f"  GraphQL errors for {repo}: {resp['errors']}", file=sys.stderr)
            break
        repo_node = resp.get("data", {}).get("repository")
        if not repo_node:
            print(f"  No repository data for {repo}", file=sys.stderr)
            break
        prs = repo_node["pullRequests"]
        for pr in prs["nodes"]:
            pr_num = pr["number"]
            if pr_num in eval_prs:
                skipped_eval += 1
                continue
            body = pr.get("body") or ""
            if len(body) < 100:
                skipped_thin += 1
                continue
            files = [f["path"] for f in pr.get("files", {}).get("nodes", [])]
            if not files:
                skipped_no_files += 1
                continue
            linked = []
            for iss in pr.get("closingIssuesReferences", {}).get("nodes", []):
                linked.append({
                    "issue_number": iss["number"],
                    "issue_title": iss.get("title", ""),
                    "issue_body": iss.get("body", "") or "",
                })
            records.append({
                "org": owner,
                "repo": name,
                "pr_number": pr_num,
                "pr_title": pr.get("title", ""),
                "pr_body": body,
                "merged_at": pr.get("mergedAt"),
                "changed_files": files,
                "linked_issues": linked,
                "is_eval_excluded": False,
            })
            if len(records) >= max_records:
                break
        page_info = prs.get("pageInfo", {})
        if not page_info.get("hasNextPage"):
            break
        cursor = page_info.get("endCursor")
        time.sleep(0.5)  # gentle backoff

    print(
        f"  {repo}: {len(records)} kept | skipped: "
        f"thin-body={skipped_thin}, no-files={skipped_no_files}, eval-excluded={skipped_eval}",
        file=sys.stderr,
    )
    return records


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--top-n", type=int, default=30, help="Top N repos by instance count")
    parser.add_argument("--max-issues", type=int, default=100, help="Max issues per repo")
    parser.add_argument(
        "--repos",
        type=str,
        default=None,
        help="Comma-separated repo list to override top-N (e.g., django/django,scikit-learn/scikit-learn)",
    )
    parser.add_argument(
        "--output",
        type=Path,
        default=Path.home() / ".cache" / "code-graph" / "episodic-memory" / "locbench-issues.jsonl",
        help="Output JSONL path",
    )
    parser.add_argument(
        "--locbench-repos-json",
        type=Path,
        default=Path(__file__).parent / "locbench_repos.json",
        help="Path to enumerate_locbench_repos.py output",
    )
    args = parser.parse_args()

    # locbench JSON is required only when we need eval-PR exclusion
    # (Loc-Bench corpus). For other corpora (e.g. redacted-internal mining),
    # --repos is passed explicitly and no exclusion is needed.
    eval_prs_by_repo: dict[str, set[int]] = {}
    if args.locbench_repos_json.exists():
        eval_prs_by_repo = parse_eval_prs(args.locbench_repos_json)
        data = json.loads(args.locbench_repos_json.read_text(encoding="utf-8"))
    else:
        data = None

    if args.repos:
        repos = args.repos.split(",")
    else:
        if data is None:
            print(
                f"ERROR: --repos not given AND {args.locbench_repos_json} not found. "
                "Either pass --repos, or run enumerate_locbench_repos.py first.",
                file=sys.stderr,
            )
            sys.exit(1)
        repos = [r["repo"] for r in data["repos_by_instance_count"][: args.top_n]]

    args.output.parent.mkdir(parents=True, exist_ok=True)

    total = 0
    print(f"Mining {len(repos)} repos, max {args.max_issues} issues each", file=sys.stderr)
    print(f"Output: {args.output}", file=sys.stderr)

    with args.output.open("w", encoding="utf-8") as f:
        for i, repo in enumerate(repos):
            eval_prs = eval_prs_by_repo.get(repo, set())
            print(f"[{i+1}/{len(repos)}] {repo} (eval PRs to exclude: {len(eval_prs)})", file=sys.stderr)
            try:
                records = mine_repo(repo, args.max_issues, eval_prs)
            except Exception as e:
                print(f"  FAILED: {e}", file=sys.stderr)
                continue
            for r in records:
                f.write(json.dumps(r) + "\n")
            total += len(records)

    print(f"\nTotal records: {total}", file=sys.stderr)
    print(f"Wrote: {args.output}", file=sys.stderr)


if __name__ == "__main__":
    main()
