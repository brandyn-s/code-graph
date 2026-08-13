#include "lang_specs.h"

// -- Extern declarations for tree-sitter grammar functions --
// These symbols are defined in the grammar C code compiled by Go tree-sitter modules.
extern const TSLanguage* tree_sitter_go(void);
extern const TSLanguage* tree_sitter_python(void);
extern const TSLanguage* tree_sitter_javascript(void);
extern const TSLanguage* tree_sitter_typescript(void);
extern const TSLanguage* tree_sitter_tsx(void);
extern const TSLanguage* tree_sitter_rust(void);
extern const TSLanguage* tree_sitter_java(void);
extern const TSLanguage* tree_sitter_cpp(void);
extern const TSLanguage* tree_sitter_c(void);
extern const TSLanguage* tree_sitter_bash(void);
extern const TSLanguage* tree_sitter_powershell(void);
extern const TSLanguage* tree_sitter_html(void);
extern const TSLanguage* tree_sitter_css(void);
extern const TSLanguage* tree_sitter_scss(void);
extern const TSLanguage* tree_sitter_yaml(void);
extern const TSLanguage* tree_sitter_toml(void);
extern const TSLanguage* tree_sitter_hcl(void);
extern const TSLanguage* tree_sitter_sql(void);
extern const TSLanguage* tree_sitter_dockerfile(void);
extern const TSLanguage* tree_sitter_nix(void);
extern const TSLanguage* tree_sitter_cuda(void);
extern const TSLanguage* tree_sitter_json(void);
extern const TSLanguage* tree_sitter_xml(void);
extern const TSLanguage* tree_sitter_markdown(void);
extern const TSLanguage* tree_sitter_make(void);
extern const TSLanguage* tree_sitter_cmake(void);
extern const TSLanguage* tree_sitter_proto(void);

// -- Empty sentinel --
static const char* empty_types[] = {NULL};

// ==================== GO ====================
static const char* go_func_types[] = {"function_declaration","method_declaration","method_elem",NULL};
static const char* go_class_types[] = {"type_spec","type_alias",NULL};
static const char* go_field_types[] = {"field_declaration",NULL};
static const char* go_module_types[] = {"source_file",NULL};
static const char* go_call_types[] = {"call_expression",NULL};
static const char* go_import_types[] = {"import_declaration",NULL};
static const char* go_branch_types[] = {"if_statement","for_statement","switch_expression","select_statement","case_clause","default_clause",NULL};
static const char* go_var_types[] = {"var_declaration","const_declaration",NULL};
static const char* go_assign_types[] = {"assignment_statement","short_var_declaration",NULL};

// ==================== PYTHON ====================
static const char* py_func_types[] = {"function_definition",NULL};
static const char* py_class_types[] = {"class_definition",NULL};
static const char* py_module_types[] = {"module",NULL};
static const char* py_call_types[] = {"call","with_statement",NULL};
static const char* py_import_types[] = {"import_statement",NULL};
static const char* py_import_from_types[] = {"import_from_statement",NULL};
static const char* py_branch_types[] = {"if_statement","for_statement","while_statement","try_statement","except_clause","with_statement","elif_clause",NULL};
static const char* py_var_types[] = {"assignment","augmented_assignment",NULL};
static const char* py_throw_types[] = {"raise_statement",NULL};
static const char* py_decorator_types[] = {"decorator",NULL};

// ==================== JAVASCRIPT ====================
static const char* js_func_types[] = {"function_declaration","generator_function_declaration","function_expression","arrow_function","method_definition",NULL};
static const char* js_class_types[] = {"class_declaration","class",NULL};
static const char* js_module_types[] = {"program",NULL};
static const char* js_call_types[] = {"call_expression",NULL};
static const char* js_import_types[] = {"import_statement","lexical_declaration","export_statement",NULL};
static const char* js_branch_types[] = {"if_statement","for_statement","for_in_statement","while_statement","switch_statement","case_clause","try_statement","catch_clause",NULL};
static const char* js_var_types[] = {"lexical_declaration","variable_declaration",NULL};
static const char* js_throw_types[] = {"throw_statement",NULL};

