"""Unit tests for project_name_from_path (Python mirror of Go's
ProjectNameFromPath). Drift between the two implementations would
make CORPUS_<TARGET>_PATH overrides produce wrong project_ids and
break the agent-effectiveness battery on CI.

Run: python3 bench/research/agent-effectiveness/test_project_id_helper.py
"""
from project_id import project_name_from_path


def test_basic_posix_path():
    assert project_name_from_path("/home/runner/fixture/ripgrep") == "home-runner-fixture-ripgrep"


def test_root_path():
    assert project_name_from_path("/") == "root"


def test_empty_string():
    assert project_name_from_path("") == "root"


def test_windows_path_drive_letter_lowercased():
    assert project_name_from_path("C:/Users/user/code/foo") == "c-Users-user-code-foo"


def test_backslashes_normalized():
    assert project_name_from_path("C:\\Users\\foo") == "c-Users-foo"


def test_consecutive_slashes_collapsed():
    assert project_name_from_path("//home///foo") == "home-foo"


def test_ci_ripgrep_actual_path():
    # The exact path the agent-effectiveness workflow uses.
    # workflow: index_repository "{\"path\":\"$HOME/fixture/ripgrep\"}"
    # ubuntu runners: $HOME = /home/runner.
    got = project_name_from_path("/home/runner/fixture/ripgrep")
    assert got == "home-runner-fixture-ripgrep", f"got {got}"


def test_corpus_psm_path():
    # Sanity check the corpus.json hard-coded PSM path still derives
    # the project_id stored in corpus.json. If this fails, the corpus
    # file is stale wrt the Go derivation rules and developer-local
    # runs will be broken.
    expected = "c-Users-user-Documents-GitHub-psm"
    got = project_name_from_path("C:/Users/user/Documents/GitHub/psm")
    assert got == expected, f"expected {expected}, got {got}"


if __name__ == "__main__":
    import sys
    funcs = [v for k, v in list(globals().items()) if k.startswith("test_") and callable(v)]
    failed = 0
    for fn in funcs:
        try:
            fn()
            print(f"PASS  {fn.__name__}")
        except AssertionError as e:
            print(f"FAIL  {fn.__name__}: {e}")
            failed += 1
    sys.exit(1 if failed else 0)
