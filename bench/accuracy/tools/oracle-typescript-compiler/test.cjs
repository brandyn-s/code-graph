"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const { spawnSync } = require("node:child_process");
const test = require("node:test");

const root = __dirname;

function sha256(bytes) {
  return require("node:crypto").createHash("sha256").update(bytes).digest("hex");
}

test("manifest serialization uses code-point key order", () => {
  const { canonicalFileManifest } = require("./main.cjs");
  assert.equal(
    canonicalFileManifest({ "src/alpha.ts": "b", "src/Zed.ts": "a" }),
    '{"src/Zed.ts":"a","src/alpha.ts":"b"}',
  );
});

test("compiler oracle matches the hand-enumerated fixture", () => {
  const run = spawnSync(
    process.execPath,
    [path.join(root, "main.cjs"), path.join(root, "fixture", "tsconfig.json")],
    { cwd: root, encoding: "utf8" },
  );
  assert.equal(run.status, 0, run.stderr || run.stdout);

  const actual = JSON.parse(run.stdout);
  const expected = JSON.parse(
    fs.readFileSync(path.join(root, "fixture", "ground_truth.json"), "utf8"),
  );
  assert.equal(actual.oracle, "typescript-compiler-api-call-target-v1");
  assert.deepEqual(actual.edges, expected.edges);
  assert.equal(
    actual.imports_oracle,
    "typescript-compiler-api-module-resolution-v1",
  );
  assert.deepEqual(actual.project_files, ["src/main.ts", "src/math.ts"]);
  assert.deepEqual(actual.imports, expected.imports);
  assert.equal(
    actual.type_relationships_oracle,
    "typescript-compiler-api-type-relationships-v1",
  );
  const expectedFileHashes = Object.fromEntries(
    actual.project_files.map((file) => [
      file,
      sha256(fs.readFileSync(path.join(root, "fixture", file))),
    ]),
  );
  assert.equal(
    actual.oracle_implementation_sha256,
    sha256(fs.readFileSync(path.join(root, "main.cjs"))),
  );
  assert.deepEqual(actual.project_file_sha256, expectedFileHashes);
  assert.equal(
    actual.project_manifest_sha256,
    sha256(
      JSON.stringify(
        Object.fromEntries(Object.entries(expectedFileHashes).sort()),
      ),
    ),
  );
  const byKey = (relationships) => [...relationships].sort((left, right) =>
    left.source.file.localeCompare(right.source.file) ||
    left.source.line - right.source.line ||
    left.kind.localeCompare(right.kind) ||
    left.target.file.localeCompare(right.target.file) ||
    left.target.line - right.target.line,
  );
  assert.deepEqual(actual.type_relationships, byKey(expected.type_relationships));
});