// ==================== TYPESCRIPT ====================
static const char* ts_func_types[] = {"function_declaration","generator_function_declaration","function_expression","arrow_function","method_definition","method_signature","abstract_method_signature","function_signature",NULL};
static const char* ts_class_types[] = {"class_declaration","class","abstract_class_declaration","enum_declaration","interface_declaration","type_alias_declaration","internal_module",NULL};
static const char* ts_decorator_types[] = {"decorator",NULL};

// ==================== RUST ====================
static const char* rust_func_types[] = {"function_item","function_signature_item","closure_expression",NULL};
static const char* rust_class_types[] = {"struct_item","enum_item","union_item","trait_item","type_item",NULL};
static const char* rust_field_types[] = {"field_declaration",NULL};
static const char* rust_module_types[] = {"source_file","mod_item",NULL};
static const char* rust_call_types[] = {"call_expression","macro_invocation",NULL};
static const char* rust_import_types[] = {"use_declaration","extern_crate_declaration",NULL};
static const char* rust_import_from_types[] = {"use_declaration",NULL};
static const char* rust_branch_types[] = {"if_expression","for_expression","while_expression","loop_expression","match_expression","match_arm",NULL};
static const char* rust_var_types[] = {"static_item","const_item",NULL};
static const char* rust_assign_types[] = {"assignment_expression","compound_assignment_expr",NULL};
static const char* rust_decorator_types[] = {"attribute_item",NULL};

// ==================== JAVA ====================
static const char* java_func_types[] = {"method_declaration","constructor_declaration",NULL};
static const char* java_class_types[] = {"class_declaration","interface_declaration","enum_declaration","annotation_type_declaration","record_declaration",NULL};
static const char* java_field_types[] = {"field_declaration",NULL};
static const char* java_module_types[] = {"program",NULL};
static const char* java_call_types[] = {"method_invocation",NULL};
static const char* java_import_types[] = {"import_declaration",NULL};
static const char* java_branch_types[] = {"if_statement","for_statement","enhanced_for_statement","while_statement","switch_expression","switch_block_statement_group","try_statement","catch_clause",NULL};
static const char* java_var_types[] = {"field_declaration","local_variable_declaration",NULL};
static const char* java_assign_types[] = {"assignment_expression",NULL};
static const char* java_throw_types[] = {"throw_statement",NULL};
static const char* java_decorator_types[] = {"marker_annotation","annotation",NULL};

// ==================== C++ ====================
static const char* cpp_func_types[] = {"function_definition","declaration","field_declaration","template_declaration","lambda_expression",NULL};
static const char* cpp_class_types[] = {"class_specifier","struct_specifier","union_specifier","enum_specifier",NULL};
static const char* cpp_field_types[] = {"field_declaration",NULL};
static const char* cpp_module_types[] = {"translation_unit","namespace_definition","linkage_specification","declaration",NULL};
static const char* cpp_call_types[] = {"call_expression","field_expression","subscript_expression","new_expression","delete_expression","binary_expression","unary_expression","update_expression",NULL};
static const char* cpp_import_types[] = {"preproc_include","template_function","declaration",NULL};
static const char* cpp_branch_types[] = {"if_statement","for_statement","for_range_loop","while_statement","switch_statement","case_statement","try_statement","catch_clause",NULL};
static const char* cpp_var_types[] = {"declaration",NULL};
static const char* cpp_assign_types[] = {"assignment_expression",NULL};
static const char* cpp_throw_types[] = {"throw_statement",NULL};

// ==================== C ====================
static const char* c_func_types[] = {"function_definition",NULL};
static const char* c_class_types[] = {"struct_specifier","enum_specifier","union_specifier",NULL};
static const char* c_field_types[] = {"field_declaration",NULL};
static const char* c_module_types[] = {"translation_unit",NULL};
static const char* c_call_types[] = {"call_expression",NULL};
static const char* c_import_types[] = {"preproc_include",NULL};
static const char* c_branch_types[] = {"if_statement","for_statement","while_statement","do_statement","switch_statement","case_statement",NULL};
static const char* c_var_types[] = {"declaration",NULL};
static const char* c_assign_types[] = {"assignment_expression",NULL};

