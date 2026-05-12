"""Seed the Loc-Bench snapshot cache from existing workdir clones.

Path A (2026-05-12): the eval harness now consults a local snapshot
cache (~/.cache/locbench-snapshots/) before any live clone. This script
populates that cache from the workdir of a prior eval run (typically
C:/tmp/locbench-n200/work/), tarballing each successful clone at its
pinned base_commit.

Why this matters: 67 of 200 Loc-Bench instances are permanently
unreachable on live GitHub today (PR base_commits GC'd). The 142
instances that DID clone successfully will become unreachable too,
just on a longer timescale. Saving them as tarballs NOW locks in
reproducibility.

Usage:
    python bench/research/snapshot_locbench_seed.py [--workdir DIR]
        [--parquet DIR/locbench.parquet] [--out DIR]

Default workdir: C:/tmp/locbench-n200/work
Default parquet: bench/research/locbench.parquet (must match harness)
Default out:     ~/.cache/locbench-snapshots/

For each subdir in workdir, look up its instance_id in the parquet,
resolve to (repo, base_commit), and write <org>__<name>__<sha>.tar.gz
to the cache. Idempotent — skips entries that already exist.
"""
from __future__ import annotations

import argparse
import sys
import tarfile
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
DEFAULT_WORKDIR = Path("C:/tmp/locbench-n200/work")
DEFAULT_PARQUET = REPO_ROOT / "bench" / "research" / "locbench.parquet"
DEFAULT_OUT = Path.home() / ".cache" / "locbench-snapshots"


def _safe_tarball_name(repo: str, base_commit: str) -> str:
    safe = repo.replace("/", "__").replace("\\", "__")
    return f"{safe}__{base_commit}.tar.gz"


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=(__doc__ or "").splitlines()[0] if __doc__ else "")
    ap.add_argument("--workdir", type=Path, default=DEFAULT_WORKDIR,
                    help=f"Directory of cloned instances (default: {DEFAULT_WORKDIR})")
    ap.add_argument("--parquet", type=Path, default=DEFAULT_PARQUET,
                    help=f"Loc-Bench parquet for instance_id -> (repo, sha) lookup "
                         f"(default: {DEFAULT_PARQUET})")
    ap.add_argument("--out", type=Path, default=DEFAULT_OUT,
                    help=f"Output cache directory (default: {DEFAULT_OUT})")
    ap.add_argument("--dry-run", action="store_true",
                    help="List what would be done, don't write tarballs")
    args = ap.parse_args(argv)

    if not args.workdir.exists():
        print(f"FAIL: workdir not found: {args.workdir}", file=sys.stderr)
        return 1
    if not args.parquet.exists():
        print(f"FAIL: parquet not found: {args.parquet}", file=sys.stderr)
        return 1

    try:
        import pandas as pd
    except ImportError:
        print("FAIL: pandas required. Install with `pip install pandas pyarrow`",
              file=sys.stderr)
        return 1

    df = pd.read_parquet(args.parquet)
    # Build instance_id -> (repo, base_commit) map
    lookup: dict[str, tuple[str, str]] = {}
    for _, row in df.iterrows():
        iid = str(row["instance_id"])
        repo = str(row["repo"])
        sha = str(row["base_commit"])
        lookup[iid] = (repo, sha)
    print(f"Loaded {len(lookup)} instances from {args.parquet.name}")

    if not args.dry_run:
        args.out.mkdir(parents=True, exist_ok=True)

    subdirs = [d for d in args.workdir.iterdir() if d.is_dir()]
    print(f"Found {len(subdirs)} cloned workdirs in {args.workdir}")

    written = 0
    skipped_existing = 0
    skipped_not_in_dataset = 0
    skipped_empty = 0
    errors = 0

    for subdir in sorted(subdirs):
        iid = subdir.name
        if iid not in lookup:
            skipped_not_in_dataset += 1
            continue
        repo, sha = lookup[iid]
        # Skip dirs that don't look like a clone (no .git or empty)
        if not any(subdir.iterdir()):
            skipped_empty += 1
            continue
        tarball = args.out / _safe_tarball_name(repo, sha)
        if tarball.exists():
            skipped_existing += 1
            continue
        if args.dry_run:
            print(f"  [dry-run] {iid:50s} -> {tarball.name}")
            written += 1
            continue
        tmp = tarball.with_suffix(".tar.gz.tmp")
        try:
            with tarfile.open(tmp, "w:gz") as tf:
                tf.add(subdir, arcname=".")
            tmp.replace(tarball)
            size_mb = tarball.stat().st_size / (1024 * 1024)
            print(f"  wrote {tarball.name} ({size_mb:.1f} MB)")
            written += 1
        except (OSError, tarfile.TarError) as e:
            print(f"  ERROR {iid}: {e}", file=sys.stderr)
            tmp.unlink(missing_ok=True)
            errors += 1

    print()
    print(f"Summary:")
    print(f"  Written:           {written}")
    print(f"  Skipped (already): {skipped_existing}")
    print(f"  Skipped (not in dataset): {skipped_not_in_dataset}")
    print(f"  Skipped (empty):   {skipped_empty}")
    print(f"  Errors:            {errors}")
    if not args.dry_run:
        total_mb = sum(p.stat().st_size for p in args.out.glob("*.tar.gz")) / (1024 * 1024)
        print(f"  Total cache size:  {total_mb:.1f} MB across "
              f"{len(list(args.out.glob('*.tar.gz')))} tarballs")
    return 0 if errors == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
