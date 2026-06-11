#include "extract_unified.h"
#include "helpers.h"
#include <string.h>

// --- Scope stack management ---

static void push_scope(WalkState* state, uint8_t kind, uint32_t depth, const char* qn) {
    if (state->scope_top >= MAX_SCOPES) return;
    state->scopes[state->scope_top].kind = kind;
    state->scopes[state->scope_top].depth = depth;
    state->scopes[state->scope_top].qn = qn;
    state->scope_top++;
}

// Pop scopes that we've ascended out of (depth >= current cursor depth).
static void pop_expired_scopes(WalkState* state, uint32_t cur_depth) {
    while (state->scope_top > 0 &&
           state->scopes[state->scope_top - 1].depth >= cur_depth) {
        state->scope_top--;
    }
}

// Recompute state flags from the current scope stack.
static void recompute_state(WalkState* state, const char* module_qn) {
    state->enclosing_func_qn = module_qn;
    state->enclosing_class_qn = NULL;
    state->inside_call = false;
    state->inside_import = false;

    for (int i = 0; i < state->scope_top; i++) {
        switch (state->scopes[i].kind) {
        case SCOPE_FUNC:
            state->enclosing_func_qn = state->scopes[i].qn;
            break;
        case SCOPE_CLASS:
            state->enclosing_class_qn = state->scopes[i].qn;
            break;
        case SCOPE_CALL:
            state->inside_call = true;
            break;
        case SCOPE_IMPORT:
            state->inside_import = true;
            break;
        }
    }
}

// Compute function QN for scope tracking (mirrors cbm_enclosing_func_qn logic).
static const char* compute_func_qn(CBMExtractCtx* ctx, TSNode node,
                                     const CBMLangSpec* spec, WalkState* state) {
    TSNode name_node = ts_node_child_by_field_name(node, "name", 4);

    // PowerShell: no named fields — function_statement names are
    // `function_name`-typed children; class methods use `simple_name`.
    if (ts_node_is_null(name_node) && ctx->language == CBM_LANG_POWERSHELL) {
        name_node = cbm_find_child_by_kind(node, "function_name");
        if (ts_node_is_null(name_node)) {
            name_node = cbm_find_child_by_kind(node, "simple_name");
        }
    }

    // Arrow function: name from parent variable_declarator
    if (ts_node_is_null(name_node) && strcmp(ts_node_type(node), "arrow_function") == 0) {
        TSNode parent = ts_node_parent(node);
        if (!ts_node_is_null(parent) &&
            strcmp(ts_node_type(parent), "variable_declarator") == 0) {
            name_node = ts_node_child_by_field_name(parent, "name", 4);
        }
    }

    if (ts_node_is_null(name_node)) return NULL;

    char* name = cbm_node_text(ctx->arena, name_node, ctx->source);
    if (!name || !name[0]) return NULL;

    if (state->enclosing_class_qn) {
        return cbm_arena_sprintf(ctx->arena, "%s.%s", state->enclosing_class_qn, name);
    }

    // Go methods: if this function declaration has a receiver field, embed
    // the receiver type in the QN so call-site CallerQN matches the
    // method node's stored QN (which is also receiver-qualified — see
    // extract_defs.c). Without this, edges from inside a Go method
    // resolve their caller to `<file>.<method>` while the method's def
    // is stored as `<file>.<RecvType>.<method>`, producing 0 outbound
    // CALLS edges from any Method-label node.
    TSNode recv = ts_node_child_by_field_name(node, "receiver", 8);
    if (!ts_node_is_null(recv)) {
        const char* recv_type = NULL;
        uint32_t rn = ts_node_child_count(recv);
        for (uint32_t ri = 0; ri < rn; ri++) {
            TSNode p = ts_node_child(recv, ri);
            if (strcmp(ts_node_type(p), "parameter_declaration") != 0) continue;
            TSNode tnode = ts_node_child_by_field_name(p, "type", 4);
            if (ts_node_is_null(tnode)) continue;
            char* tn = cbm_node_text(ctx->arena, tnode, ctx->source);
            if (!tn || !tn[0]) continue;
            while (*tn == '*' || *tn == '&') tn++;
            char* bracket = strchr(tn, '[');
            if (bracket) *bracket = '\0';
            recv_type = tn;
            break;
        }
        if (recv_type && recv_type[0]) {
            const char* module_qn = cbm_fqn_compute(ctx->arena, ctx->project, ctx->rel_path, "");
            size_t mlen = strlen(module_qn);
            if (mlen > 0 && module_qn[mlen-1] == '.') {
                char* trimmed = cbm_arena_strndup(ctx->arena, module_qn, mlen - 1);
                return cbm_arena_sprintf(ctx->arena, "%s.%s.%s", trimmed, recv_type, name);
            }
            return cbm_arena_sprintf(ctx->arena, "%s.%s.%s", module_qn, recv_type, name);
        }
    }

    return cbm_fqn_compute(ctx->arena, ctx->project, ctx->rel_path, name);
}

