package pipeline

// SyntheticInterfaceRegistry — curated map of well-known external traits
// (std + tier-1 deps) that PSM and similar Rust codebases reference but
// which aren't in the indexed graph as Interface nodes (because std/external
// crates aren't indexed).
//
// Phase A2 of plan #459 (`2026-05-08-external-crate-trait-registry.md`):
// when `resolveAsClassWithReason` returns ResolveEmpty AND PR #265's
// label-aware fallback also fails, we look up the trait name in this map.
// If found, return the synthetic QN with the new ResolveOKViaFallbackFromExternal
// reason — emit IMPLEMENTS with a synthetic Interface target instead of
// dropping the impl-block.
//
// The synthetic QN uses an `_external.<crate>.<trait>` namespace prefix to
// distinguish from real indexed nodes. The "_external" prefix is reserved
// for this purpose; no real Rust code can produce a node with this prefix.
//
// Curated list comes from PR #265's 20-trait sample on PSM (lines 36-50 of
// the D-Implement baseline file): 17 of 20 common-trait-names are stdlib
// + serde + tokio prelude, NOT in PSM as Interface nodes. This map covers
// those 17 plus the broader stdlib/tier-1 prelude inferred from idiomatic
// Rust usage.
//
// Substrate evidence: PR #459 baseline projects ~85% of 722 PSM
// resolve-empty cases are external traits. Conservative coverage by this
// curated 50-trait list: 70-85% of external substrate, recoverable max
// 215-261 IMPLEMENTS lift. Bootstrap source: PR #460 A1 outcome.

// syntheticTraits maps trait NAMES (as seen in `impl Trait for Struct`) to
// synthetic Interface QNs. The QN format `_external.<crate>.<trait>` lets
// future external-crate registries extend with crate-specific entries (e.g.
// _external.tokio_io.AsyncRead distinct from _external.std_io.AsyncRead).
var syntheticTraits = map[string]string{
	// std::convert
	"From":    "_external.std.convert.From",
	"TryFrom": "_external.std.convert.TryFrom",
	"Into":    "_external.std.convert.Into",
	"TryInto": "_external.std.convert.TryInto",
	"AsRef":   "_external.std.convert.AsRef",
	"AsMut":   "_external.std.convert.AsMut",
	// std::fmt
	"Display": "_external.std.fmt.Display",
	"Debug":   "_external.std.fmt.Debug",
	"Write":   "_external.std.fmt.Write",
	// std::ops
	"Drop":     "_external.std.ops.Drop",
	"Deref":    "_external.std.ops.Deref",
	"DerefMut": "_external.std.ops.DerefMut",
	"Add":      "_external.std.ops.Add",
	"Sub":      "_external.std.ops.Sub",
	"Mul":      "_external.std.ops.Mul",
	"Div":      "_external.std.ops.Div",
	"Index":    "_external.std.ops.Index",
	"IndexMut": "_external.std.ops.IndexMut",
	// std::cmp
	"PartialEq":  "_external.std.cmp.PartialEq",
	"Eq":         "_external.std.cmp.Eq",
	"PartialOrd": "_external.std.cmp.PartialOrd",
	"Ord":        "_external.std.cmp.Ord",
	"Hash":       "_external.std.hash.Hash",
	// std::clone, std::default
	"Clone":   "_external.std.clone.Clone",
	"Default": "_external.std.default.Default",
	// std::iter
	"Iterator":            "_external.std.iter.Iterator",
	"IntoIterator":        "_external.std.iter.IntoIterator",
	"FromIterator":        "_external.std.iter.FromIterator",
	"ExactSizeIterator":   "_external.std.iter.ExactSizeIterator",
	"DoubleEndedIterator": "_external.std.iter.DoubleEndedIterator",
	// std::marker
	"Send":  "_external.std.marker.Send",
	"Sync":  "_external.std.marker.Sync",
	"Sized": "_external.std.marker.Sized",
	"Copy":  "_external.std.marker.Copy",
	"Unpin": "_external.std.marker.Unpin",
	// std::error, std::str, std::future, std::any
	"Error":      "_external.std.error.Error",
	"FromStr":    "_external.std.str.FromStr",
	"Future":     "_external.std.future.Future",
	"IntoFuture": "_external.std.future.IntoFuture",
	"Any":        "_external.std.any.Any",
	// std::borrow
	"Borrow":    "_external.std.borrow.Borrow",
	"BorrowMut": "_external.std.borrow.BorrowMut",
	"ToOwned":   "_external.std.borrow.ToOwned",
	// serde
	"Serialize":   "_external.serde.Serialize",
	"Deserialize": "_external.serde.Deserialize",
	// tokio
	"AsyncRead":     "_external.tokio.io.AsyncRead",
	"AsyncWrite":    "_external.tokio.io.AsyncWrite",
	"AsyncReadExt":  "_external.tokio.io.AsyncReadExt",
	"AsyncWriteExt": "_external.tokio.io.AsyncWriteExt",
	// futures
	"Stream":    "_external.futures.Stream",
	"StreamExt": "_external.futures.StreamExt",
	// clap
	"Parser":     "_external.clap.Parser",
	"Subcommand": "_external.clap.Subcommand",
	"ValueEnum":  "_external.clap.ValueEnum",
	"Args":       "_external.clap.Args",
}

// lookupSyntheticTrait returns the synthetic QN for a curated external
// trait name, or empty string + false if the name isn't in the registry.
//
// Called by resolveAsClassWithReason as the 11th resolution strategy,
// after registry.Resolve and PR #265's label-aware project-wide fallback
// have both failed. Restricted to the resolveAsClassWithReason path
// (IMPLEMENTS only) — the generic resolver path used by CALLS does NOT
// consult this map, preserving CALLS resolution behavior.
//
// Strict name match: "From" matches but "from" or "FromIterator" does NOT
// match "From" (FromIterator has its own entry).
func lookupSyntheticTrait(name string) (string, bool) {
	qn, ok := syntheticTraits[name]
	return qn, ok
}
