"""Provenance manifest tests (Family B — measurement-discipline pay-down).

Family B is the third leg of the three-leg stool. The manifest captures
the generation context of every published number — harness SHA, eval/
index binary SHAs, dataset SHA, scorer schema version, agent iteration,
modes, max_mb. When a current report is compared against a baseline,
any difference in the comparable-keys set REFUSES the report unless
explicitly accepted.

Per the 2026-05-04 incident-backport experiment, Family B catches 4/7
documented incidents (the largest count of any single gate), all
sharing the shape "comparing two numbers across mismatched generation
contexts" — incidents 1, 5, 6, 7.

Run via: `python -m pytest bench/research/test_provenance.py -v`
"""
from __future__ import annotations

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))

from eval_locbench_compare import (  # noqa: E402
    PROVENANCE_COMPARE_KEYS,
    SCORER_SCHEMA_VERSION,
    _check_provenance_match,
    _check_report_invariants,
    _compute_provenance_manifest,
    _file_sha256_short,
    _git_sha_short,
    _parse_provenance_manifest,
    _render_provenance_table,
    InstanceResult,
    ModeResult,
)


# ----------------------------------------------------------------------
# Manifest computation
# ----------------------------------------------------------------------

class TestComputeManifest:
    def test_manifest_has_required_keys(self, tmp_path):
        eb = tmp_path / "eval.exe"
        eb.write_bytes(b"fake binary content")
        ib = tmp_path / "idx.exe"
        ib.write_bytes(b"different fake binary")

        m = _compute_provenance_manifest(eb, ib, ["hybrid-agent"], 1000, 50, 18)
        required = {
            "harness_sha", "scorer_schema", "eval_bin_sha", "index_bin_sha",
            "dataset_sha", "agent_iterations", "modes", "max_mb",
            "n_attempted", "n_indexed", "timestamp_utc",
        }
        assert required.issubset(m.keys()), (
            f"Missing keys: {required - set(m.keys())}"
        )

    def test_manifest_values_present(self, tmp_path):
        eb = tmp_path / "eval.exe"
        eb.write_bytes(b"fake binary content")
        ib = tmp_path / "idx.exe"
        ib.write_bytes(b"different fake binary")

        m = _compute_provenance_manifest(eb, ib, ["hybrid-agent", "substring-primitives"], 5000, 200, 192)
        assert m["scorer_schema"] == str(SCORER_SCHEMA_VERSION)
        assert m["modes"] == "hybrid-agent,substring-primitives"
        assert m["max_mb"] == "5000"
        assert m["n_attempted"] == "200"
        assert m["n_indexed"] == "192"
        # Binary SHAs should differ for different content
        assert m["eval_bin_sha"] != m["index_bin_sha"]

    def test_missing_binary_marked(self, tmp_path):
        """Missing binary file → '<missing>' marker in manifest."""
        m = _compute_provenance_manifest(
            tmp_path / "nonexistent_eval.exe",
            tmp_path / "nonexistent_idx.exe",
            ["hybrid-agent"], 1000, 0, 0,
        )
        assert m["eval_bin_sha"] == "<missing>"
        assert m["index_bin_sha"] == "<missing>"


class TestFileSha256:
    def test_same_content_same_sha(self, tmp_path):
        a = tmp_path / "a.bin"
        b = tmp_path / "b.bin"
        a.write_bytes(b"same content")
        b.write_bytes(b"same content")
        assert _file_sha256_short(a) == _file_sha256_short(b)

    def test_different_content_different_sha(self, tmp_path):
        a = tmp_path / "a.bin"
        b = tmp_path / "b.bin"
        a.write_bytes(b"content one")
        b.write_bytes(b"content two")
        assert _file_sha256_short(a) != _file_sha256_short(b)

    def test_missing_returns_sentinel(self, tmp_path):
        assert _file_sha256_short(tmp_path / "nope.bin") == "<missing>"

    def test_sha_length_is_12(self, tmp_path):
        f = tmp_path / "x.bin"
        f.write_bytes(b"xx")
        assert len(_file_sha256_short(f)) == 12


# ----------------------------------------------------------------------
# Render + parse round-trip
# ----------------------------------------------------------------------

