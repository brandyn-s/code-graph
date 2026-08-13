#include "cbm.h"
#include "helpers.h"
#include "lang_specs.h"
#include <string.h>
#include <ctype.h>

// Forward declarations
static void parse_go_imports(CBMExtractCtx* ctx);
static void parse_python_imports(CBMExtractCtx* ctx);
static void parse_es_imports(CBMExtractCtx* ctx);
static void parse_java_imports(CBMExtractCtx* ctx);
static void parse_rust_imports(CBMExtractCtx* ctx);
static void parse_c_imports(CBMExtractCtx* ctx);
static void parse_generic_imports(CBMExtractCtx* ctx, const char* node_type);

// Helper: strip quotes from a string literal
static char* strip_quotes(CBMArena* a, const char* s) {
    if (!s) return NULL;
    size_t len = strlen(s);
    if (len >= 2 && (s[0] == '"' || s[0] == '\'') && s[len-1] == s[0]) {
        return cbm_arena_strndup(a, s + 1, len - 2);
    }
    return cbm_arena_strdup(a, s);
}

// Helper: get last path component as local name
static const char* path_last(CBMArena* a, const char* path) {
    if (!path) return NULL;
    const char* last = strrchr(path, '/');
    if (last) return cbm_arena_strdup(a, last + 1);
    // Rust `::` separator — find the last `::` occurrence.
    const char* p = path;
    const char* last_colon_colon = NULL;
    while ((p = strstr(p, "::")) != NULL) {
        last_colon_colon = p;
        p += 2;
    }
    if (last_colon_colon) {
        return cbm_arena_strdup(a, last_colon_colon + 2);
    }
    last = strrchr(path, '.');
    if (last) return cbm_arena_strdup(a, last + 1);
    return path;
}

// --- Go imports ---
// import_declaration -> import_spec_list -> import_spec -> (name, path)

static void parse_go_imports(CBMExtractCtx* ctx) {
    CBMArena* a = ctx->arena;

    uint32_t root_count = ts_node_child_count(ctx->root);
    for (uint32_t i = 0; i < root_count; i++) {
        TSNode decl = ts_node_child(ctx->root, i);
        if (strcmp(ts_node_type(decl), "import_declaration") != 0) continue;

        uint32_t dc = ts_node_child_count(decl);
        for (uint32_t j = 0; j < dc; j++) {
            TSNode child = ts_node_child(decl, j);
            const char* ck = ts_node_type(child);
            if (strcmp(ck, "import_spec") != 0 && strcmp(ck, "import_spec_list") != 0) continue;

            if (strcmp(ck, "import_spec") == 0) {
                TSNode path_node = ts_node_child_by_field_name(child, "path", 4);
                if (ts_node_is_null(path_node)) continue;
                char* path = strip_quotes(a, cbm_node_text(a, path_node, ctx->source));
                if (!path || !path[0]) continue;

                TSNode name_node = ts_node_child_by_field_name(child, "name", 4);
                const char* local_name;
                if (!ts_node_is_null(name_node)) {
                    local_name = cbm_node_text(a, name_node, ctx->source);
                } else {
                    local_name = path_last(a, path);
                }

                CBMImport imp = {.local_name = local_name, .module_path = path};
                cbm_imports_push(&ctx->result->imports, a, imp);
            } else {
                // import_spec_list: contains multiple import_spec children
                uint32_t sc = ts_node_child_count(child);
                for (uint32_t k = 0; k < sc; k++) {
                    TSNode spec = ts_node_child(child, k);
                    if (strcmp(ts_node_type(spec), "import_spec") != 0) continue;

                    TSNode path_node = ts_node_child_by_field_name(spec, "path", 4);
                    if (ts_node_is_null(path_node)) continue;
                    char* path = strip_quotes(a, cbm_node_text(a, path_node, ctx->source));
                    if (!path || !path[0]) continue;

                    TSNode name_node = ts_node_child_by_field_name(spec, "name", 4);
                    const char* local_name;
                    if (!ts_node_is_null(name_node)) {
                        local_name = cbm_node_text(a, name_node, ctx->source);
                    } else {
                        local_name = path_last(a, path);
                    }

                    CBMImport imp = {.local_name = local_name, .module_path = path};
                    cbm_imports_push(&ctx->result->imports, a, imp);
                }
            }
        }
    }
}