// ==================== BASH ====================
static const char* ps_func_types[] = {"function_statement","class_method_definition",NULL};
static const char* ps_class_types[] = {"class_statement",NULL};
static const char* ps_module_types[] = {"program",NULL};
static const char* ps_call_types[] = {"command","invokation_expression",NULL};
static const char* ps_import_types[] = {"command",NULL};
static const char* ps_branch_types[] = {"if_statement","while_statement","for_statement","foreach_statement","switch_statement","do_statement","trap_statement",NULL};
static const char* ps_var_types[] = {"assignment_expression",NULL};
static const char* bash_func_types[] = {"function_definition",NULL};
static const char* bash_module_types[] = {"program",NULL};
static const char* bash_call_types[] = {"command",NULL};
static const char* bash_import_types[] = {"command",NULL};
static const char* bash_branch_types[] = {"if_statement","while_statement","for_statement","case_statement","elif_clause",NULL};
static const char* bash_var_types[] = {"variable_assignment",NULL};

// ==================== HTML ====================
static const char* html_module_types[] = {"document",NULL};

// ==================== CSS ====================
static const char* css_module_types[] = {"stylesheet",NULL};
static const char* css_import_types[] = {"import_statement",NULL};

// ==================== SCSS ====================
static const char* scss_func_types[] = {"mixin_statement","function_statement",NULL};
static const char* scss_module_types[] = {"stylesheet",NULL};
static const char* scss_call_types[] = {"call_expression",NULL};
static const char* scss_import_types[] = {"import_statement","use_statement",NULL};
static const char* scss_branch_types[] = {"if_statement",NULL};
static const char* scss_var_types[] = {"declaration",NULL};

// ==================== YAML ====================
static const char* yaml_module_types[] = {"stream",NULL};
static const char* yaml_var_types[] = {"block_mapping_pair",NULL};

// ==================== TOML ====================
static const char* toml_module_types[] = {"document",NULL};
static const char* toml_class_types[] = {"table","table_array_element",NULL};
static const char* toml_var_types[] = {"pair",NULL};

// ==================== HCL ====================
static const char* hcl_class_types[] = {"block",NULL};
static const char* hcl_module_types[] = {"config_file",NULL};
static const char* hcl_call_types[] = {"function_call",NULL};
static const char* hcl_var_types[] = {"attribute",NULL};

// ==================== SQL ====================
static const char* sql_func_types[] = {"create_function",NULL};
static const char* sql_field_types[] = {"column_definition",NULL};
static const char* sql_module_types[] = {"program",NULL};
static const char* sql_call_types[] = {"function_call","invocation",NULL};
static const char* sql_branch_types[] = {"if_statement","case_expression",NULL};
static const char* sql_var_types[] = {"create_table","create_view",NULL};

// ==================== DOCKERFILE ====================
static const char* dockerfile_module_types[] = {"source_file",NULL};
static const char* dockerfile_var_types[] = {"env_instruction","arg_instruction",NULL};

// ==================== ENV ACCESS ====================
static const char* go_env_funcs[] = {"os.Getenv","os.LookupEnv",NULL};
static const char* py_env_funcs[] = {"os.getenv","os.environ.get",NULL};
static const char* py_env_members[] = {"os.environ",NULL};
static const char* js_env_members[] = {"process.env",NULL};
static const char* ts_env_members[] = {"process.env",NULL};
static const char* rust_env_funcs[] = {"env::var","std::env::var",NULL};
static const char* java_env_funcs[] = {"System.getenv","System.getProperty",NULL};
static const char* cpp_env_funcs[] = {"getenv","std::getenv",NULL};
static const char* c_env_funcs[] = {"getenv",NULL};

// ==================== NIX ====================
static const char* nix_func_types[] = {"function_expression",NULL};
static const char* nix_module_types[] = {"source_expression",NULL};
static const char* nix_call_types[] = {"apply_expression",NULL};
static const char* nix_branch_types[] = {"if_expression",NULL};
static const char* nix_var_types[] = {"binding",NULL};

// ==================== CUDA ====================
// CUDA extends C++, reuse cpp types (same grammar family)

