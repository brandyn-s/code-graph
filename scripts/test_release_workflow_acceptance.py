from __future__ import annotations

import json
import os
import re
import shutil
import subprocess
import tempfile
import textwrap
import unittest
from pathlib import Path

REPOSITORY_ROOT = Path(__file__).resolve().parents[1]
HELPER = REPOSITORY_ROOT / "scripts" / "release_workflow.sh"
RELEASE_VERSION = REPOSITORY_ROOT / "scripts" / "release_version.py"

VERSION = "v0.9.0"
LATEST_VERSION = "v0.8.0-legacy.11"
GITHUB_SHA = "a" * 40
OTHER_SHA = "b" * 40
GITHUB_REPOSITORY = "brandyn-s/code-graph"

EXPECTED_ASSETS = (
    "code-graph-linux-amd64.tar.gz",
    "code-graph-linux-arm64.tar.gz",
    "code-graph-darwin-amd64.tar.gz",
    "code-graph-darwin-arm64.tar.gz",
    "code-graph-windows-amd64.zip",
    "checksums.txt",
)


FAKE_GH = r"""#!/usr/bin/env python3
from __future__ import annotations

import atexit
import json
import os
from pathlib import Path
import sys


state_path = Path(os.environ["FAKE_RELEASE_STATE"])
state = json.loads(state_path.read_text(encoding="utf-8"))
argv = sys.argv[1:]
state["operations"].append({"tool": "gh", "argv": argv})


def save() -> None:
    state_path.write_text(
        json.dumps(state, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )


atexit.register(save)


def reject(message: str, code: int = 97) -> None:
    print(f"fake gh: {message}", file=sys.stderr)
    raise SystemExit(code)


if not os.environ.get("GH_TOKEN"):
    reject("GH_TOKEN is required", code=4)


def option_value(name: str) -> str | None:
    for index, value in enumerate(argv):
        if value == name:
            if index + 1 >= len(argv):
                reject(f"{name} is missing its value")
            return argv[index + 1]
        if value.startswith(name + "="):
            return value.split("=", 1)[1]
    return None


def release_for(tag: str) -> dict:
    matches = [
        release
        for release in state["releases"]
        if release["tag_name"] == tag
    ]
    if len(matches) != 1:
        reject(f"expected exactly one release for {tag}, found {len(matches)}")
    return matches[0]


def tag_after(command: str) -> str | None:
    start = argv.index(command) + 1
    index = start
    while index < len(argv):
        value = argv[index]
        if value in {"--repo", "--json", "--jq", "--notes"}:
            index += 2
            continue
        if value.startswith("--"):
            index += 1
            continue
        return value
    return None


def asset_names(release: dict) -> list[str]:
    return sorted(release.get("assets", {}))


dangerous = (
    argv[:2] == ["release", "delete"]
    or option_value("--method") in {"DELETE", "PATCH", "PUT"}
    or option_value("-X") in {"DELETE", "PATCH", "PUT"}
)
if dangerous:
    state["dangerous_operations"].append(
        {"tool": "gh", "argv": argv, "reason": "destructive operation"}
    )
    reject("destructive operations are forbidden")

if argv[:2] == ["release", "view"]:
    tag = tag_after("view")
    json_fields = option_value("--json") or ""
    jq = option_value("--jq")
    if tag is None:
        if json_fields != "tagName" or jq != ".tagName":
            reject("unsupported latest-release query")
        print(state["latest_version"])
        raise SystemExit(0)

    release = release_for(tag)
    if "assets" in json_fields:
        if jq is not None:
            if "name" not in jq:
                reject("unsupported release-assets jq expression")
            print("\n".join(asset_names(release)))
        else:
            print(
                json.dumps(
                    {
                        "assets": [
                            {"name": name}
                            for name in asset_names(release)
                        ]
                    }
                )
            )
        raise SystemExit(0)
    if "isDraft" in json_fields:
        if jq not in {None, ".isDraft"}:
            reject("unsupported isDraft jq expression")
        value = bool(release["draft"])
        print(
            ("true" if value else "false")
            if jq is not None
            else json.dumps({"isDraft": value})
        )
        raise SystemExit(0)
    reject("unsupported release view")

if argv and argv[0] == "api":
    endpoint = next(
        (
            value
            for index, value in enumerate(argv[1:], start=1)
            if not value.startswith("-")
            and argv[index - 1] not in {"--jq", "--method", "-X"}
        ),
        None,
    )
    if endpoint is None:
        reject("missing API endpoint")
    version = os.environ["VERSION"]
    if "/git/matching-refs/tags/" in endpoint or "/git/ref/tags/" in endpoint:
        if state.get("fail_api") == "matching-refs":
            reject("injected matching-ref API failure", code=74)
        sha = state["tags"].get(version, "")
        jq = option_value("--jq")
        if jq is not None:
            if sha:
                print(f"refs/tags/{version}\t{sha}")
        elif sha:
            print(
                json.dumps(
                    {
                        "ref": f"refs/tags/{version}",
                        "object": {"sha": sha},
                    }
                )
            )
        raise SystemExit(0)
    if "/releases/tags/" in endpoint:
        release = release_for(version)
        jq = option_value("--jq")
        if jq is not None and "name" in jq:
            print("\n".join(asset_names(release)))
        else:
            print(
                json.dumps(
                    {
                        "draft": release["draft"],
                        "assets": [
                            {"name": name}
                            for name in asset_names(release)
                        ],
                    }
                )
            )
        raise SystemExit(0)
    if "/releases?per_page=" in endpoint:
        if state.get("fail_api") == "releases":
            reject("injected releases API failure", code=74)
        records = [
            (
                f"{release['tag_name']}\t"
                f"{'draft' if release['draft'] else 'published'}"
            )
            for release in state["releases"]
        ]
        print("\n".join(records))
        raise SystemExit(0)
    reject(f"unsupported API endpoint: {endpoint}")

if argv[:2] == ["release", "create"]:
    tag = tag_after("create")
    if tag is None:
        reject("release create requires a tag")
    if "--draft" not in argv:
        reject("release must be created as a draft")
    if state["tags"].get(tag) != os.environ.get("GITHUB_SHA"):
        reject("release tag is absent or does not match GITHUB_SHA")
    if any(release["tag_name"] == tag for release in state["releases"]):
        reject("release already exists")
    state["releases"].append(
        {"tag_name": tag, "draft": True, "assets": {}}
    )
    raise SystemExit(0)

if argv[:2] == ["release", "upload"]:
    tag = tag_after("upload")
    if tag is None:
        reject("release upload requires a tag")
    if "--clobber" not in argv:
        reject("release upload must use --clobber")
    release = release_for(tag)
    if not release["draft"]:
        reject("assets cannot be uploaded to a published release")

    paths: list[Path] = []
    index = 3
    while index < len(argv):
        value = argv[index]
        if value == "--repo":
            index += 2
            continue
        if value == "--clobber":
            index += 1
            continue
        if value.startswith("-"):
            reject(f"unsupported upload option: {value}")
        paths.append(Path(value))
        index += 1

    if not paths:
        reject("release upload requires assets")
    uploaded_names = [path.name for path in paths]
    if len(uploaded_names) != len(set(uploaded_names)):
        reject("duplicate asset names in upload")
    if set(uploaded_names) != set(state["expected_assets"]):
        reject(
            "upload inventory must be exactly the expected release assets"
        )

    fail_after = state.get("fail_upload_after")
    should_fail = (
        isinstance(fail_after, int)
        and not state.get("upload_failure_used", False)
    )
    upload_count = fail_after if should_fail else len(paths)
    for path in paths[:upload_count]:
        if not path.is_file():
            reject(f"asset does not exist: {path}")
        release["assets"][path.name] = path.read_bytes().hex()
    if should_fail:
        state["upload_failure_used"] = True
        reject("injected transient upload failure", code=75)

    for path in paths[upload_count:]:
        if not path.is_file():
            reject(f"asset does not exist: {path}")
        release["assets"][path.name] = path.read_bytes().hex()
    if state.get("inject_extra_after_upload"):
        release["assets"]["unexpected-concurrent-asset"] = "00"
    raise SystemExit(0)

if argv[:2] == ["release", "edit"]:
    tag = tag_after("edit")
    if tag is None:
        reject("release edit requires a tag")
    draft_value = option_value("--draft")
    if draft_value != "false":
        reject("only draft publication is supported")
    release = release_for(tag)
    if not release["draft"]:
        reject("release is already published")
    release["draft"] = False
    raise SystemExit(0)

reject("unsupported command: " + " ".join(argv))
"""