// --- Python imports ---
// import_statement: import X, import X as Y
// import_from_statement: from X import Y, from X import Y as Z
//
// Python imports can appear at ANY depth: module-level, inside functions
// (lazy imports), inside classes, inside conditionals. The earlier version
// only scanned direct children of the AST root and missed ~75% of cross-
// service imports in service code that uses lazy-loading patterns (e.g.,
// `        from shared.errors import api_error` inside a function body).
// The current implementation walks the full AST and dispatches each
// `import_statement` / `import_from_statement` to its handler regardless
// of depth. Measured gap before the fix (mcp-servers fixture): 10 edges
// captured vs 42 the AST oracle finds. Expected after fix: match the
// AST oracle's full set.

static void handle_python_import_statement(CBMExtractCtx* ctx, TSNode node) {
    CBMArena* a = ctx->arena;
    // import X [as Y]
    TSNode name_node = ts_node_child_by_field_name(node, "name", 4);
    if (ts_node_is_null(name_node)) {
        // Try children: dotted_name or aliased_import
        uint32_t nc = ts_node_child_count(node);
        for (uint32_t j = 0; j < nc; j++) {
            TSNode child = ts_node_child(node, j);
            const char* ck = ts_node_type(child);
            if (strcmp(ck, "dotted_name") == 0 || strcmp(ck, "identifier") == 0) {
                char* mod = cbm_node_text(a, child, ctx->source);
                if (mod && mod[0]) {
                    const char* local = path_last(a, mod);
                    CBMImport imp = {.local_name = local, .module_path = mod};
                    cbm_imports_push(&ctx->result->imports, a, imp);
                }
            } else if (strcmp(ck, "aliased_import") == 0) {
                TSNode mod_node = ts_node_child_by_field_name(child, "name", 4);
                TSNode alias_node = ts_node_child_by_field_name(child, "alias", 5);
                if (!ts_node_is_null(mod_node)) {
                    char* mod = cbm_node_text(a, mod_node, ctx->source);
                    const char* local = !ts_node_is_null(alias_node) ?
                        cbm_node_text(a, alias_node, ctx->source) : path_last(a, mod);
                    CBMImport imp = {.local_name = local, .module_path = mod};
                    cbm_imports_push(&ctx->result->imports, a, imp);
                }
            }
        }
    } else {
        char* mod = cbm_node_text(a, name_node, ctx->source);
        if (mod && mod[0]) {
            CBMImport imp = {.local_name = path_last(a, mod), .module_path = mod};
            cbm_imports_push(&ctx->result->imports, a, imp);
        }
    }
}