// ==================== JSON ====================
static const char* json_module_types[] = {"document",NULL};
static const char* json_var_types[] = {"pair",NULL};

// ==================== XML ====================
static const char* xml_module_types[] = {"document",NULL};
static const char* xml_class_types[] = {"element",NULL};

// ==================== MARKDOWN ====================
static const char* markdown_module_types[] = {"document",NULL};
static const char* markdown_class_types[] = {"atx_heading","setext_heading",NULL};

// ==================== MAKEFILE ====================
static const char* makefile_func_types[] = {"rule",NULL};
static const char* makefile_module_types[] = {"makefile",NULL};
static const char* makefile_call_types[] = {"function_call",NULL};
static const char* makefile_import_types[] = {"include_directive",NULL};
static const char* makefile_var_types[] = {"variable_assignment",NULL};

// ==================== CMAKE ====================
static const char* cmake_module_types[] = {"source_file",NULL};
static const char* cmake_call_types[] = {"normal_command",NULL};

// ==================== PROTOBUF ====================
static const char* protobuf_class_types[] = {"message","enum",NULL};
static const char* protobuf_module_types[] = {"source_file",NULL};
static const char* protobuf_field_types[] = {"field","map_field","oneof_field",NULL};
static const char* protobuf_import_types[] = {"import",NULL};

// ==================== NEW LANG ENV ACCESS ====================
static const char* nix_env_funcs[] = {"builtins.getEnv",NULL};

// ==================== SPEC TABLE ====================

