"""Bootstrap the PyCG oracle environment.

PyCG 0.0.7 has a reentrant-import crash on Python 3.11+ that manifests as
`ImportManagerError: Can't add edge to a non existing node` during
`install_hooks() -> _clear_caches() -> importlib.invalidate_caches()`.
The upstream fix has not been released; we carry three local patches that
survive across Python 3.11 and 3.12+.

This module provisions an isolated, uv-managed Python 3.11 venv with PyCG
installed and patched, so the harness works out-of-the-box after a fresh
clone. Idempotent: subsequent runs verify the env exists and return the
cached interpreter path.

Layout (created on first run):
    ~/.cache/code-graph-bench/py311/               # uv-managed venv
    ~/.cache/code-graph-bench/py311/Scripts/python.exe
                                                    (Unix: bin/python)
    ~/.cache/code-graph-bench/py311/.patches_applied
                                                    (marker file)
"""
from __future__ import annotations

import os
import shutil
import subprocess
import sys
from pathlib import Path

VENV_DIR = Path.home() / ".cache" / "code-graph-bench" / "py311"
PATCH_MARKER = VENV_DIR / ".pycg_patches_applied"

# Package pins — frozen so reruns produce identical results and any
# PyCG upgrade is an explicit commit, not a silent drift.
SETUPTOOLS_PIN = "setuptools<81"   # pre-81 keeps pkg_resources importable
PYCG_PIN = "pycg==0.0.7"            # last released version


def _venv_python(venv: Path) -> Path:
    """Return the Python executable inside a venv for the current platform."""
    if os.name == "nt":
        return venv / "Scripts" / "python.exe"
    return venv / "bin" / "python"


def _ensure_uv_available() -> None:
    if shutil.which("uv") is None:
        raise SystemExit(
            "`uv` not found on PATH. Install from https://github.com/astral-sh/uv "
            "and retry. uv provisions the isolated Python 3.11 venv that PyCG requires."
        )


def _create_venv() -> None:
    """Create the uv-managed Python 3.11 venv if it doesn't exist."""
    if VENV_DIR.exists() and _venv_python(VENV_DIR).exists():
        return
    _ensure_uv_available()
    VENV_DIR.parent.mkdir(parents=True, exist_ok=True)
    print(f"[env] creating Python 3.11 venv at {VENV_DIR}")
    # uv resolves python 3.11 from its own managed install if available;
    # otherwise downloads it. Either way, the venv is isolated from system
    # Python.
    subprocess.run(
        ["uv", "python", "install", "3.11"],
        check=True,
        capture_output=True,
    )
    subprocess.run(
        ["uv", "venv", "--python", "3.11", str(VENV_DIR)],
        check=True,
        capture_output=True,
    )


def _install_packages() -> None:
    """Install PyCG + a compatible setuptools (cached once installed)."""
    vpy = _venv_python(VENV_DIR)
    # Check if pycg is already importable.
    probe = subprocess.run(
        [str(vpy), "-c", "import pycg, pkg_resources"],
        capture_output=True,
    )
    if probe.returncode == 0:
        return
    print("[env] installing setuptools<81 + pycg==0.0.7 into bench venv")
    subprocess.run(
        ["uv", "pip", "install", "--python", str(vpy), SETUPTOOLS_PIN, PYCG_PIN],
        check=True,
        capture_output=True,
    )


# ---------------------------------------------------------------------------
# PyCG local patches.
#
# These survive as inline Python string edits rather than a .patch file so
# we don't need the `patch` binary on Windows. Each patch is idempotent —
# looks for a marker unique to the patched state before touching anything.
#
# Patch rationale:
#   #1 `create_edge` auto-creates missing nodes instead of raising, because
#      modern Python's lazy importlib.metadata loading triggers PyCG's own
#      finder before its graph is populated.
#   #2 `create_edge` skips silently when no current module is set, because
#      the imports triggered by invalidate_caches() are Python stdlib
#      internals, not user code.
#   #3 `_clear_caches` skips `importlib.invalidate_caches()` entirely,
#      because the reentrant lazy-import path is unavoidable otherwise.
#      Safe for one-shot CLI invocations (no stale caches to clear).
# ---------------------------------------------------------------------------

PATCH_MARKER_STRING = "# Local patch (2026-04-23)"