static void handle_python_import_from_statement(CBMExtractCtx* ctx, TSNode node) {
    CBMArena* a = ctx->arena;
    // from X import Y [as Z]
    TSNode module_node = ts_node_child_by_field_name(node, "module_name", 11);
    if (ts_node_is_null(module_node)) {
        // Try alternative field names
        uint32_t nc = ts_node_child_count(node);
        for (uint32_t j = 0; j < nc; j++) {
            TSNode c = ts_node_child(node, j);
            if (strcmp(ts_node_type(c), "dotted_name") == 0 ||
                strcmp(ts_node_type(c), "relative_import") == 0) {
                module_node = c;
                break;
            }
        }
    }
    char* mod_path = ts_node_is_null(module_node) ? NULL :
        cbm_node_text(a, module_node, ctx->source);

    // Find imported names
    uint32_t nc = ts_node_child_count(node);
    for (uint32_t j = 0; j < nc; j++) {
        TSNode child = ts_node_child(node, j);
        const char* ck = ts_node_type(child);
        if (strcmp(ck, "identifier") == 0 || strcmp(ck, "dotted_name") == 0) {
            // Skip the module name node
            if (!ts_node_is_null(module_node) &&
                ts_node_start_byte(child) == ts_node_start_byte(module_node)) continue;
            char* name = cbm_node_text(a, child, ctx->source);
            if (name && name[0]) {
                const char* full = mod_path ?
                    cbm_arena_sprintf(a, "%s.%s", mod_path, name) : name;
                CBMImport imp = {.local_name = name, .module_path = full};
                cbm_imports_push(&ctx->result->imports, a, imp);
            }
        } else if (strcmp(ck, "aliased_import") == 0) {
            TSNode name_n = ts_node_child_by_field_name(child, "name", 4);
            TSNode alias_n = ts_node_child_by_field_name(child, "alias", 5);
            if (!ts_node_is_null(name_n)) {
                char* name = cbm_node_text(a, name_n, ctx->source);
                const char* local = !ts_node_is_null(alias_n) ?
                    cbm_node_text(a, alias_n, ctx->source) : name;
                const char* full = mod_path ?
                    cbm_arena_sprintf(a, "%s.%s", mod_path, name) : name;
                CBMImport imp = {.local_name = local, .module_path = full};
                cbm_imports_push(&ctx->result->imports, a, imp);
            }
        }
    }
}

// Recursive walker: visit `node` and every descendant; dispatch each
// import statement to its handler. This catches deferred/lazy imports
// nested inside functions, conditionals, try/except blocks, etc. — the
// pattern used heavily in mcp-servers/airlock for cross-service
// `from shared.* import *` calls.
static void walk_python_imports(CBMExtractCtx* ctx, TSNode node) {
    const char* kind = ts_node_type(node);
    if (strcmp(kind, "import_statement") == 0) {
        handle_python_import_statement(ctx, node);
        // Import statements are leaves for our purposes; don't recurse.
        return;
    }
    if (strcmp(kind, "import_from_statement") == 0) {
        handle_python_import_from_statement(ctx, node);
        return;
    }
    uint32_t count = ts_node_child_count(node);
    for (uint32_t i = 0; i < count; i++) {
        walk_python_imports(ctx, ts_node_child(node, i));
    }
}

static void parse_python_imports(CBMExtractCtx* ctx) {
    walk_python_imports(ctx, ctx->root);
}

// --- ES module imports (JS/TS/TSX) ---
// import X from "Y"; import {A, B} from "Y"; import * as X from "Y"
// const X = require("Y")

