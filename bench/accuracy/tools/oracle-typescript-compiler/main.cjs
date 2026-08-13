#!/usr/bin/env node
"use strict";

// Independent TypeScript CALLS and IMPORTS oracle. This reads source through
// the public TypeScript compiler API and never reads SCIP, code-graph
// databases, or graph output. CALLS keys are declaration coordinates; IMPORTS
// keys are compiler-resolved project-local source-file pairs.

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

function canonicalFileManifest(fileHashes) {
  return JSON.stringify(Object.fromEntries(Object.entries(fileHashes).sort()));
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
  const importByKey = new Map();
  const typeRelationshipByKey = new Map();
  const projectFiles = program
    .getSourceFiles()
    .map((sourceFile) => projectFile(root, sourceFile))
    .filter((file) => file !== null)
    .sort((left, right) => left.localeCompare(right));
  const projectFileSha256 = Object.fromEntries(
    projectFiles.map((file) => [file, sha256File(path.join(root, file))]),
  );

  function recordImport(sourceFile, moduleSpecifier) {
    if (!moduleSpecifier || !ts.isStringLiteralLike(moduleSpecifier)) {
      return;
    }
    const resolved = ts.resolveModuleName(
      moduleSpecifier.text,
      sourceFile.fileName,
      parsed.options,
      ts.sys,
    ).resolvedModule;
    if (!resolved) {
      return;
    }
    const targetSource = program.getSourceFile(resolved.resolvedFileName);
    if (!targetSource) {
      return;
    }
    const source = projectFile(root, sourceFile);
    const target = projectFile(root, targetSource);
    if (source === null || target === null || source === target) {
      return;
    }
    const key = `${source}\0${target}`;
    importByKey.set(key, {
      source: { file: source },
      target: { file: target },
    });
  }

  function recordTypeRelationships(declaration) {
    const source = coordinate(root, declaration);
    if (!source || !source.name || !declaration.heritageClauses) {
      return;
    }
    for (const clause of declaration.heritageClauses) {
      const kind = clause.token === ts.SyntaxKind.ExtendsKeyword
        ? "extends"
        : clause.token === ts.SyntaxKind.ImplementsKeyword
          ? "implements"
          : null;
      if (kind === null) {
        continue;
      }
      for (const type of clause.types) {
        let symbol = checker.getSymbolAtLocation(type.expression);
        if (symbol && (symbol.flags & ts.SymbolFlags.Alias) !== 0) {
          symbol = checker.getAliasedSymbol(symbol);
        }
        const targetDeclaration = symbol && symbol.declarations
          ? symbol.declarations.find((candidate) =>
              ts.isClassDeclaration(candidate) ||
              ts.isInterfaceDeclaration(candidate) ||
              ts.isTypeAliasDeclaration(candidate))
          : null;
        if (!targetDeclaration) {
          continue;
        }
        const target = coordinate(root, targetDeclaration);
        if (!target || !target.name) {
          continue;
        }
        const key = `${kind}\0${source.file}\0${source.line}\0${target.file}\0${target.line}`;
        typeRelationshipByKey.set(key, { kind, source, target });
      }
    }
  }

  function visit(node) {
    if (
      (ts.isImportDeclaration(node) || ts.isExportDeclaration(node)) &&
      node.moduleSpecifier
    ) {
      recordImport(node.getSourceFile(), node.moduleSpecifier);
    } else if (
      ts.isImportEqualsDeclaration(node) &&
      ts.isExternalModuleReference(node.moduleReference)
    ) {
      recordImport(node.getSourceFile(), node.moduleReference.expression);
    }
    if (ts.isClassDeclaration(node) || ts.isInterfaceDeclaration(node)) {
      recordTypeRelationships(node);
    }
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
  const imports = [...importByKey.values()].sort((left, right) =>
    left.source.file.localeCompare(right.source.file) ||
    left.target.file.localeCompare(right.target.file),
  );
  const typeRelationships = [...typeRelationshipByKey.values()].sort((left, right) =>
    left.source.file.localeCompare(right.source.file) ||
    left.source.line - right.source.line ||
    left.kind.localeCompare(right.kind) ||
    left.target.file.localeCompare(right.target.file) ||
    left.target.line - right.target.line,
  );
  return {
    schema_version: 1,
    oracle: "typescript-compiler-api-call-target-v1",
    oracle_scope: "static_source_calls_with_function_like_callers",
    imports_oracle: "typescript-compiler-api-module-resolution-v1",
    imports_oracle_scope: "static_project_local_module_resolution",
    type_relationships_oracle: "typescript-compiler-api-type-relationships-v1",
    type_relationships_oracle_scope: "declared_project_local_extends_and_implements",
    oracle_implementation_sha256: sha256File(__filename),
    typescript_version: ts.version,
    tsconfig_sha256: sha256File(configPath),
    project_files: projectFiles,
    project_file_sha256: projectFileSha256,
    project_manifest_sha256: crypto
      .createHash("sha256")
      .update(canonicalFileManifest(projectFileSha256))
      .digest("hex"),
    imports,
    type_relationships: typeRelationships,
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

module.exports = { canonicalFileManifest, extract };
