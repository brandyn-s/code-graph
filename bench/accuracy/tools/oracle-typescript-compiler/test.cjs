"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const { spawnSync } = require("node:child_process");
const test = require("node:test");

const root = __dirname;

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
});