// Compute class QN for scope tracking.
static const char* compute_class_qn(CBMExtractCtx* ctx, TSNode node) {
    TSNode name_node = ts_node_child_by_field_name(node, "name", 4);
    // PowerShell: class_statement's name is a `simple_name`-typed child.
    if (ts_node_is_null(name_node) && ctx->language == CBM_LANG_POWERSHELL) {
        name_node = cbm_find_child_by_kind(node, "simple_name");
    }
    if (ts_node_is_null(name_node)) return NULL;

    char* name = cbm_node_text(ctx->arena, name_node, ctx->source);
    if (!name || !name[0]) return NULL;

    return cbm_fqn_compute(ctx->arena, ctx->project, ctx->rel_path, name);
}

// --- Main unified cursor walk ---

void cbm_extract_unified(CBMExtractCtx* ctx) {
    const CBMLangSpec* spec = cbm_lang_spec(ctx->language);
    if (!spec) return;

    TSTreeCursor cursor = ts_tree_cursor_new(ctx->root);
    WalkState state;
    memset(&state, 0, sizeof(state));

    uint32_t depth = 0;

    for (;;) {
        TSNode node = ts_tree_cursor_current_node(&cursor);

        // 1. Pop scopes we've ascended out of
        pop_expired_scopes(&state, depth);

        // 2. Recompute state from remaining scopes
        recompute_state(&state, ctx->module_qn);

        // 3. Dispatch to all handlers
        handle_calls(ctx, node, spec, &state);
        handle_usages(ctx, node, spec, &state);
        handle_throws(ctx, node, spec, &state);
        handle_readwrites(ctx, node, spec, &state);
        handle_type_refs(ctx, node, spec, &state);
        handle_env_accesses(ctx, node, spec, &state);
        handle_type_assigns(ctx, node, spec, &state);

        // 4. Push scope markers for boundary nodes
        if (spec->function_node_types && cbm_kind_in_set(node, spec->function_node_types)) {
            const char* fqn = compute_func_qn(ctx, node, spec, &state);
            if (fqn) push_scope(&state, SCOPE_FUNC, depth, fqn);
        } else if (spec->class_node_types && cbm_kind_in_set(node, spec->class_node_types)) {
            const char* cqn = compute_class_qn(ctx, node);
            if (cqn) push_scope(&state, SCOPE_CLASS, depth, cqn);
        } else if (ctx->language == CBM_LANG_RUST &&
                   strcmp(ts_node_type(node), "impl_item") == 0) {
            // Rust impl block acts as class scope for methods.
            // Strip generic-parameter suffix (`Foo<T>` -> `Foo`) so the
            // class scope QN matches the canonical struct/trait QN — the
            // method-definition extraction in extract_defs.c does the same.
            TSNode type_node = ts_node_child_by_field_name(node, "type", 4);
            if (!ts_node_is_null(type_node)) {
                char* type_name = cbm_node_text(ctx->arena, type_node, ctx->source);
                if (type_name && type_name[0]) {
                    char* lt = strchr(type_name, '<');
                    if (lt) {
                        // Trim trailing whitespace before `<` and terminate.
                        char* p = lt;
                        while (p > type_name && (p[-1] == ' ' || p[-1] == '\t')) p--;
                        *p = '\0';
                    }
                    if (type_name[0]) {
                        const char* tqn = cbm_fqn_compute(ctx->arena, ctx->project,
                                                           ctx->rel_path, type_name);
                        push_scope(&state, SCOPE_CLASS, depth, tqn);
                    }
                }
            }
        }

        if (spec->call_node_types && cbm_kind_in_set(node, spec->call_node_types)) {
            push_scope(&state, SCOPE_CALL, depth, NULL);
        }
        if (spec->import_node_types && cbm_kind_in_set(node, spec->import_node_types)) {
            push_scope(&state, SCOPE_IMPORT, depth, NULL);
        }

        // 5. Advance cursor: DFS order
        if (ts_tree_cursor_goto_first_child(&cursor)) {
            depth++;
            continue;
        }
        if (ts_tree_cursor_goto_next_sibling(&cursor)) {
            continue;
        }
        // Ascend until we find a sibling
        bool found = false;
        while (ts_tree_cursor_goto_parent(&cursor)) {
            depth--;
            if (ts_tree_cursor_goto_next_sibling(&cursor)) {
                found = true;
                break;
            }
        }
        if (!found) break;
    }

    ts_tree_cursor_delete(&cursor);
}
