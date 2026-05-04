"""Scorer fixtures for `eval_locbench_compare.py::score_entities`.

Family A pre-publication assertions for the measurement-discipline
pay-down. Each test corresponds to a known incident or edge case the
scorer must handle without manual review:

- Incident 4 (2026-05-04, ACC-012): class_hit forced False for
  module-level GTs (no Class. prefix). 34% of n=200 instances were
  affected; the scorer's else branch only set func_hit, leaving
  class_hit at its initial False.
- Path normalization edge cases (backslash conversion, trailing slashes)
- Project-prefix containment (code-graph QNs have a project prefix
  before the module path)
- Mixed GT shapes (some entries have classes, some don't)

These tests would have failed before publication on any of the above
conditions. They are the "Family A scorer fixture" leg of the three-leg
stool from the 2026-05-04 incident-backport experiment
(`~/Documents/knowledge-base/research/2026-05-04-incident-backport-experiment.md`).

Run via: `python -m pytest bench/research/test_score_entities.py -v`
"""
from __future__ import annotations

import sys
from pathlib import Path

# Make eval_locbench_compare importable when run directly
sys.path.insert(0, str(Path(__file__).parent))

import pytest  # noqa: E402

from eval_locbench_compare import score_entities, normalize_path  # noqa: E402


# ----------------------------------------------------------------------
# normalize_path
# ----------------------------------------------------------------------

class TestNormalizePath:
    def test_backslash_to_forward(self):
        assert normalize_path("a\\b\\c.py") == "a/b/c.py"

    def test_trailing_slash_stripped(self):
        assert normalize_path("a/b/") == "a/b"

    def test_leading_slash_stripped(self):
        assert normalize_path("/a/b") == "a/b"

    def test_already_normalized(self):
        assert normalize_path("a/b/c.py") == "a/b/c.py"

    def test_empty(self):
        assert normalize_path("") == ""


# ----------------------------------------------------------------------
# score_entities — module-level GT (no class component)
# ----------------------------------------------------------------------

class TestModuleLevelGT:
    """ACC-012 regression — class_hit must be True for module-level GTs
    when file matches. The class-equivalent enclosing scope IS the
    module/file. Before the fix shipped 2026-05-04, the scorer's else
    branch only set func_hit, leaving 34% of Loc-Bench Python instances
    forced to class=N regardless of agent output."""

    def test_module_level_full_match_all_hits(self):
        """Module-level GT, file + func match: all three hits True."""
        entities = [
            {"qualified_name": "proj.module.func", "file_path": "proj/module.py"},
        ]
        gt = ["proj/module.py:func"]
        f, c, fn = score_entities(entities, gt)
        assert (f, c, fn) == (True, True, True)

    def test_module_level_file_only_match(self):
        """Module-level GT, file matches but func name wrong:
        file_hit and class_hit (=scope_hit) True, func_hit False."""
        entities = [
            {"qualified_name": "proj.module.other_func", "file_path": "proj/module.py"},
        ]
        gt = ["proj/module.py:func"]
        f, c, fn = score_entities(entities, gt)
        assert (f, c, fn) == (True, True, False), (
            "ACC-012: when GT has no class component and file matches, "
            "class_hit must equal file_hit (enclosing scope = module)"
        )

    def test_module_level_no_file_match(self):
        """Module-level GT, no file match at all: all hits False."""
        entities = [
            {"qualified_name": "wrong.path.func", "file_path": "wrong/path.py"},
        ]
        gt = ["proj/module.py:func"]
        f, c, fn = score_entities(entities, gt)
        assert (f, c, fn) == (False, False, False)


# ----------------------------------------------------------------------
# score_entities — class-method GT
# ----------------------------------------------------------------------

class TestClassMethodGT:
    def test_class_method_full_match(self):
        """Class.method GT, full match: all three hits True."""
        entities = [
            {"qualified_name": "proj.module.MyClass.my_method", "file_path": "proj/module.py"},
        ]
        gt = ["proj/module.py:MyClass.my_method"]
        f, c, fn = score_entities(entities, gt)
        assert (f, c, fn) == (True, True, True)

    def test_class_method_class_hit_only(self):
        """Class.method GT, predicted class but wrong method:
        file_hit, class_hit True, func_hit False."""
        entities = [
            {"qualified_name": "proj.module.MyClass.other_method", "file_path": "proj/module.py"},
        ]
        gt = ["proj/module.py:MyClass.my_method"]
        f, c, fn = score_entities(entities, gt)
        assert (f, c, fn) == (True, True, False)

    def test_class_method_file_only(self):
        """Class.method GT, predicted file but wrong class:
        file_hit True, class_hit False, func_hit False."""
        entities = [
            {"qualified_name": "proj.module.OtherClass.something", "file_path": "proj/module.py"},
        ]
        gt = ["proj/module.py:MyClass.my_method"]
        f, c, fn = score_entities(entities, gt)
        assert (f, c, fn) == (True, False, False)


