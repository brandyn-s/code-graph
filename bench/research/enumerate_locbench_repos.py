"""Enumerate Loc-Bench source repos + per-repo issue counts from locbench.parquet.

Output:
  - Distinct (org, repo) pairs sorted by instance count
  - Per-instance ground-truth issue numbers (to exclude from corpus mining)
  - Total instances per repo
"""

import json
import sys
from collections import Counter
from pathlib import Path

try:
    import pyarrow.parquet as pq
except ImportError:
    print("ERROR: pyarrow not installed. pip install pyarrow", file=sys.stderr)
    sys.exit(1)


def main() -> None:
    parquet_path = Path(__file__).parent / "locbench.parquet"
    if not parquet_path.exists():
        print(f"ERROR: {parquet_path} not found", file=sys.stderr)
        sys.exit(1)

    table = pq.read_table(parquet_path)
    print(f"# locbench.parquet schema:\n{table.schema}\n", file=sys.stderr)
    print(f"# rows: {table.num_rows}\n", file=sys.stderr)

    df = table.to_pandas()
    print(f"# columns: {list(df.columns)}\n", file=sys.stderr)

    # Try common SWE-bench-style column names
    repo_col = None
    for cand in ("repo", "repo_name", "repository", "project"):
        if cand in df.columns:
            repo_col = cand
            break

    if repo_col is None:
        print(f"ERROR: no repo column found. Columns: {list(df.columns)}", file=sys.stderr)
        sys.exit(1)

    print(f"# Using repo column: {repo_col}\n", file=sys.stderr)

    repo_counts = Counter(df[repo_col].tolist())

    # Issue ID column for exclusion
    issue_col = None
    for cand in ("issue_number", "issue_id", "instance_id", "pr_number", "number"):
        if cand in df.columns:
            issue_col = cand
            break
    print(f"# Issue/instance column: {issue_col}\n", file=sys.stderr)

    output = {
        "total_instances": int(df.shape[0]),
        "distinct_repos": len(repo_counts),
        "repos_by_instance_count": [
            {"repo": str(r), "instance_count": int(c)}
            for r, c in repo_counts.most_common()
        ],
    }

    if issue_col:
        # Group by repo → list of eval-set issue identifiers
        eval_issues_per_repo: dict[str, list] = {}
        for _, row in df.iterrows():
            r = str(row[repo_col])
            eval_issues_per_repo.setdefault(r, []).append(str(row[issue_col]))
        output["eval_issues_per_repo"] = {
            r: sorted(set(ids)) for r, ids in eval_issues_per_repo.items()
        }
        output["eval_issue_column"] = issue_col

    print(json.dumps(output, indent=2))


if __name__ == "__main__":
    main()