static const CBMLangSpec lang_specs[CBM_LANG_COUNT] = {
    // CBM_LANG_GO
    {CBM_LANG_GO, go_func_types, go_class_types, go_field_types, go_module_types, go_call_types,
     go_import_types, go_import_types, go_branch_types, go_var_types, go_assign_types,
     empty_types, NULL, empty_types, go_env_funcs, NULL},

    // CBM_LANG_PYTHON
    {CBM_LANG_PYTHON, py_func_types, py_class_types, empty_types, py_module_types, py_call_types,
     py_import_types, py_import_from_types, py_branch_types, py_var_types, py_var_types,
     py_throw_types, NULL, py_decorator_types, py_env_funcs, py_env_members},

    // CBM_LANG_JAVASCRIPT
    {CBM_LANG_JAVASCRIPT, js_func_types, js_class_types, empty_types, js_module_types, js_call_types,
     js_import_types, js_import_types, js_branch_types, js_var_types, (const char*[]){"assignment_expression","augmented_assignment_expression",NULL},
     js_throw_types, NULL, empty_types, NULL, js_env_members},

    // CBM_LANG_TYPESCRIPT
    {CBM_LANG_TYPESCRIPT, ts_func_types, ts_class_types, empty_types, js_module_types, js_call_types,
     js_import_types, js_import_types, js_branch_types, js_var_types, (const char*[]){"assignment_expression","augmented_assignment_expression",NULL},
     js_throw_types, NULL, ts_decorator_types, NULL, ts_env_members},

    // CBM_LANG_TSX
    {CBM_LANG_TSX, ts_func_types, ts_class_types, empty_types, js_module_types, js_call_types,
     js_import_types, js_import_types, js_branch_types, js_var_types, (const char*[]){"assignment_expression","augmented_assignment_expression",NULL},
     js_throw_types, NULL, ts_decorator_types, NULL, ts_env_members},

    // CBM_LANG_RUST
    {CBM_LANG_RUST, rust_func_types, rust_class_types, rust_field_types, rust_module_types, rust_call_types,
     rust_import_types, rust_import_from_types, rust_branch_types, rust_var_types, rust_assign_types,
     empty_types, NULL, rust_decorator_types, rust_env_funcs, NULL},

    // CBM_LANG_JAVA
    {CBM_LANG_JAVA, java_func_types, java_class_types, java_field_types, java_module_types, java_call_types,
     java_import_types, java_import_types, java_branch_types, java_var_types, java_assign_types,
     java_throw_types, "throws", java_decorator_types, java_env_funcs, NULL},

    // CBM_LANG_CPP
    {CBM_LANG_CPP, cpp_func_types, cpp_class_types, cpp_field_types, cpp_module_types, cpp_call_types,
     cpp_import_types, cpp_import_types, cpp_branch_types, cpp_var_types, cpp_assign_types,
     cpp_throw_types, NULL, empty_types, cpp_env_funcs, NULL},

    // CBM_LANG_C
    {CBM_LANG_C, c_func_types, c_class_types, c_field_types, c_module_types, c_call_types,
     c_import_types, empty_types, c_branch_types, c_var_types, c_assign_types,
     empty_types, NULL, empty_types, c_env_funcs, NULL},

    // CBM_LANG_BASH
    {CBM_LANG_BASH, bash_func_types, empty_types, empty_types, bash_module_types, bash_call_types,
     bash_import_types, empty_types, bash_branch_types, bash_var_types, bash_var_types,
     empty_types, NULL, empty_types, NULL, NULL},

    // CBM_LANG_POWERSHELL
    {CBM_LANG_POWERSHELL, ps_func_types, ps_class_types, empty_types, ps_module_types, ps_call_types,
     ps_import_types, empty_types, ps_branch_types, ps_var_types, ps_var_types,
     empty_types, NULL, empty_types, NULL, NULL},

    // CBM_LANG_HTML
    {CBM_LANG_HTML, empty_types, empty_types, empty_types, html_module_types, empty_types,
     empty_types, empty_types, empty_types, empty_types, empty_types,
     empty_types, NULL, empty_types, NULL, NULL},

    // CBM_LANG_CSS
    {CBM_LANG_CSS, empty_types, empty_types, empty_types, css_module_types, empty_types,
     css_import_types, empty_types, empty_types, empty_types, empty_types,
     empty_types, NULL, empty_types, NULL, NULL},

    // CBM_LANG_SCSS
    {CBM_LANG_SCSS, scss_func_types, empty_types, empty_types, scss_module_types, scss_call_types,
     scss_import_types, empty_types, scss_branch_types, scss_var_types, empty_types,
     empty_types, NULL, empty_types, NULL, NULL},

    // CBM_LANG_YAML
    {CBM_LANG_YAML, empty_types, empty_types, empty_types, yaml_module_types, empty_types,
     empty_types, empty_types, empty_types, yaml_var_types, empty_types,
     empty_types, NULL, empty_types, NULL, NULL},

    // CBM_LANG_TOML
    {CBM_LANG_TOML, empty_types, toml_class_types, empty_types, toml_module_types, empty_types,
     empty_types, empty_types, empty_types, toml_var_types, empty_types,
     empty_types, NULL, empty_types, NULL, NULL},

    // CBM_LANG_HCL
    {CBM_LANG_HCL, empty_types, hcl_class_types, empty_types, hcl_module_types, hcl_call_types,
     empty_types, empty_types, empty_types, hcl_var_types, empty_types,
     empty_types, NULL, empty_types, NULL, NULL},

    // CBM_LANG_SQL
    {CBM_LANG_SQL, sql_func_types, empty_types, sql_field_types, sql_module_types, sql_call_types,
     empty_types, empty_types, sql_branch_types, sql_var_types, empty_types,
     empty_types, NULL, empty_types, NULL, NULL},

    // CBM_LANG_DOCKERFILE
    {CBM_LANG_DOCKERFILE, empty_types, empty_types, empty_types, dockerfile_module_types, empty_types,
     empty_types, empty_types, empty_types, dockerfile_var_types, empty_types,
     empty_types, NULL, empty_types, NULL, NULL},

    // CBM_LANG_NIX
    {CBM_LANG_NIX, nix_func_types, empty_types, empty_types, nix_module_types, nix_call_types,
     empty_types, empty_types, nix_branch_types, nix_var_types, nix_var_types,
     empty_types, NULL, empty_types, nix_env_funcs, NULL},

    // CBM_LANG_CUDA (reuses C++ node types)
    {CBM_LANG_CUDA, cpp_func_types, cpp_class_types, cpp_field_types, cpp_module_types, cpp_call_types,
     cpp_import_types, cpp_import_types, cpp_branch_types, cpp_var_types, cpp_assign_types,
     cpp_throw_types, NULL, empty_types, cpp_env_funcs, NULL},

    // CBM_LANG_JSON
    {CBM_LANG_JSON, empty_types, empty_types, empty_types, json_module_types, empty_types,
     empty_types, empty_types, empty_types, json_var_types, empty_types,
     empty_types, NULL, empty_types, NULL, NULL},

    // CBM_LANG_XML
    {CBM_LANG_XML, empty_types, xml_class_types, empty_types, xml_module_types, empty_types,
     empty_types, empty_types, empty_types, empty_types, empty_types,
     empty_types, NULL, empty_types, NULL, NULL},

    // CBM_LANG_MARKDOWN
    {CBM_LANG_MARKDOWN, empty_types, markdown_class_types, empty_types, markdown_module_types, empty_types,
     empty_types, empty_types, empty_types, empty_types, empty_types,
     empty_types, NULL, empty_types, NULL, NULL},

    // CBM_LANG_MAKEFILE
    {CBM_LANG_MAKEFILE, makefile_func_types, empty_types, empty_types, makefile_module_types, makefile_call_types,
     makefile_import_types, empty_types, empty_types, makefile_var_types, empty_types,
     empty_types, NULL, empty_types, NULL, NULL},

    // CBM_LANG_CMAKE
    {CBM_LANG_CMAKE, empty_types, empty_types, empty_types, cmake_module_types, cmake_call_types,
     empty_types, empty_types, empty_types, empty_types, empty_types,
     empty_types, NULL, empty_types, NULL, NULL},

    // CBM_LANG_PROTOBUF
    {CBM_LANG_PROTOBUF, empty_types, protobuf_class_types, protobuf_field_types, protobuf_module_types, empty_types,
     protobuf_import_types, empty_types, empty_types, empty_types, empty_types,
     empty_types, NULL, empty_types, NULL, NULL},
};