static void walk_es_imports(CBMExtractCtx* ctx, TSNode node) {
    CBMArena* a = ctx->arena;
    const char* kind = ts_node_type(node);

    if (strcmp(kind, "export_statement") == 0) {
        // Re-exports (`export {X} from "./x.js"`, `export * from ...`)
        // create a module dependency without introducing a local binding.
        TSNode source_node = ts_node_child_by_field_name(node, "source", 6);
        if (!ts_node_is_null(source_node)) {
            char* path = strip_quotes(a, cbm_node_text(a, source_node, ctx->source));
            if (path && path[0]) {
                CBMImport imp = {
                    .local_name = path,
                    .module_path = path,
                    .dependency_only = true,
                };
                cbm_imports_push(&ctx->result->imports, a, imp);
            }
        }
        return;
    }

    if (strcmp(kind, "import_statement") == 0) {
        TSNode source_node = ts_node_child_by_field_name(node, "source", 6);
        if (ts_node_is_null(source_node)) {
            // Try last string child
            uint32_t nc = ts_node_child_count(node);
            for (int j = (int)nc - 1; j >= 0; j--) {
                TSNode c = ts_node_child(node, (uint32_t)j);
                const char* ck = ts_node_type(c);
                if (strcmp(ck, "string") == 0 || strcmp(ck, "string_literal") == 0) {
                    source_node = c;
                    break;
                }
            }
        }
        if (ts_node_is_null(source_node)) goto recurse;

        char* path = strip_quotes(a, cbm_node_text(a, source_node, ctx->source));
        if (!path || !path[0]) goto recurse;

        // Default import, namespace import, named imports
        uint32_t nc = ts_node_child_count(node);
        bool found = false;
        for (uint32_t j = 0; j < nc; j++) {
            TSNode child = ts_node_child(node, j);
            const char* ck = ts_node_type(child);

            if (strcmp(ck, "identifier") == 0) {
                // Default import: import X from "Y"
                char* name = cbm_node_text(a, child, ctx->source);
                CBMImport imp = {.local_name = name, .module_path = path};
                cbm_imports_push(&ctx->result->imports, a, imp);
                found = true;
            } else if (strcmp(ck, "import_clause") == 0) {
                // Walk into import clause for named/default imports
                uint32_t cc = ts_node_child_count(child);
                for (uint32_t k = 0; k < cc; k++) {
                    TSNode sub = ts_node_child(child, k);
                    const char* sk = ts_node_type(sub);
                    if (strcmp(sk, "identifier") == 0) {
                        char* name = cbm_node_text(a, sub, ctx->source);
                        CBMImport imp = {.local_name = name, .module_path = path};
                        cbm_imports_push(&ctx->result->imports, a, imp);
                        found = true;
                    } else if (strcmp(sk, "namespace_import") == 0) {
                        TSNode as_name = ts_node_child_by_field_name(sub, "name", 4);
                        if (ts_node_is_null(as_name) && ts_node_child_count(sub) > 0) {
                            as_name = ts_node_child(sub, ts_node_child_count(sub) - 1);
                        }
                        if (!ts_node_is_null(as_name)) {
                            char* name = cbm_node_text(a, as_name, ctx->source);
                            CBMImport imp = {.local_name = name, .module_path = path};
                            cbm_imports_push(&ctx->result->imports, a, imp);
                            found = true;
                        }
                    } else if (strcmp(sk, "named_imports") == 0) {
                        uint32_t nc2 = ts_node_child_count(sub);
                        for (uint32_t m = 0; m < nc2; m++) {
                            TSNode imp_spec = ts_node_child(sub, m);
                            if (strcmp(ts_node_type(imp_spec), "import_specifier") == 0) {
                                TSNode local = ts_node_child_by_field_name(imp_spec, "alias", 5);
                                TSNode orig = ts_node_child_by_field_name(imp_spec, "name", 4);
                                if (ts_node_is_null(orig) && ts_node_child_count(imp_spec) > 0) {
                                    orig = ts_node_child(imp_spec, 0);
                                }
                                if (!ts_node_is_null(orig)) {
                                    char* local_name = !ts_node_is_null(local) ?
                                        cbm_node_text(a, local, ctx->source) :
                                        cbm_node_text(a, orig, ctx->source);
                                    CBMImport imp = {.local_name = local_name, .module_path = path};
                                    cbm_imports_push(&ctx->result->imports, a, imp);
                                    found = true;
                                }
                            }
                        }
                    }
                }
            }
        }

        if (!found) {
            // Side-effect import: import "Y"
            CBMImport imp = {.local_name = path_last(a, path), .module_path = path};
            cbm_imports_push(&ctx->result->imports, a, imp);
        }
        return;
    }

recurse:
    ;
    uint32_t count = ts_node_child_count(node);
    for (uint32_t i = 0; i < count; i++) {
        walk_es_imports(ctx, ts_node_child(node, i));
    }
}

static void parse_es_imports(CBMExtractCtx* ctx) {
    walk_es_imports(ctx, ctx->root);
}

// --- Java imports ---
// import_declaration -> scoped_identifier