# ----------------------------------------------------------------------
# score_entities — case insensitivity (current scorer uses .lower())
# ----------------------------------------------------------------------

class TestCaseInsensitive:
    def test_case_insensitive_class_match(self):
        """Class names match case-insensitively."""
        entities = [
            {"qualified_name": "proj.module.myclass.my_method", "file_path": "proj/module.py"},
        ]
        gt = ["proj/module.py:MyClass.my_method"]
        f, c, fn = score_entities(entities, gt)
        assert (f, c, fn) == (True, True, True)


# ----------------------------------------------------------------------
# score_entities — project-prefix containment
# ----------------------------------------------------------------------

class TestProjectPrefix:
    """Code-graph QNs include a project prefix
    (e.g. 'c-tmp-locbench-batch-X.backend.foo.Bar.baz').
    The scorer matches against the GT tail."""

    def test_project_prefixed_class_match(self):
        entities = [
            {"qualified_name": "c-tmp-locbench-X.proj.module.MyClass.my_method",
             "file_path": "proj/module.py"},
        ]
        gt = ["proj/module.py:MyClass.my_method"]
        f, c, fn = score_entities(entities, gt)
        assert (f, c, fn) == (True, True, True)

    def test_project_prefixed_module_func(self):
        entities = [
            {"qualified_name": "c-tmp-locbench-X.proj.module.helper",
             "file_path": "proj/module.py"},
        ]
        gt = ["proj/module.py:helper"]
        f, c, fn = score_entities(entities, gt)
        assert (f, c, fn) == (True, True, True)


# ----------------------------------------------------------------------
# score_entities — multiple GT entries (mixed shapes)
# ----------------------------------------------------------------------

class TestMixedGT:
    def test_mixed_gt_class_method_and_module_func(self):
        """Mixed GT: one class-method, one module-level. Hit if any
        entity matches either; module-level part forces class_hit
        once file matches even if no class entity is predicted."""
        entities = [
            # Predicts a module-level function (matches the second GT)
            {"qualified_name": "proj.module.helper", "file_path": "proj/module.py"},
        ]
        gt = [
            "proj/module.py:MyClass.my_method",  # class-method
            "proj/module.py:helper",  # module-level
        ]
        f, c, fn = score_entities(entities, gt)
        # File match on both GT entries
        assert f is True
        # class_hit: True via the module-level GT path (helper matches)
        assert c is True
        # func_hit: helper matches the module-level GT
        assert fn is True

    def test_mixed_gt_only_class_predicted(self):
        """Mixed GT, agent predicts class method but not module func:
        all three hit (the class-method GT is fully hit)."""
        entities = [
            {"qualified_name": "proj.module.MyClass.my_method", "file_path": "proj/module.py"},
        ]
        gt = [
            "proj/module.py:MyClass.my_method",
            "proj/module.py:helper",
        ]
        f, c, fn = score_entities(entities, gt)
        assert (f, c, fn) == (True, True, True)


# ----------------------------------------------------------------------
# score_entities — degenerate inputs
# ----------------------------------------------------------------------

class TestDegenerate:
    def test_empty_entities(self):
        f, c, fn = score_entities([], ["proj/module.py:func"])
        assert (f, c, fn) == (False, False, False)

    def test_empty_gt(self):
        entities = [{"qualified_name": "proj.module.func", "file_path": "proj/module.py"}]
        f, c, fn = score_entities(entities, [])
        assert (f, c, fn) == (False, False, False)

    def test_gt_no_colon_skipped(self):
        """GT entry without `:` is gracefully skipped, not crashed on."""
        entities = [{"qualified_name": "proj.module.func", "file_path": "proj/module.py"}]
        gt = ["proj/module.py", "proj/module.py:func"]  # first is malformed
        f, c, fn = score_entities(entities, gt)
        assert (f, c, fn) == (True, True, True), (
            "Malformed GT entries should be skipped, not block valid ones"
        )

    def test_entity_missing_qn(self):
        """Entity with no qualified_name still counts for file_hit."""
        entities = [{"file_path": "proj/module.py"}]  # no qn
        gt = ["proj/module.py:func"]
        f, c, fn = score_entities(entities, gt)
        # File matches; class_hit=True (module-level GT, scope = file);
        # func_hit=False (no qn to match against)
        assert (f, c, fn) == (True, True, False)

    def test_entity_missing_file_path(self):
        """Entity with no file_path can't file-match anything."""
        entities = [{"qualified_name": "proj.module.func"}]  # no fp
        gt = ["proj/module.py:func"]
        f, c, fn = score_entities(entities, gt)
        assert (f, c, fn) == (False, False, False)