PATCH_ONE_OLD = (
    "    def create_edge(self, dest):\n"
    "        if not dest or not isinstance(dest, str):\n"
    "            raise ImportManagerError(\"Invalid node name\")\n"
    "\n"
    "        node = self.get_node(self._get_module_path())\n"
    "        if not node:\n"
    "            raise ImportManagerError(\"Can't add edge to a non existing node\")\n"
    "\n"
    "        node[\"imports\"].add(dest)\n"
)

PATCH_ONE_NEW = (
    "    def create_edge(self, dest):\n"
    "        if not dest or not isinstance(dest, str):\n"
    "            raise ImportManagerError(\"Invalid node name\")\n"
    "\n"
    "        # Local patch (2026-04-23): skip silently when no current module is\n"
    "        # set, and auto-create when the caller module is missing. Modern\n"
    "        # Python's lazy importlib.metadata loading triggers this finder\n"
    "        # during invalidate_caches() before PyCG has set a current\n"
    "        # module, causing an unavoidable ImportManagerError on 3.11+.\n"
    "        current = self._get_module_path()\n"
    "        if not current:\n"
    "            return\n"
    "\n"
    "        node = self.get_node(current)\n"
    "        if not node:\n"
    "            node = self.create_node(current)\n"
    "\n"
    "        node[\"imports\"].add(dest)\n"
)

PATCH_TWO_OLD = (
    "    def _clear_caches(self):\n"
    "        importlib.invalidate_caches()\n"
    "        sys.path_importer_cache.clear()\n"
)

PATCH_TWO_NEW = (
    "    def _clear_caches(self):\n"
    "        # Local patch (2026-04-23): skip importlib.invalidate_caches().\n"
    "        # On 3.11+, invalidate_caches() lazy-loads importlib.metadata\n"
    "        # which triggers PyCG's own finder reentrantly before user code\n"
    "        # is analyzed. Since _clear_caches is only called at the start\n"
    "        # of a fresh pycg process (no stale caches anyway), skipping is\n"
    "        # safe for one-shot CLI invocations.\n"
    "        sys.path_importer_cache.clear()\n"
)


def _apply_patches() -> None:
    """Apply PyCG patches idempotently. No-op if already patched."""
    if PATCH_MARKER.exists():
        return

    vpy = _venv_python(VENV_DIR)
    result = subprocess.run(
        [
            str(vpy),
            "-c",
            "import pycg.machinery.imports as m; print(m.__file__)",
        ],
        capture_output=True,
        check=True,
    )
    imports_py = Path(result.stdout.decode("utf-8", errors="replace").strip())
    if not imports_py.exists():
        raise SystemExit(f"[env] cannot locate pycg imports.py at {imports_py}")

    original = imports_py.read_bytes().decode("utf-8")
    if PATCH_MARKER_STRING in original:
        # Already patched in a prior session — record the marker and move on.
        PATCH_MARKER.write_text(
            f"pycg imports.py already carries marker {PATCH_MARKER_STRING!r}\n",
            encoding="utf-8",
        )
        return

    patched = original
    for old, new, label in (
        (PATCH_ONE_OLD, PATCH_ONE_NEW, "patch #1 (create_edge resilience)"),
        (PATCH_TWO_OLD, PATCH_TWO_NEW, "patch #2 (skip invalidate_caches)"),
    ):
        if old not in patched:
            raise SystemExit(
                f"[env] {label}: expected old block not found in {imports_py}. "
                f"PyCG upstream may have changed; refresh patches."
            )
        patched = patched.replace(old, new)

    imports_py.write_bytes(patched.encode("utf-8"))
    PATCH_MARKER.write_text(
        "PyCG imports.py patched for modern Python reentrant-import compat.\n"
        f"Target: {imports_py}\n",
        encoding="utf-8",
    )
    print(f"[env] applied PyCG patches to {imports_py}")


def ensure_bench_venv() -> Path:
    """Provision the isolated PyCG bench environment. Returns the interpreter path.

    Idempotent: on second and later calls, all steps verify state and return
    immediately if already provisioned.
    """
    _create_venv()
    _install_packages()
    _apply_patches()
    vpy = _venv_python(VENV_DIR)
    if not vpy.exists():
        raise SystemExit(f"[env] bench python missing after provisioning: {vpy}")
    return vpy


def main() -> int:
    """CLI entry point. Useful for pre-provisioning in CI or before running oracles."""
    python_path = ensure_bench_venv()
    print(f"[env] ready: {python_path}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