static void parse_java_imports(CBMExtractCtx* ctx) {
    CBMArena* a = ctx->arena;

    uint32_t count = ts_node_child_count(ctx->root);
    for (uint32_t i = 0; i < count; i++) {
        TSNode node = ts_node_child(ctx->root, i);
        if (strcmp(ts_node_type(node), "import_declaration") != 0) continue;

        // Get the full import path (skip "import" and "static" keywords)
        uint32_t nc = ts_node_child_count(node);
        for (uint32_t j = 0; j < nc; j++) {
            TSNode child = ts_node_child(node, j);
            const char* ck = ts_node_type(child);
            if (strcmp(ck, "scoped_identifier") == 0 || strcmp(ck, "identifier") == 0) {
                char* path = cbm_node_text(a, child, ctx->source);
                if (path && path[0]) {
                    CBMImport imp = {.local_name = path_last(a, path), .module_path = path};
                    cbm_imports_push(&ctx->result->imports, a, imp);
                }
                break;
            }
        }
    }
}

// --- Rust imports ---
//
// Tree-sitter Rust use_declaration grammar:
//   use_declaration -> "use" use_clause ";"
// use_clause is one of:
//   - identifier               `use foo;`
//   - scoped_identifier        `use a::b::c;`
//   - use_as_clause            `use a::b as c;`
//   - scoped_use_list          `use a::b::{x, y};`
//   - use_list                 (bare `{x, y}` — rare)
//   - use_wildcard             `use a::*;`
//
// The walker recursively decomposes nested groups (`use a::{b::{c, d}, e};`)
// into one CBMImport per leaf identifier, with the prefix path tracked.
// Pre-2026-05-24: the parser took the full use_declaration text and emitted
// ONE CBMImport with a garbage local_name like "{x, y, z}". This dropped
// every binding inside a group, breaking the resolver's import-reachability
// gate for thousands of `Type::method` static calls.

// Join two `::`-separated path segments. Either side may be empty.
static const char* join_use_path(CBMArena* a, const char* prefix, const char* segment) {
    if (!prefix || !prefix[0]) {
        return segment ? cbm_arena_strdup(a, segment) : NULL;
    }
    if (!segment || !segment[0]) {
        return cbm_arena_strdup(a, prefix);
    }
    return cbm_arena_sprintf(a, "%s::%s", prefix, segment);
}

// Return the last `::`-separated segment of a scoped path string.
// `a::b::c` -> `c`. `c` -> `c`. NULL/empty -> empty string.
static const char* last_use_segment(const char* text) {
    if (!text || !text[0]) return "";
    const char* p = text;
    const char* last = text;
    while ((p = strstr(p, "::")) != NULL) {
        last = p + 2;
        p += 2;
    }
    return last;
}

