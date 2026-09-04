# Third-Party Notices

code-graph is a fork of [DeusData/codebase-memory-mcp](https://github.com/DeusData/codebase-memory-mcp) (MIT, (c) 2025 DeusData); see `LICENSE`.
It statically links vendored third-party C sources. Each vendored directory carries the upstream `LICENSE` file alongside the sources.

## Tree-sitter runtime

- `internal/cbm/vendored/ts_runtime/` — [tree-sitter/tree-sitter](https://github.com/tree-sitter/tree-sitter), MIT, (c) 2018 Max Brunsfeld.

## Tree-sitter grammars

Pre-generated parsers (`parser.c`, plus `scanner.c` where applicable) under `internal/cbm/vendored/grammars/<name>/`.
Grammars were inherited from the upstream fork's vendoring; the pinned refs below are those recorded in upstream's grammar manifest
(`internal/cbm/vendored/grammars/MANIFEST.md` in DeusData/codebase-memory-mcp, captured 2026-06) and match the ABI version compiled here.
`powershell` was vendored directly by this fork at the commit shown.

| Grammar | Upstream | Pinned ref | ABI | License |
|---|---|---|---|---|
| bash | [tree-sitter/tree-sitter-bash](https://github.com/tree-sitter/tree-sitter-bash) | `a06c2e4415e9` | 15 | MIT |
| c | [tree-sitter/tree-sitter-c](https://github.com/tree-sitter/tree-sitter-c) | `ae19b676b13b` | 15 | MIT |
| cmake | [uyha/tree-sitter-cmake](https://github.com/uyha/tree-sitter-cmake) | `c7b2a71e7f8e` | 14 | MIT |
| cpp | [tree-sitter/tree-sitter-cpp](https://github.com/tree-sitter/tree-sitter-cpp) | `8b5b49eb196b` | 14 | MIT |
| css | [tree-sitter/tree-sitter-css](https://github.com/tree-sitter/tree-sitter-css) | `dda5cfc5722c` | 15 | MIT |
| cuda | [tree-sitter-grammars/tree-sitter-cuda](https://github.com/tree-sitter-grammars/tree-sitter-cuda) | `48b066f334f4` | 15 | MIT |
| dockerfile | [camdencheek/tree-sitter-dockerfile](https://github.com/camdencheek/tree-sitter-dockerfile) | `971acdd90856` | 14 | MIT |
| go | [tree-sitter/tree-sitter-go](https://github.com/tree-sitter/tree-sitter-go) | `2346a3ab1bb3` | 15 | MIT |
| hcl | [tree-sitter-grammars/tree-sitter-hcl](https://github.com/tree-sitter-grammars/tree-sitter-hcl) | `64ad62785d44` | 15 | Apache-2.0 |
| html | [tree-sitter/tree-sitter-html](https://github.com/tree-sitter/tree-sitter-html) | `73a3947324f6` | 14 | MIT |
| java | [tree-sitter/tree-sitter-java](https://github.com/tree-sitter/tree-sitter-java) | `e10607b45ff7` | 14 | MIT |
| javascript | [tree-sitter/tree-sitter-javascript](https://github.com/tree-sitter/tree-sitter-javascript) | `58404d8cf191` | 15 | MIT |
| json | [tree-sitter/tree-sitter-json](https://github.com/tree-sitter/tree-sitter-json) | `001c28d7a298` | 14 | MIT |
| make | [tree-sitter-grammars/tree-sitter-make](https://github.com/tree-sitter-grammars/tree-sitter-make) | `70613f3d812c` | 15 | MIT |
| markdown | [tree-sitter-grammars/tree-sitter-markdown](https://github.com/tree-sitter-grammars/tree-sitter-markdown) | `f969cd3ae3f9` | 15 | MIT |
| nix | [nix-community/tree-sitter-nix](https://github.com/nix-community/tree-sitter-nix) | `eabf96807ea4` | 13 | MIT |
| powershell | [airbus-cert/tree-sitter-powershell](https://github.com/airbus-cert/tree-sitter-powershell) | `d398441825243b00e317e87e1829b9d6a3e54ce0` | 15 | MIT |
| protobuf | [DeusData/codebase-memory-mcp](https://github.com/DeusData/codebase-memory-mcp) (first-party grammar) | `see upstream MANIFEST.md` | 13 | MIT |
| python | [tree-sitter/tree-sitter-python](https://github.com/tree-sitter/tree-sitter-python) | `v0.25.0` | 15 | MIT |
| rust | [tree-sitter/tree-sitter-rust](https://github.com/tree-sitter/tree-sitter-rust) | `77a3747266f4` | 15 | MIT |
| scss | [serenadeai/tree-sitter-scss](https://github.com/serenadeai/tree-sitter-scss) | `c478c6868648` | 14 | MIT |
| sql | [DerekStride/tree-sitter-sql](https://github.com/DerekStride/tree-sitter-sql) | `851e9cb257ba` | 15 | MIT |
| toml | [tree-sitter-grammars/tree-sitter-toml](https://github.com/tree-sitter-grammars/tree-sitter-toml) | `64b56832c2cf` | 14 | MIT |
| tsx | [tree-sitter/tree-sitter-typescript](https://github.com/tree-sitter/tree-sitter-typescript) | `75b3874edb2d` | 14 | MIT |
| typescript | [tree-sitter/tree-sitter-typescript](https://github.com/tree-sitter/tree-sitter-typescript) | `75b3874edb2d` | 14 | MIT |
| xml | [tree-sitter-grammars/tree-sitter-xml](https://github.com/tree-sitter-grammars/tree-sitter-xml) | `5000ae8f22d1` | 14 | MIT |
| yaml | [tree-sitter-grammars/tree-sitter-yaml](https://github.com/tree-sitter-grammars/tree-sitter-yaml) | `4463985dfccc` | 14 | MIT |
| lua | [tree-sitter-grammars/tree-sitter-lua](https://github.com/tree-sitter-grammars/tree-sitter-lua) | `10fe0054734e` | 15 | MIT |
| vue | [tree-sitter-grammars/tree-sitter-vue](https://github.com/tree-sitter-grammars/tree-sitter-vue) | `ce8011a414fd` | 15 | MIT |
| svelte | [tree-sitter-grammars/tree-sitter-svelte](https://github.com/tree-sitter-grammars/tree-sitter-svelte) | `ae5199db4775` | 14 | MIT |
| graphql | [bkegley/tree-sitter-graphql](https://github.com/bkegley/tree-sitter-graphql) | `5e66e961eee4` | 13 | MIT |
| gomod | [camdencheek/tree-sitter-go-mod](https://github.com/camdencheek/tree-sitter-go-mod) | `2e886870578e` | 15 | MIT |
| erlang | [WhatsApp/tree-sitter-erlang](https://github.com/WhatsApp/tree-sitter-erlang) | `1d78195c4fbb` | 14 | Apache-2.0 |
| clojure | [sogaiu/tree-sitter-clojure](https://github.com/sogaiu/tree-sitter-clojure) | `e43eff80d17c` | 14 | CC0-1.0 |

Refer to each directory's `LICENSE` for the full text and copyright holder.

Grammars vendored from upstream codebase-memory-mcp's manifest in 0.9.1 (lua, vue, svelte, graphql, gomod, erlang, clojure) were copied at the commits that manifest pins; `scripts/vendor-grammar-from-manifest.sh` reproduces that step. tree-sitter-erlang is Apache-2.0 and tree-sitter-clojure is CC0-1.0; both are compatible with this project's MIT license and their LICENSE files ship beside the grammar sources.