FAKE_GIT = r"""#!/usr/bin/env python3
from __future__ import annotations

import atexit
import json
import os
from pathlib import Path
import sys


state_path = Path(os.environ["FAKE_RELEASE_STATE"])
state = json.loads(state_path.read_text(encoding="utf-8"))
argv = sys.argv[1:]
state["operations"].append({"tool": "git", "argv": argv})


def save() -> None:
    state_path.write_text(
        json.dumps(state, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )


atexit.register(save)


def reject(message: str, code: int = 96) -> None:
    print(f"fake git: {message}", file=sys.stderr)
    raise SystemExit(code)


dangerous = (
    (argv and argv[0] in {"reset", "delete"})
    or "-f" in argv
    or "--force" in argv
    or "--delete" in argv
    or (
        argv
        and argv[0] == "push"
        and any(value.startswith(":refs/") for value in argv)
    )
)
if dangerous:
    state["dangerous_operations"].append(
        {"tool": "git", "argv": argv, "reason": "destructive operation"}
    )
    reject("destructive operations are forbidden")

if argv == ["rev-parse", "HEAD"]:
    print(state["head_sha"])
    raise SystemExit(0)

if len(argv) == 3 and argv[0] == "tag":
    tag, sha = argv[1:]
    existing = state["local_tags"].get(tag)
    if existing is not None:
        reject(f"local tag already exists: {tag}")
    if sha != os.environ.get("GITHUB_SHA"):
        reject("tag target must equal GITHUB_SHA")
    state["local_tags"][tag] = sha
    raise SystemExit(0)

if len(argv) == 3 and argv[:2] == ["push", "origin"]:
    refspec = argv[2]
    if ":" not in refspec:
        reject("push requires an explicit immutable refspec")
    source, target = refspec.split(":", 1)
    prefix = "refs/tags/"
    if not source.startswith(prefix) or not target.startswith(prefix):
        reject("only tag-to-same-tag pushes are supported")
    source_tag = source.removeprefix(prefix)
    target_tag = target.removeprefix(prefix)
    if source_tag != target_tag:
        reject("tag push may not repoint a different ref")
    sha = state["local_tags"].get(source_tag)
    if sha is None:
        reject("local tag is missing")
    existing = state["tags"].get(target_tag)
    if existing is not None and existing != sha:
        state["dangerous_operations"].append(
            {
                "tool": "git",
                "argv": argv,
                "reason": "attempted tag repoint",
            }
        )
        reject("remote tag already points elsewhere")
    state["tags"][target_tag] = sha
    raise SystemExit(0)

reject("unsupported command: " + " ".join(argv))
"""


