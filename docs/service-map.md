# Service map configuration

`service_map` and `diff_services` group the top-level crates and packages of a
repository into domains such as `service`, `library`, or `infrastructure`.
Service naming conventions are organization-specific, so the grouping table is
a plain JSON file you can replace.

## Resolution order

1. `CODE_GRAPH_SERVICE_MAP=/path/to/service_map.json`
2. `<user config dir>/code-graph/service_map.json`, i.e.
   `~/.config/code-graph/service_map.json` on Linux,
   `~/Library/Application Support/code-graph/service_map.json` on macOS,
   `%AppData%\code-graph\service_map.json` on Windows
3. The built-in generic default

A configured file replaces the default table entirely. An unreadable or
invalid file is logged and the default is used, so a bad config never breaks
indexing.

## File format

```json
{
  "navigation":   ["nav*", "*-gps", "compassd"],
  "perception":   ["tracker*", "*-vision"],
  "communications": ["telem-*", "*-bridge"],
  "infrastructure": ["terraform", "nix", "deploy*"]
}
```

Each key is a domain name and each value is a list of patterns matched against
the lower-cased crate or package name:

| Pattern | Matches |
|---|---|
| `name` | exactly `name`, or any name starting with `name` |
| `name*` | any name starting with `name` |
| `*suffix` | any name ending in `suffix` (but not `suffix` alone) |
| `*part*` | any name containing `part` |

When several patterns match, the longest pattern wins; ties break on domain
name so classification is deterministic. Names that match nothing are
`library` if they start with `lib`, otherwise `other`.

## Default table

```json
{
  "library":        ["lib*", "*-lib", "*-sdk", "*-client"],
  "service":        ["*d", "*daemon", "*-service", "*-server", "*-api", "*-worker"],
  "tooling":        ["*ctl", "*-cli", "*cli", "*-tool", "*-tools", "scripts"],
  "testing":        ["test*", "*-test", "*_test", "*-tests", "e2e*", "integration*"],
  "infrastructure": ["terraform", "infra*", "nix", "docker", "k8s", "kubernetes", "helm", "ansible", "deploy*"],
  "ui":             ["web", "web-*", "ui", "ui-*", "frontend", "*-frontend", "*-ui", "*-web"],
  "data":           ["data*", "*-data", "ml-*", "*-ml", "etl*", "*-pipeline"]
}
```

## Related settings

Nix service extraction has two similar knobs for organization-specific module
conventions: `CODE_GRAPH_NIX_SERVICE_OPTION_PREFIX` (default `services`, the
`options.<prefix>.<name>` path service modules declare under) and
`CODE_GRAPH_NIX_PKGS_PREFIX` (default `pkgs`, the package set referenced as
`${<prefix>.<pkg>}/bin/<binary>` in service scripts).
