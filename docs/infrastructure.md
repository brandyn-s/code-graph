# Infrastructure files

The indexer's infrastructure pass (`internal/pipeline/infrascan*.go`) walks the
repository for configuration that describes how the code is built, deployed and
exposed, and stores each finding as an `InfraFile` node (plus a `File` node so
`search_code` can open it). `InfraFile` nodes carry an `infra_type` property and
type-specific properties, and are reachable through `query_graph`.

| `infra_type` | Source | Node name | Notable properties |
|---|---|---|---|
| `dockerfile` | `Dockerfile*` | file name | `base_image(s)`, `exposed_ports`, `env_vars`, `user`, `cmd` |
| `compose-service` | `docker-compose*.yml`, `compose.yml` | file name (one node per service) | `service_name`, `image`, `ports`, `environment`, `depends_on` |
| `cloudbuild` | `cloudbuild*.yaml` | file name | build steps and images |
| `env` | `.env*` | file name | `env_vars` (values of secret-looking keys are dropped) |
| `terraform` | `*.tf` | file name | resources, variables, providers |
| `shell` | `*.sh` | file name | environment assignments, docker commands |
| `nix-input` | `flake.lock` | file name (one node per input) | `input_name`, `DEPENDS_ON` edges |
| `k8s-resource` | any `*.yaml` / `*.yml` with `apiVersion`, `kind`, `metadata.name` | `Kind/name` (one node per document) | see below |
| `k8s-kustomization` | `kustomization.yaml` | `Kustomization` | `resources` |

## Kubernetes manifests

Every YAML file that is not a compose or cloudbuild file is probed for
Kubernetes documents. Multi-document files yield one node per document, with
qualified name `<file>::Kind/name` (or `Kind/namespace/name`). Unrendered Helm
templates (files containing `{{`) and YAML that merely mentions the words
`apiVersion`/`kind` without being a manifest are skipped.

Properties recorded per resource (only when present):

- Identity: `api_version`, `kind`, `name`, `namespace`.
- Workloads (Pod, Deployment, StatefulSet, DaemonSet, ReplicaSet, Job, CronJob
  and anything else with a pod template): `containers`, `images`,
  `container_ports`, `host_ports`, `service_account`,
  `automount_service_account_token`, `host_network`, `host_pid`, `host_ipc`,
  `privileged`, `allow_privilege_escalation`, `runs_as_root`,
  `capabilities_add`, `secret_refs`, `configmap_refs`, `host_paths`.
- Service: `service_type`, `service_ports` (`port[:nodePort=N][/PROTO]`),
  `external_ips`.
- Ingress and Gateway API routes: `ingress_hosts`, `ingress_paths`,
  `backend_services`, `tls`, `listeners`.
- NetworkPolicy: `policy_types`, `ingress_rules`, `egress_rules`.
- Role / ClusterRole: `rbac_rules` (one `apiGroups=... resources=... verbs=...`
  string per rule). RoleBinding / ClusterRoleBinding: `role_ref`, `subjects`.
- Secret: `secret_type` and `secret_keys`. Secret **values** are never stored.

### Security surfaces

Resources that open or guard an attack surface are tagged with the same
`security_role` / `security_subtype` properties that `passSecurityTags` puts
on code, so `query_security_surfaces` lists them alongside functions. A
resource gets one role (most severe first) and a `security_signals` list with
every reason:

| `security_role` | `security_signals` (prefixed `k8s_`) |
|---|---|
| `privilege_escalation` | `privileged_container`, `allow_privilege_escalation`, `runs_as_root`, `host_pid`, `host_ipc`, `host_path_volume`, `dangerous_capability` (ALL, SYS_ADMIN, SYS_PTRACE, SYS_MODULE, NET_ADMIN, SYS_RAWIO, DAC_READ_SEARCH, SYS_BOOT), `wildcard_rbac_rule`, `secrets_read_rbac`, `pod_exec_rbac`, `rbac_escalate_verb`, `admin_role_binding`, `broad_subject_binding` |
| `input_entry_point` | `ingress`, `gateway_route`, `external_service` (LoadBalancer, NodePort, externalIPs), `host_network`, `host_port` |
| `auth_boundary` | `rbac_role`, `rbac_binding`, `network_policy`, `service_account`, `admission_control` |
| `sensitive_sink` | `secret_resource`, `secret_reference` (env, envFrom or volume references to a Secret) |

A hardened workload (no host namespaces, `runAsNonRoot`, no privilege, no
secret references) carries no `security_role` and does not appear in
`query_security_surfaces`. Manifests have no CALLS edges, so they never take
part in `tainted_paths`; they are evidence of exposure and privilege, not a
data-flow proof.

Fixture: `internal/pipeline/testdata/k8s/`, exercised by
`internal/pipeline/infrascan_k8s_test.go`.