class ReleaseWorkflowAcceptanceTests(unittest.TestCase):
    maxDiff = None

    def setUp(self) -> None:
        self.temporary_directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary_directory.cleanup)
        self.workspace = Path(self.temporary_directory.name) / "workspace"
        self.scripts = self.workspace / "scripts"
        self.fake_bin = self.workspace / "fake-bin"
        self.scripts.mkdir(parents=True)
        self.fake_bin.mkdir()

        shutil.copy2(HELPER, self.scripts / HELPER.name)
        shutil.copy2(RELEASE_VERSION, self.scripts / RELEASE_VERSION.name)
        self.helper = self.scripts / HELPER.name

        self._write_executable(self.fake_bin / "gh", FAKE_GH)
        self._write_executable(self.fake_bin / "git", FAKE_GIT)

        self.asset_contents = {
            name: f"current payload for {name}\n".encode() for name in EXPECTED_ASSETS
        }
        for name, contents in self.asset_contents.items():
            (self.workspace / name).write_bytes(contents)

        self.state_path = self.workspace / "release-state.json"
        self.output_path = self.workspace / "github-output.txt"
        self.reset_state()

        self.environment = os.environ.copy()
        self.environment.update(
            {
                "DEFAULT_BRANCH": "main",
                "FAKE_RELEASE_STATE": str(self.state_path),
                "GH_TOKEN": "test-token",
                "GITHUB_OUTPUT": str(self.output_path),
                "GITHUB_REF": "refs/heads/main",
                "GITHUB_REPOSITORY": GITHUB_REPOSITORY,
                "GITHUB_SHA": GITHUB_SHA,
                "PATH": (f"{self.fake_bin}{os.pathsep}{self.environment['PATH']}"),
                "RELEASE_NOTES": "Acceptance-test release",
                "RELEASE_STATE": "absent",
                "TAG_EXISTS": "false",
                "VERSION": VERSION,
            }
        )

    @staticmethod
    def _write_executable(path: Path, contents: str) -> None:
        path.write_text(
            textwrap.dedent(contents),
            encoding="utf-8",
        )
        path.chmod(0o755)

    def reset_state(
        self,
        *,
        tags: dict[str, str] | None = None,
        releases: list[dict] | None = None,
        head_sha: str = GITHUB_SHA,
        fail_upload_after: int | None = None,
        fail_api: str | None = None,
        inject_extra_after_upload: bool = False,
    ) -> None:
        state = {
            "dangerous_operations": [],
            "expected_assets": list(EXPECTED_ASSETS),
            "head_sha": head_sha,
            "latest_version": LATEST_VERSION,
            "local_tags": {},
            "operations": [],
            "releases": releases or [],
            "tags": tags or {},
        }
        if fail_upload_after is not None:
            state["fail_upload_after"] = fail_upload_after
            state["upload_failure_used"] = False
        if fail_api is not None:
            state["fail_api"] = fail_api
        if inject_extra_after_upload:
            state["inject_extra_after_upload"] = True
        self.state_path.write_text(
            json.dumps(state, indent=2, sort_keys=True) + "\n",
            encoding="utf-8",
        )
        self.output_path.unlink(missing_ok=True)

    def state(self) -> dict:
        return json.loads(self.state_path.read_text(encoding="utf-8"))

    def run_helper(
        self,
        command: str,
        *,
        helper: Path | None = None,
        **environment_overrides: str | None,
    ) -> subprocess.CompletedProcess[str]:
        environment = self.environment.copy()
        for key, value in environment_overrides.items():
            if value is None:
                environment.pop(key, None)
            else:
                environment[key] = value
        return subprocess.run(
            ["bash", str(helper or self.helper), command],
            cwd=self.workspace,
            env=environment,
            check=False,
            capture_output=True,
            text=True,
        )

    def assert_success(
        self,
        result: subprocess.CompletedProcess[str],
    ) -> None:
        self.assertEqual(
            result.returncode,
            0,
            msg=(f"command failed\nstdout:\n{result.stdout}\nstderr:\n{result.stderr}"),
        )

    def assert_rejected(
        self,
        result: subprocess.CompletedProcess[str],
    ) -> None:
        self.assertNotEqual(
            result.returncode,
            0,
            msg=(
                "command unexpectedly succeeded"
                f"\nstdout:\n{result.stdout}\nstderr:\n{result.stderr}"
            ),
        )

    def output_values(self) -> dict[str, str]:
        return dict(
            line.split("=", 1)
            for line in self.output_path.read_text(encoding="utf-8").splitlines()
        )

    def expected_remote_assets(self) -> dict[str, str]:
        return {name: contents.hex() for name, contents in self.asset_contents.items()}

    def mutating_operations(self) -> list[dict]:
        mutations: list[dict] = []
        for operation in self.state()["operations"]:
            argv = operation["argv"]
            if (
                operation["tool"] == "git"
                and argv[:1] in (["tag"], ["push"])
                or (
                    operation["tool"] == "gh"
                    and argv[:2]
                    in (
                        ["release", "create"],
                        ["release", "upload"],
                        ["release", "edit"],
                        ["release", "delete"],
                    )
                )
                or (
                    operation["tool"] == "gh"
                    and argv[:1] == ["api"]
                    and any(
                        value in {"DELETE", "PATCH", "POST", "PUT"} for value in argv
                    )
                )
            ):
                mutations.append(operation)
        return mutations

    def assert_no_dangerous_operations(self) -> None:
        state = self.state()
        self.assertEqual(state["dangerous_operations"], [])
        for operation in state["operations"]:
            joined = " ".join(operation["argv"])
            with self.subTest(operation=operation):
                self.assertNotIn("release delete", joined)
                self.assertNotRegex(joined, r"(^| )tag +-f( |$)")
                self.assertNotRegex(joined, r"(^| )push +(--force|-f)( |$)")
                self.assertNotRegex(joined, r":refs/tags/[^ ]+ +--delete")

    def release(self) -> dict:
        matching = [
            release
            for release in self.state()["releases"]
            if release["tag_name"] == VERSION
        ]
        self.assertEqual(len(matching), 1)
        return matching[0]

    def test_fresh_release_succeeds_with_exact_tag_and_asset_inventory(
        self,
    ) -> None:
        validation = self.run_helper("validate")
        self.assert_success(validation)
        self.assertEqual(
            self.output_values(),
            {"tag_exists": "false", "release_state": "absent"},
        )

        tagging = self.run_helper("tag", TAG_EXISTS="false")
        self.assert_success(tagging)
        self.assertEqual(self.state()["tags"], {VERSION: GITHUB_SHA})

        publication = self.run_helper(
            "publish",
            RELEASE_STATE="absent",
        )
        self.assert_success(publication)

        release = self.release()
        self.assertFalse(release["draft"])
        self.assertEqual(release["assets"], self.expected_remote_assets())
        self.assertEqual(self.state()["tags"], {VERSION: GITHUB_SHA})
        self.assert_no_dangerous_operations()

    def test_exact_existing_tag_and_draft_are_resumed_without_retagging(
        self,
    ) -> None:
        self.reset_state(
            tags={VERSION: GITHUB_SHA},
            releases=[
                {"tag_name": VERSION, "draft": True, "assets": {}},
            ],
        )

        validation = self.run_helper("validate")
        self.assert_success(validation)
        self.assertEqual(
            self.output_values(),
            {"tag_exists": "true", "release_state": "draft"},
        )

        tagging = self.run_helper("tag", TAG_EXISTS="true")
        self.assert_success(tagging)
        publication = self.run_helper(
            "publish",
            RELEASE_STATE="draft",
        )
        self.assert_success(publication)

        tag_mutations = [
            operation
            for operation in self.mutating_operations()
            if operation["tool"] == "git"
        ]
        self.assertEqual(tag_mutations, [])
        self.assertEqual(self.state()["tags"], {VERSION: GITHUB_SHA})
        self.assertFalse(self.release()["draft"])
        self.assertEqual(
            self.release()["assets"],
            self.expected_remote_assets(),
        )
        self.assert_no_dangerous_operations()

    def test_mismatched_existing_tag_is_rejected_without_mutation(self) -> None:
        self.reset_state(tags={VERSION: OTHER_SHA})

        result = self.run_helper("validate")

        self.assert_rejected(result)
        self.assertEqual(self.mutating_operations(), [])
        self.assertEqual(self.state()["tags"], {VERSION: OTHER_SHA})
        self.assert_no_dangerous_operations()

    def test_wrong_dispatch_branch_is_rejected_without_mutation(self) -> None:
        result = self.run_helper(
            "validate",
            GITHUB_REF="refs/heads/not-main",
        )

        self.assert_rejected(result)
        self.assertEqual(self.mutating_operations(), [])
        self.assert_no_dangerous_operations()

    def test_checkout_sha_mismatch_is_rejected_without_mutation(self) -> None:
        self.reset_state(head_sha=OTHER_SHA)

        result = self.run_helper("validate")

        self.assert_rejected(result)
        self.assertEqual(self.mutating_operations(), [])
        self.assertEqual(self.state()["tags"], {})
        self.assert_no_dangerous_operations()

    def test_matching_ref_api_failure_is_rejected_without_mutation(self) -> None:
        self.reset_state(fail_api="matching-refs")

        result = self.run_helper("validate")

        self.assert_rejected(result)
        self.assertEqual(self.mutating_operations(), [])
        self.assertEqual(self.state()["tags"], {})
        self.assert_no_dangerous_operations()

    def test_release_list_api_failure_is_rejected_without_mutation(self) -> None:
        self.reset_state(fail_api="releases")

        result = self.run_helper("validate")

        self.assert_rejected(result)
        self.assertEqual(self.mutating_operations(), [])
        self.assertEqual(self.state()["releases"], [])
        self.assert_no_dangerous_operations()

    def test_published_release_is_rejected_without_mutation(self) -> None:
        self.reset_state(
            tags={VERSION: GITHUB_SHA},
            releases=[
                {"tag_name": VERSION, "draft": False, "assets": {}},
            ],
        )

        result = self.run_helper("validate")

        self.assert_rejected(result)
        self.assertEqual(self.mutating_operations(), [])
        self.assert_no_dangerous_operations()

    def test_ambiguous_release_state_is_rejected_without_mutation(self) -> None:
        self.reset_state(
            tags={VERSION: GITHUB_SHA},
            releases=[
                {"tag_name": VERSION, "draft": True, "assets": {}},
                {"tag_name": VERSION, "draft": False, "assets": {}},
            ],
        )

        result = self.run_helper("validate")

        self.assert_rejected(result)
        self.assertEqual(self.mutating_operations(), [])
        self.assert_no_dangerous_operations()

    def test_draft_with_unexpected_remote_asset_is_rejected_before_mutation(
        self,
    ) -> None:
        self.reset_state(
            tags={VERSION: GITHUB_SHA},
            releases=[
                {
                    "tag_name": VERSION,
                    "draft": True,
                    "assets": {"unexpected-debug-binary": "00"},
                },
            ],
        )

        result = self.run_helper("publish", RELEASE_STATE="draft")

        self.assert_rejected(result)
        self.assertEqual(self.mutating_operations(), [])
        self.assertEqual(
            self.release()["assets"],
            {"unexpected-debug-binary": "00"},
        )
        self.assertTrue(self.release()["draft"])
        self.assert_no_dangerous_operations()

    def test_partial_expected_draft_is_repaired_to_exact_current_inventory(
        self,
    ) -> None:
        first_asset, second_asset = EXPECTED_ASSETS[:2]
        self.reset_state(
            tags={VERSION: GITHUB_SHA},
            releases=[
                {
                    "tag_name": VERSION,
                    "draft": True,
                    "assets": {
                        first_asset: b"stale payload".hex(),
                        second_asset: self.asset_contents[second_asset].hex(),
                    },
                },
            ],
        )

        result = self.run_helper("publish", RELEASE_STATE="draft")

        self.assert_success(result)
        self.assertEqual(
            self.release()["assets"],
            self.expected_remote_assets(),
        )
        self.assertFalse(self.release()["draft"])
        self.assert_no_dangerous_operations()

    def test_post_upload_unexpected_asset_blocks_publication(self) -> None:
        self.reset_state(
            tags={VERSION: GITHUB_SHA},
            releases=[
                {"tag_name": VERSION, "draft": True, "assets": {}},
            ],
            inject_extra_after_upload=True,
        )

        result = self.run_helper("publish", RELEASE_STATE="draft")

        self.assert_rejected(result)
        self.assertTrue(self.release()["draft"])
        self.assertIn(
            "unexpected-concurrent-asset",
            self.release()["assets"],
        )
        publish_operations = [
            operation
            for operation in self.state()["operations"]
            if operation["tool"] == "gh"
            and operation["argv"][:2] == ["release", "edit"]
        ]
        self.assertEqual(publish_operations, [])
        self.assert_no_dangerous_operations()

    def test_publish_rechecks_and_rejects_a_non_draft_release(self) -> None:
        self.reset_state(
            tags={VERSION: GITHUB_SHA},
            releases=[
                {"tag_name": VERSION, "draft": False, "assets": {}},
            ],
        )

        result = self.run_helper("publish", RELEASE_STATE="draft")

        self.assert_rejected(result)
        self.assertEqual(self.mutating_operations(), [])
        self.assertFalse(self.release()["draft"])
        self.assert_no_dangerous_operations()

    def test_retry_after_partial_upload_repairs_and_publishes_draft(
        self,
    ) -> None:
        self.reset_state(
            tags={VERSION: GITHUB_SHA},
            releases=[
                {"tag_name": VERSION, "draft": True, "assets": {}},
            ],
            fail_upload_after=2,
        )

        first_attempt = self.run_helper(
            "publish",
            RELEASE_STATE="draft",
        )

        self.assert_rejected(first_attempt)
        self.assertTrue(self.release()["draft"])
        self.assertEqual(len(self.release()["assets"]), 2)
        first_attempt_edits = [
            operation
            for operation in self.state()["operations"]
            if operation["tool"] == "gh"
            and operation["argv"][:2] == ["release", "edit"]
        ]
        self.assertEqual(first_attempt_edits, [])

        second_attempt = self.run_helper(
            "publish",
            RELEASE_STATE="draft",
        )

        self.assert_success(second_attempt)
        self.assertFalse(self.release()["draft"])
        self.assertEqual(
            self.release()["assets"],
            self.expected_remote_assets(),
        )
        self.assert_no_dangerous_operations()

    def test_extra_local_archive_is_never_uploaded(self) -> None:
        self.reset_state(tags={VERSION: GITHUB_SHA})
        (self.workspace / "unexpected-platform.tar.gz").write_bytes(b"rogue")

        result = self.run_helper("publish", RELEASE_STATE="absent")

        self.assert_success(result)
        self.assertFalse(self.release()["draft"])
        self.assertEqual(
            self.release()["assets"],
            self.expected_remote_assets(),
        )
        self.assertNotIn(
            "unexpected-platform.tar.gz",
            self.release()["assets"],
        )
        self.assert_no_dangerous_operations()

    def test_missing_local_release_asset_is_rejected_before_mutation(
        self,
    ) -> None:
        self.reset_state(tags={VERSION: GITHUB_SHA})
        (self.workspace / EXPECTED_ASSETS[0]).unlink()

        result = self.run_helper("publish", RELEASE_STATE="absent")

        self.assert_rejected(result)
        self.assertEqual(self.mutating_operations(), [])
        self.assertEqual(self.state()["releases"], [])
        self.assert_no_dangerous_operations()

    def test_validation_runs_canonical_version_ordering_check(self) -> None:
        # A canonical candidate whose base is older than the published
        # (legacy-scheme) latest must be rejected by the ordering check.
        result = self.run_helper("validate", VERSION="v0.7.9")

        self.assert_rejected(result)
        self.assertIn("must be newer", result.stderr)
        self.assertEqual(self.mutating_operations(), [])
        self.assert_no_dangerous_operations()

    def test_mismatched_tag_negative_case_kills_exit_guard_mutant(self) -> None:
        self.reset_state(tags={VERSION: OTHER_SHA})
        baseline = self.run_helper("validate")
        self.assert_rejected(baseline)

        source = self.helper.read_text(encoding="utf-8")
        mutant_source, replacements = re.subn(
            r"(?m)^([ \t]*)exit 1[ \t]*$",
            r"\1:",
            source,
        )
        self.assertGreater(
            replacements,
            0,
            msg="helper has no fail-closed `exit 1` guard to mutation-test",
        )
        mutant = self.scripts / "release_workflow_mutant.sh"
        mutant.write_text(mutant_source, encoding="utf-8")
        mutant.chmod(0o755)
        self.reset_state(tags={VERSION: OTHER_SHA})

        mutant_result = self.run_helper(
            "validate",
            helper=mutant,
        )

        self.assertEqual(
            mutant_result.returncode,
            0,
            msg=(
                "neutralizing fail-closed exits did not make the mismatched "
                "tag scenario survive; the mutation was not exercised"
                f"\nstdout:\n{mutant_result.stdout}"
                f"\nstderr:\n{mutant_result.stderr}"
            ),
        )
        with self.assertRaises(AssertionError):
            self.assert_rejected(mutant_result)


if __name__ == "__main__":
    unittest.main()