// Recursive walker for a Rust use-tree node. Emits CBMImports for every
// leaf identifier (or alias) it encounters, with the module_path built
// from `prefix` plus the leaf's scoped position.
static void emit_rust_use_clause(CBMExtractCtx* ctx, TSNode node, const char* prefix) {
    if (ts_node_is_null(node)) return;
    const char* kind = ts_node_type(node);
    CBMArena* a = ctx->arena;

    // Leaf: bare identifier (`foo` or `self`).
    if (strcmp(kind, "identifier") == 0 || strcmp(kind, "self") == 0) {
        char* name = cbm_node_text(a, node, ctx->source);
        if (!name || !name[0]) return;
        const char* full_path = join_use_path(a, prefix, name);
        if (!full_path) return;
        // For `use foo::{self};` the leaf is `self` and the binding name
        // is the prefix's last segment. Otherwise the binding name is the
        // identifier itself.
        const char* local = name;
        if (strcmp(name, "self") == 0 && prefix && prefix[0]) {
            local = cbm_arena_strdup(a, last_use_segment(prefix));
            if (!local || !local[0]) return;
            // module_path drops the `::self` tail — the alias maps to the prefix.
            CBMImport imp = {.local_name = local, .module_path = cbm_arena_strdup(a, prefix)};
            cbm_imports_push(&ctx->result->imports, a, imp);
            return;
        }
        CBMImport imp = {.local_name = name, .module_path = full_path};
        cbm_imports_push(&ctx->result->imports, a, imp);
        return;
    }

    // Leaf: scoped path (`a::b::c`). The binding name is the last segment.
    if (strcmp(kind, "scoped_identifier") == 0) {
        char* text = cbm_node_text(a, node, ctx->source);
        if (!text || !text[0]) return;
        const char* last = last_use_segment(text);
        if (!last || !last[0]) return;
        char* local = cbm_arena_strdup(a, last);
        const char* full_path = join_use_path(a, prefix, text);
        if (!full_path) return;
        CBMImport imp = {.local_name = local, .module_path = full_path};
        cbm_imports_push(&ctx->result->imports, a, imp);
        return;
    }

    // Alias: `<path> as <alias>`. The alias is the local_name; the path
    // (joined with prefix) is the module_path.
    if (strcmp(kind, "use_as_clause") == 0) {
        TSNode path = ts_node_child_by_field_name(node, "path", 4);
        TSNode alias = ts_node_child_by_field_name(node, "alias", 5);
        if (ts_node_is_null(path) || ts_node_is_null(alias)) return;
        char* path_text = cbm_node_text(a, path, ctx->source);
        char* alias_text = cbm_node_text(a, alias, ctx->source);
        if (!path_text || !path_text[0] || !alias_text || !alias_text[0]) return;
        const char* full_path = join_use_path(a, prefix, path_text);
        if (!full_path) return;
        CBMImport imp = {.local_name = alias_text, .module_path = full_path};
        cbm_imports_push(&ctx->result->imports, a, imp);
        return;
    }

    // Scoped group: `<path>::{...}`. Extract the prefix, recurse on list items.
    if (strcmp(kind, "scoped_use_list") == 0) {
        TSNode path = ts_node_child_by_field_name(node, "path", 4);
        TSNode list = ts_node_child_by_field_name(node, "list", 4);
        const char* new_prefix = prefix;
        if (!ts_node_is_null(path)) {
            char* path_text = cbm_node_text(a, path, ctx->source);
            if (path_text && path_text[0]) {
                new_prefix = join_use_path(a, prefix, path_text);
            }
        }
        if (!ts_node_is_null(list)) {
            uint32_t n = ts_node_named_child_count(list);
            for (uint32_t i = 0; i < n; i++) {
                emit_rust_use_clause(ctx, ts_node_named_child(list, i), new_prefix);
            }
        }
        return;
    }

    // Bare group: `{a, b, c}` (no scoped prefix). Each child inherits the
    // outer prefix unchanged. Rare in practice but grammatically valid.
    if (strcmp(kind, "use_list") == 0) {
        uint32_t n = ts_node_named_child_count(node);
        for (uint32_t i = 0; i < n; i++) {
            emit_rust_use_clause(ctx, ts_node_named_child(node, i), prefix);
        }
        return;
    }

    // Wildcard `*`: brings unspecified names into scope. No binding to emit.
    if (strcmp(kind, "use_wildcard") == 0) {
        return;
    }

    // Unknown clause kind: silently skip rather than emit garbage.
}

static void parse_rust_imports(CBMExtractCtx* ctx) {
    uint32_t count = ts_node_child_count(ctx->root);
    for (uint32_t i = 0; i < count; i++) {
        TSNode node = ts_node_child(ctx->root, i);
        if (strcmp(ts_node_type(node), "use_declaration") != 0) continue;

        // The clause is the use_declaration's "argument" field. Tree-sitter
        // Rust grammar tags this child explicitly; fall back to the first
        // named child if the grammar version is older.
        TSNode clause = ts_node_child_by_field_name(node, "argument", 8);
        if (ts_node_is_null(clause)) {
            uint32_t nc = ts_node_named_child_count(node);
            if (nc > 0) clause = ts_node_named_child(node, 0);
        }
        if (ts_node_is_null(clause)) continue;
        emit_rust_use_clause(ctx, clause, "");
    }
}

// --- C/C++ imports ---
// preproc_include -> path or string_literal