class TestRenderParseRoundtrip:
    def test_render_then_parse_recovers_fields(self, tmp_path):
        manifest = {
            "harness_sha": "abc123def456",
            "scorer_schema": "2",
            "eval_bin_sha": "feedface1234",
            "index_bin_sha": "deadbeef5678",
            "dataset_sha": "0123456789ab",
            "agent_iterations": "2",
            "modes": "hybrid-agent",
            "max_mb": "5000",
            "n_attempted": "200",
            "n_indexed": "192",
            "timestamp_utc": "2026-05-04T13:00:00Z",
        }
        report_lines = ["# Test report", ""]
        report_lines.extend(_render_provenance_table(manifest))
        report_lines.append("## Aggregate")  # next section
        report_lines.append("dummy aggregate content")
        report_path = tmp_path / "report.md"
        report_path.write_text("\n".join(report_lines), encoding="utf-8")

        parsed = _parse_provenance_manifest(report_path)
        assert parsed is not None
        for key, val in manifest.items():
            assert parsed.get(key) == val, f"key {key}: got {parsed.get(key)} want {val}"

    def test_parse_missing_file_returns_none(self, tmp_path):
        assert _parse_provenance_manifest(tmp_path / "nope.md") is None

    def test_parse_report_without_manifest_returns_none(self, tmp_path):
        report = tmp_path / "old_report.md"
        report.write_text("# Old report\n\nNo manifest here.\n## Aggregate\n", encoding="utf-8")
        assert _parse_provenance_manifest(report) is None


# ----------------------------------------------------------------------
# Comparison gate
# ----------------------------------------------------------------------

class TestProvenanceMatch:
    @staticmethod
    def _ref() -> dict[str, str]:
        return {
            "harness_sha": "abc123def456",
            "scorer_schema": "2",
            "eval_bin_sha": "feedface1234",
            "index_bin_sha": "deadbeef5678",
            "dataset_sha": "0123456789ab",
            "agent_iterations": "2",
            "modes": "hybrid-agent",
            "max_mb": "5000",
            "n_attempted": "200",
            "n_indexed": "192",
            "timestamp_utc": "2026-05-04T13:00:00Z",
        }

    def test_identical_manifests_pass(self):
        a = self._ref()
        b = self._ref()
        violations = _check_provenance_match(a, b)
        assert violations == []

    def test_timestamp_difference_does_not_refuse(self):
        """Timestamps differ legitimately between runs; not in compare keys."""
        a = self._ref()
        b = dict(a)
        b["timestamp_utc"] = "2026-05-05T13:00:00Z"
        violations = _check_provenance_match(a, b)
        assert violations == []

    def test_n_attempted_difference_does_not_refuse(self):
        """n_attempted differs legitimately (rerun on subset)."""
        a = self._ref()
        b = dict(a)
        b["n_attempted"] = "100"
        violations = _check_provenance_match(a, b)
        assert violations == []

    def test_eval_bin_mismatch_refuses(self):
        a = self._ref()
        b = dict(a)
        b["eval_bin_sha"] = "00000000aaaa"
        violations = _check_provenance_match(a, b)
        refusals = [v for v in violations if v.startswith("REFUSE:")]
        assert len(refusals) == 1
        assert "eval_bin_sha" in refusals[0]

    def test_scorer_schema_mismatch_refuses(self):
        """Scorer schema bump means scoring semantics changed; cannot compare."""
        a = self._ref()
        b = dict(a)
        b["scorer_schema"] = "1"  # older schema (pre-ACC-012)
        violations = _check_provenance_match(a, b)
        refusals = [v for v in violations if v.startswith("REFUSE:")]
        assert len(refusals) == 1
        assert "scorer_schema" in refusals[0]

    def test_dataset_mismatch_refuses(self):
        a = self._ref()
        b = dict(a)
        b["dataset_sha"] = "ffffffffffff"
        violations = _check_provenance_match(a, b)
        refusals = [v for v in violations if v.startswith("REFUSE:")]
        assert len(refusals) == 1
        assert "dataset_sha" in refusals[0]

    def test_agent_iterations_mismatch_refuses(self):
        a = self._ref()
        b = dict(a)
        b["agent_iterations"] = "1"
        violations = _check_provenance_match(a, b)
        refusals = [v for v in violations if v.startswith("REFUSE:")]
        assert len(refusals) == 1

    def test_multiple_mismatches_all_listed(self):
        a = self._ref()
        b = dict(a)
        b["eval_bin_sha"] = "00000000aaaa"
        b["dataset_sha"] = "ffffffffffff"
        b["scorer_schema"] = "1"
        violations = _check_provenance_match(a, b)
        refusals = [v for v in violations if v.startswith("REFUSE:")]
        assert len(refusals) == 3

    def test_accept_override_converts_refuse_to_accepted(self):
        a = self._ref()
        b = dict(a)
        b["eval_bin_sha"] = "00000000aaaa"
        violations = _check_provenance_match(
            a, b, accept_provenance_mismatch="known regression test, eval_bin v2 in baseline"
        )
        refusals = [v for v in violations if v.startswith("REFUSE:")]
        accepted = [v for v in violations if v.startswith("[ACCEPTED")]
        assert refusals == []
        assert len(accepted) == 1


