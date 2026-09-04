//go:build !cbm_all

package lang

// optionalGrammarsEnabled reports whether grammars behind the cbm_all build
// tag are compiled into this binary. Default builds leave them out to keep
// the artifact small; `make build-all` includes them.
const optionalGrammarsEnabled = false
