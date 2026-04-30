"""Smoke-test the modal split logic on synthetic data.

Run from repo root: python bench/accuracy/_smoke_modal.py
Confirms compute_metrics handles each union without crash and that the
metrics shift in the expected direction.
"""
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from common import Edge  # noqa: E402
from compare import compute_metrics  # noqa: E402


def main() -> int:
    oracle = [
        Edge(from_qn="a.b.f1", to_qn="a.b.f2", type="CALLS", file="a.go", line=1, source="oracle"),
        Edge(from_qn="a.b.f1", to_qn="a.b.f3", type="CALLS", file="a.go", line=2, source="oracle"),
        Edge(from_qn="a.b.f2", to_qn="a.b.f3", type="CALLS", file="a.go", line=3, source="oracle"),
    ]
    real = [
        Edge(from_qn="a.b.f1", to_qn="a.b.f2", type="CALLS", file="a.go", line=1, source="cg"),
        Edge(from_qn="a.b.f1", to_qn="a.b.f3", type="CALLS", file="a.go", line=2, source="cg"),
    ]
    external = [
        Edge(from_qn="a.b.f1", to_qn="fmt.Println", type="CALLS_EXTERNAL", file="a.go", line=1, source="cg"),
    ]
    pseudo = [
        Edge(from_qn="a.b", to_qn="a.b.init", type="CALLS_PSEUDO", file="a.go", line=0, source="cg"),
    ]

    mr = compute_metrics(oracle, real)
    mre = compute_metrics(oracle, real + external)
    mrp = compute_metrics(oracle, real + pseudo)
    mall = compute_metrics(oracle, real + external + pseudo)

    print("real_only          :", mr["exact"])
    print("real_plus_external :", mre["exact"])
    print("real_plus_pseudo   :", mrp["exact"])
    print("all_calls_family   :", mall["exact"])

    # Sanity: real_only should have highest precision (no synthetic noise)
    assert mr["exact"]["precision"] >= mre["exact"]["precision"], "external must dilute precision"
    assert mr["exact"]["precision"] >= mrp["exact"]["precision"], "pseudo must dilute precision"
    assert mall["measured_count"] == 4, f"expected 4, got {mall.get('measured_count')}"
    print("OK")
    return 0


if __name__ == "__main__":
    sys.exit(main())