# ----------------------------------------------------------------------
# Compare-keys list (catches "drift in what's compared")
# ----------------------------------------------------------------------

class TestCompareKeysContract:
    def test_compare_keys_excludes_runtime_fields(self):
        """Runtime-varying fields (timestamp, n_attempted, n_indexed)
        must NOT be in PROVENANCE_COMPARE_KEYS — they differ
        legitimately between runs."""
        for k in ("timestamp_utc", "n_attempted", "n_indexed"):
            assert k not in PROVENANCE_COMPARE_KEYS, (
                f"{k} legitimately varies between runs; must not gate comparison"
            )

    def test_compare_keys_includes_load_bearing_fields(self):
        """Hard-equality fields that MUST match for two reports to be
        meaningfully comparable."""
        for k in (
            "scorer_schema",  # ACC-012 lesson — schema bump invalidates comparison
            "dataset_sha",  # different Loc-Bench versions
            "agent_iterations",  # iter=1 vs iter=2
            "modes",  # hybrid-agent vs substring-primitives
            "eval_bin_sha", "index_bin_sha",  # binary version
        ):
            assert k in PROVENANCE_COMPARE_KEYS, (
                f"{k} is load-bearing for comparison validity"
            )


# ----------------------------------------------------------------------
# End-to-end via _check_report_invariants
# ----------------------------------------------------------------------

def _make_indexed_results(n: int = 20):
    """Helper: clean indexed results spread across categories."""
    cats = ["Bug Report", "Feature Request", "Performance Issue", "Security Vulnerability"]
    out = []
    for i in range(n):
        cat = cats[i % len(cats)]
        miss_file = (i % 5 == 0)
        miss_class = (i % 5 == 1)
        miss_func = (i % 5 == 2)
        out.append(InstanceResult(
            instance_id=f"r-{i}",
            repo="example/repo",
            category=cat,
            base_commit="abc",
            ground_truth=["fake.py:func"],
            indexed=True,
            mode_results=[ModeResult(
                mode="hybrid-agent",
                file_hit=not miss_file,
                class_hit=not miss_class,
                func_hit=not miss_func,
            )],
            repo_size_mb=10.0,
        ))
    return out


class TestEndToEnd:
    def test_no_baseline_skips_provenance_gate(self):
        """No --baseline-report set: provenance gate doesn't fire."""
        results = _make_indexed_results(20)
        violations = _check_report_invariants(
            results, ["hybrid-agent"],
        )
        prov_violations = [v for v in violations if "provenance" in v]
        assert prov_violations == []

    def test_matching_manifests_pass(self):
        results = _make_indexed_results(20)
        manifest = {
            "harness_sha": "abc123",
            "scorer_schema": "2",
            "eval_bin_sha": "111", "index_bin_sha": "222",
            "dataset_sha": "333", "agent_iterations": "2",
            "modes": "hybrid-agent", "max_mb": "1000",
        }
        violations = _check_report_invariants(
            results, ["hybrid-agent"],
            current_manifest=manifest,
            baseline_manifest=manifest,
        )
        prov_refusals = [v for v in violations if v.startswith("REFUSE:") and "provenance" in v]
        assert prov_refusals == []

    def test_mismatched_manifests_refuse(self):
        results = _make_indexed_results(20)
        cur = {"harness_sha": "abc", "scorer_schema": "2", "eval_bin_sha": "111",
               "index_bin_sha": "222", "dataset_sha": "333", "agent_iterations": "2",
               "modes": "hybrid-agent", "max_mb": "1000"}
        base = dict(cur)
        base["scorer_schema"] = "1"
        violations = _check_report_invariants(
            results, ["hybrid-agent"],
            current_manifest=cur,
            baseline_manifest=base,
        )
        prov_refusals = [v for v in violations if v.startswith("REFUSE:") and "provenance" in v]
        assert len(prov_refusals) == 1
