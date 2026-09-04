//go:build !cbm_all

// Default builds exclude the CUDA grammar (its parser table is the largest
// vendored grammar). This stub keeps the symbol resolvable; cbm_ts_language
// returns NULL for CBM_LANG_CUDA and extraction reports the language as not
// compiled in. Build with `-tags cbm_all` (make build-all) to include it.
#include "lang_specs.h"

const TSLanguage* tree_sitter_cuda(void) { return NULL; }