# ----------------------------------------------------------------------
# score_entities — dotted func name (nested classes / dotted functions)
# ----------------------------------------------------------------------

class TestDottedFunc:
    def test_nested_class_method(self):
        """GT 'Outer.Inner.method' → cls='Outer', fn='Inner.method'."""
        entities = [
            {"qualified_name": "proj.module.Outer.Inner.method",
             "file_path": "proj/module.py"},
        ]
        gt = ["proj/module.py:Outer.Inner.method"]
        f, c, fn = score_entities(entities, gt)
        assert (f, c, fn) == (True, True, True)


# ----------------------------------------------------------------------
# score_entities — path normalization at match time
# ----------------------------------------------------------------------

class TestPathNormalization:
    def test_backslash_in_entity_path(self):
        """Entity file_path with backslashes is normalized for comparison."""
        entities = [
            {"qualified_name": "proj.module.func", "file_path": "proj\\module.py"},
        ]
        gt = ["proj/module.py:func"]
        f, c, fn = score_entities(entities, gt)
        assert (f, c, fn) == (True, True, True)

    def test_backslash_in_gt_path(self):
        """GT file_path with backslashes is normalized for comparison."""
        entities = [
            {"qualified_name": "proj.module.func", "file_path": "proj/module.py"},
        ]
        gt = ["proj\\module.py:func"]
        f, c, fn = score_entities(entities, gt)
        assert (f, c, fn) == (True, True, True)


# ----------------------------------------------------------------------
# score_entities — invariants that should ALWAYS hold (property tests)
# ----------------------------------------------------------------------

class TestInvariants:
    """High-level invariants the scorer must always satisfy. Caught
    incident 4 in retrospect; would have caught it at PR time."""

    def test_class_hit_implies_file_hit(self):
        """class_hit=True ⟹ file_hit=True (you can't get the class
        right without getting the file right)."""
        # Try several scenarios that would set class_hit
        scenarios = [
            (
                [{"qualified_name": "p.m.C.f", "file_path": "p/m.py"}],
                ["p/m.py:C.f"],
            ),
            (
                [{"qualified_name": "p.m.func", "file_path": "p/m.py"}],
                ["p/m.py:func"],  # module-level, class_hit=file_hit
            ),
        ]
        for entities, gt in scenarios:
            f, c, fn = score_entities(entities, gt)
            assert (not c) or f, (
                f"Invariant violated: class_hit=True but file_hit=False for "
                f"entities={entities} gt={gt}"
            )

    def test_func_hit_implies_class_hit(self):
        """func_hit=True ⟹ class_hit=True (the enclosing scope must
        be right if the function itself is right). Catches the same
        class of metric-misalignment bug as ACC-012."""
        scenarios = [
            (
                [{"qualified_name": "p.m.C.f", "file_path": "p/m.py"}],
                ["p/m.py:C.f"],
            ),
            (
                [{"qualified_name": "p.m.func", "file_path": "p/m.py"}],
                ["p/m.py:func"],
            ),
            (
                [{"qualified_name": "c-tmp.p.m.C.f", "file_path": "p/m.py"}],
                ["p/m.py:C.f"],
            ),
        ]
        for entities, gt in scenarios:
            f, c, fn = score_entities(entities, gt)
            assert (not fn) or c, (
                f"Monotonicity invariant violated: func_hit=True but "
                f"class_hit=False for entities={entities} gt={gt}. "
                f"This is the shape that surfaced as the ACC-012 instrument bug."
            )

    def test_func_hit_implies_file_hit(self):
        """func_hit=True ⟹ file_hit=True."""
        scenarios = [
            ([{"qualified_name": "p.m.C.f", "file_path": "p/m.py"}], ["p/m.py:C.f"]),
            ([{"qualified_name": "p.m.func", "file_path": "p/m.py"}], ["p/m.py:func"]),
        ]
        for entities, gt in scenarios:
            f, c, fn = score_entities(entities, gt)
            assert (not fn) or f
