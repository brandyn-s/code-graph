#!/usr/bin/env node
"use strict";

// Independent TypeScript CALLS oracle. This reads source through the public
// TypeScript compiler API and never reads SCIP, code-graph databases, or graph
// output. Its comparison key is the caller/callee declaration coordinate.

const crypto = require("node:crypto");
const fs = require("node:fs");
const path = require("node:path");
const ts = require("typescript");

function fail(message) {
  process.stderr.write(`[typescript-compiler-oracle] ${message}\n`);
  process.exitCode = 2;
}

function sha256File(file) {
  return crypto.createHash("sha256").update(fs.readFileSync(file)).digest("hex");
}

function projectFile(root, sourceFile) {
  const absolute = path.resolve(sourceFile.fileName);
  const relative = path.relative(root, absolute);
  if (
    relative === "" ||
    relative.startsWith(`..${path.sep}`) ||
    path.isAbsolute(relative) ||
    relative.split(path.sep).includes("node_modules") ||
    sourceFile.isDeclarationFile
  ) {
    return null;
  }
  return relative.split(path.sep).join("/");
}

function namedVariableFunction(declaration) {
  if (!ts.isFunctionExpression(declaration) && !ts.isArrowFunction(declaration)) {
    return null;
  }
  const variable = declaration.parent;
  if (!ts.isVariableDeclaration(variable) || !ts.isIdentifier(variable.name)) {
    return null;
  }
  const list = variable.parent;
  const statement = list && list.parent;
  if (
    !ts.isVariableDeclarationList(list) ||
    !ts.isVariableStatement(statement) ||
    !ts.isSourceFile(statement.parent)
  ) {
    return null;
  }
  return variable.name.text;
}

function functionLikeAncestor(node) {
  for (let current = node.parent; current; current = current.parent) {
    if (
      ts.isFunctionDeclaration(current) ||
      ts.isMethodDeclaration(current) ||
      ts.isConstructorDeclaration(current) ||
      ts.isGetAccessorDeclaration(current) ||
      ts.isSetAccessorDeclaration(current)
    ) {
      return current;
    }
    if (namedVariableFunction(current) !== null) {
      return current;
    }
  }
  return null;
}

function declarationName(declaration) {
  if (ts.isConstructorDeclaration(declaration)) {
    return "constructor";
  }
  if (declaration.name && ts.isIdentifier(declaration.name)) {
    return declaration.name.text;
  }
  if (declaration.name && ts.isPrivateIdentifier(declaration.name)) {
    return declaration.name.text;
  }
  const variableName = namedVariableFunction(declaration);
  if (variableName !== null) {
    return variableName;
  }
  return null;
}

function coordinate(root, declaration) {
  const sourceFile = declaration.getSourceFile();
  const file = projectFile(root, sourceFile);
  if (file === null) {
    return null;
  }
  const line = sourceFile.getLineAndCharacterOfPosition(
    declaration.getStart(sourceFile),
  ).line + 1;
  return { file, line, name: declarationName(declaration) };
}

function extract(tsconfigPath) {
  const configPath = path.resolve(tsconfigPath);
  const root = path.dirname(configPath);
  const loaded = ts.readConfigFile(configPath, ts.sys.readFile);
  if (loaded.error) {
    throw new Error(ts.flattenDiagnosticMessageText(loaded.error.messageText, "\n"));
  }
  const parsed = ts.parseJsonConfigFileContent(
    loaded.config,
    ts.sys,
    root,
    { noEmit: true },
    configPath,
  );
  if (parsed.errors.length > 0) {
    throw new Error(
      parsed.errors
        .map((diagnostic) => ts.flattenDiagnosticMessageText(diagnostic.messageText, "\n"))
        .join("\n"),
    );
  }

  const program = ts.createProgram({
    rootNames: parsed.fileNames,
    options: parsed.options,
    projectReferences: parsed.projectReferences,
  });
  const checker = program.getTypeChecker();
  const edgeByKey = new Map();

  function visit(node) {
    if (ts.isCallExpression(node) || ts.isNewExpression(node)) {
      const callerDeclaration = functionLikeAncestor(node);
      const signature = checker.getResolvedSignature(node);
      const calleeDeclaration = signature && signature.getDeclaration();
      if (callerDeclaration && calleeDeclaration) {
        const caller = coordinate(root, callerDeclaration);
        const callee = coordinate(root, calleeDeclaration);
        // The graph's compiler tier projects onto named Function/Method nodes.
        // Signatures declared only in type aliases/interfaces and local arrow
        // functions have no endpoint in that vocabulary, so exclude them.
        if (caller && caller.name && callee && callee.name) {
          const key = `${caller.file}\0${caller.line}\0${callee.file}\0${callee.line}`;
          edgeByKey.set(key, { caller, callee });
        }
      }
    }
    ts.forEachChild(node, visit);
  }

  for (const sourceFile of program.getSourceFiles()) {
    if (projectFile(root, sourceFile) !== null) {
      visit(sourceFile);
    }
  }

  const edges = [...edgeByKey.values()].sort((left, right) =>
    left.caller.file.localeCompare(right.caller.file) ||
    left.caller.line - right.caller.line ||
    left.callee.file.localeCompare(right.callee.file) ||
    left.callee.line - right.callee.line,
  );
  return {
    schema_version: 1,
    oracle: "typescript-compiler-api-call-target-v1",
    oracle_scope: "static_source_calls_with_function_like_callers",
    typescript_version: ts.version,
    tsconfig_sha256: sha256File(configPath),
    edges,
  };
}

if (require.main === module) {
  const argument = process.argv[2];
  if (!argument) {
    fail("usage: node main.cjs /path/to/tsconfig.json");
  } else {
    try {
      process.stdout.write(`${JSON.stringify(extract(argument), null, 2)}\n`);
    } catch (error) {
      fail(error instanceof Error ? error.message : String(error));
    }
  }
}

module.exports = { extract };