static void parse_c_imports(CBMExtractCtx* ctx) {
    CBMArena* a = ctx->arena;

    uint32_t count = ts_node_child_count(ctx->root);
    for (uint32_t i = 0; i < count; i++) {
        TSNode node = ts_node_child(ctx->root, i);
        const char* kind = ts_node_type(node);
        if (strcmp(kind, "preproc_include") != 0 && strcmp(kind, "preproc_import") != 0) continue;

        TSNode path_node = ts_node_child_by_field_name(node, "path", 4);
        if (ts_node_is_null(path_node)) {
            // Try system_lib_string or string_literal
            uint32_t nc = ts_node_child_count(node);
            for (uint32_t j = 0; j < nc; j++) {
                TSNode c = ts_node_child(node, j);
                const char* ck = ts_node_type(c);
                if (strcmp(ck, "string_literal") == 0 || strcmp(ck, "system_lib_string") == 0) {
                    path_node = c;
                    break;
                }
            }
        }
        if (ts_node_is_null(path_node)) continue;

        char* path = strip_quotes(a, cbm_node_text(a, path_node, ctx->source));
        // Also strip angle brackets
        if (path && path[0] == '<') {
            size_t len = strlen(path);
            if (len > 1 && path[len-1] == '>') {
                path = cbm_arena_strndup(a, path + 1, len - 2);
            }
        }
        if (!path || !path[0]) continue;

        CBMImport imp = {.local_name = path_last(a, path), .module_path = path};
        cbm_imports_push(&ctx->result->imports, a, imp);
    }
}

// --- Generic import parsing for languages with simple import_declaration ---

static void parse_generic_imports(CBMExtractCtx* ctx, const char* node_type) {
    CBMArena* a = ctx->arena;

    uint32_t count = ts_node_child_count(ctx->root);
    for (uint32_t i = 0; i < count; i++) {
        TSNode node = ts_node_child(ctx->root, i);
        if (strcmp(ts_node_type(node), node_type) != 0) continue;

        // Try to find a path/source/module field
        static const char* path_fields[] = {"path","source","module","name",NULL};
        for (const char** f = path_fields; *f; f++) {
            TSNode path_node = ts_node_child_by_field_name(node, *f, (uint32_t)strlen(*f));
            if (!ts_node_is_null(path_node)) {
                char* path = strip_quotes(a, cbm_node_text(a, path_node, ctx->source));
                if (path && path[0]) {
                    CBMImport imp = {.local_name = path_last(a, path), .module_path = path};
                    cbm_imports_push(&ctx->result->imports, a, imp);
                }
                break;
            }
        }

        // Fallback: use full node text minus keyword
        if (ctx->result->imports.count == 0) {
            char* text = cbm_node_text(a, node, ctx->source);
            if (text && text[0]) {
                // Strip leading keyword
                char* space = strchr(text, ' ');
                if (space) text = space + 1;
                // Strip trailing semicolon
                size_t len = strlen(text);
                if (len > 0 && text[len-1] == ';') text[len-1] = '\0';
                if (text[0]) {
                    CBMImport imp = {.local_name = path_last(a, text), .module_path = text};
                    cbm_imports_push(&ctx->result->imports, a, imp);
                }
            }
        }
    }
}

// --- Main dispatch ---

void cbm_extract_imports(CBMExtractCtx* ctx) {
    switch (ctx->language) {
        case CBM_LANG_GO:
            parse_go_imports(ctx);
            break;
        case CBM_LANG_PYTHON:
            parse_python_imports(ctx);
            break;
        case CBM_LANG_JAVASCRIPT:
        case CBM_LANG_TYPESCRIPT:
        case CBM_LANG_TSX:
            parse_es_imports(ctx);
            break;
        case CBM_LANG_JAVA:
            parse_java_imports(ctx);
            break;
        case CBM_LANG_RUST:
            parse_rust_imports(ctx);
            break;
        case CBM_LANG_C:
        case CBM_LANG_CPP:
            parse_c_imports(ctx);
            break;
        case CBM_LANG_BASH:
            // source/. commands
            parse_generic_imports(ctx, "command");
            break;
        case CBM_LANG_CSS:
        case CBM_LANG_SCSS:
            parse_generic_imports(ctx, "import_statement");
            break;
        default:
            break;
    }
}