const CBMLangSpec* cbm_lang_spec(CBMLanguage lang) {
    if (lang < 0 || lang >= CBM_LANG_COUNT) return NULL;
    return &lang_specs[lang];
}

const TSLanguage* cbm_ts_language(CBMLanguage lang) {
    switch (lang) {
        case CBM_LANG_GO:         return tree_sitter_go();
        case CBM_LANG_PYTHON:     return tree_sitter_python();
        case CBM_LANG_JAVASCRIPT: return tree_sitter_javascript();
        case CBM_LANG_TYPESCRIPT: return tree_sitter_typescript();
        case CBM_LANG_TSX:        return tree_sitter_tsx();
        case CBM_LANG_RUST:       return tree_sitter_rust();
        case CBM_LANG_JAVA:       return tree_sitter_java();
        case CBM_LANG_CPP:        return tree_sitter_cpp();
        case CBM_LANG_C:          return tree_sitter_c();
        case CBM_LANG_BASH:       return tree_sitter_bash();
        case CBM_LANG_POWERSHELL: return tree_sitter_powershell();
        case CBM_LANG_HTML:       return tree_sitter_html();
        case CBM_LANG_CSS:        return tree_sitter_css();
        case CBM_LANG_SCSS:       return tree_sitter_scss();
        case CBM_LANG_YAML:       return tree_sitter_yaml();
        case CBM_LANG_TOML:       return tree_sitter_toml();
        case CBM_LANG_HCL:        return tree_sitter_hcl();
        case CBM_LANG_SQL:        return tree_sitter_sql();
        case CBM_LANG_DOCKERFILE: return tree_sitter_dockerfile();
        case CBM_LANG_NIX:        return tree_sitter_nix();
        case CBM_LANG_CUDA:       return tree_sitter_cuda();
        case CBM_LANG_JSON:       return tree_sitter_json();
        case CBM_LANG_XML:        return tree_sitter_xml();
        case CBM_LANG_MARKDOWN:   return tree_sitter_markdown();
        case CBM_LANG_MAKEFILE:   return tree_sitter_make();
        case CBM_LANG_CMAKE:      return tree_sitter_cmake();
        case CBM_LANG_PROTOBUF:   return tree_sitter_proto();
        default:                  return NULL;
    }
}
