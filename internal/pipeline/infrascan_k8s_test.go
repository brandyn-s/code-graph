package pipeline

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/brandyn-s/code-graph/internal/discover"
	"github.com/brandyn-s/code-graph/internal/store"
)

func k8sFixture(t *testing.T, name string) []infraFile {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("testdata", "k8s", name))
	if err != nil {
		t.Fatal(err)
	}
	return parseKubernetesFile(abs, "deploy/"+name)
}

func byName(t *testing.T, files []infraFile, name string) infraFile {
	t.Helper()
	for _, f := range files {
		if f.name == name {
			return f
		}
	}
	t.Fatalf("resource %q not found in %d documents", name, len(files))
	return infraFile{}
}

func TestParseKubernetesWorkloadFlagsPrivilegeAndExposure(t *testing.T) {
	files := k8sFixture(t, "workload.yaml")
	if len(files) != 3 {
		t.Fatalf("documents = %d, want 3", len(files))
	}

	dep := byName(t, files, "Deployment/node-agent")
	p := dep.properties
	assertEqual(t, p["infra_type"], "k8s-resource")
	assertEqual(t, p["kind"], "Deployment")
	assertEqual(t, p["namespace"], "monitoring")
	assertEqual(t, p["security_role"], RolePrivilegeEscalation)
	assertEqual(t, p["security_subtype"], "k8s_host_pid")
	for _, sig := range []string{"k8s_host_pid", "k8s_privileged_container", "k8s_allow_privilege_escalation", "k8s_runs_as_root", "k8s_dangerous_capability", "k8s_host_path_volume", "k8s_host_network", "k8s_host_port", "k8s_secret_reference"} {
		assertSliceContains(t, p["security_signals"], sig)
	}
	assertSliceContains(t, p["images"], "registry.example.com/agent:1.4.2")
	assertSliceContains(t, p["images"], "busybox:1.36")
	assertSliceContains(t, p["container_ports"], "9100")
	assertSliceContains(t, p["container_ports"], "9101/UDP")
	assertSliceContains(t, p["host_ports"], "9100")
	assertSliceContains(t, p["secret_refs"], "agent-token")
	assertSliceContains(t, p["secret_refs"], "agent-tls")
	assertSliceContains(t, p["configmap_refs"], "agent-config")
	assertSliceContains(t, p["host_paths"], "/var/run/docker.sock")
	assertSliceContains(t, p["capabilities_add"], "SYS_ADMIN")
	assertEqual(t, p["service_account"], "node-agent")
	assertEqual(t, p["privileged"], true)
	assertEqual(t, p["host_network"], true)

	svc := byName(t, files, "Service/agent-lb")
	assertEqual(t, svc.properties["service_type"], "LoadBalancer")
	assertEqual(t, svc.properties["security_role"], RoleInputEntryPoint)
	assertEqual(t, svc.properties["security_subtype"], "k8s_external_service")
	assertSliceContains(t, svc.properties["service_ports"], "443")
	assertSliceContains(t, svc.properties["service_ports"], "30080:nodePort=30080")

	cron := byName(t, files, "CronJob/nightly")
	if _, tagged := cron.properties["security_role"]; tagged {
		t.Errorf("hardened CronJob must not be a security surface: %v", cron.properties["security_signals"])
	}
	assertSliceContains(t, cron.properties["images"], "alpine:3.19")
}

func TestParseKubernetesIngressIsAnEntryPoint(t *testing.T) {
	files := k8sFixture(t, "ingress.yaml")
	ing := byName(t, files, "Ingress/web")
	assertEqual(t, ing.properties["security_role"], RoleInputEntryPoint)
	assertEqual(t, ing.properties["security_subtype"], "k8s_ingress")
	assertSliceContains(t, ing.properties["ingress_hosts"], "app.example.com")
	assertSliceContains(t, ing.properties["ingress_paths"], "/api")
	assertSliceContains(t, ing.properties["backend_services"], "frontend")
	assertEqual(t, ing.properties["tls"], true)
}

func TestParseKubernetesRBACSeparatesBoundariesFromEscalation(t *testing.T) {
	files := k8sFixture(t, "rbac.yaml")
	if len(files) != 5 {
		t.Fatalf("documents = %d, want 5", len(files))
	}
	reader := byName(t, files, "ClusterRole/reader")
	assertEqual(t, reader.properties["security_role"], RoleAuthBoundary)
	assertEqual(t, reader.properties["security_subtype"], "k8s_rbac_role")
	assertSliceContains(t, reader.properties["rbac_rules"], "apiGroups= resources=pods,services verbs=get,list,watch")

	secrets := byName(t, files, "ClusterRole/secret-reader")
	assertEqual(t, secrets.properties["security_role"], RolePrivilegeEscalation)
	assertEqual(t, secrets.properties["security_subtype"], "k8s_secrets_read_rbac")

	binding := byName(t, files, "ClusterRoleBinding/everyone-admin")
	assertEqual(t, binding.properties["security_role"], RolePrivilegeEscalation)
	assertEqual(t, binding.properties["security_subtype"], "k8s_admin_role_binding")
	assertSliceContains(t, binding.properties["security_signals"], "k8s_broad_subject_binding")
	assertSliceContains(t, binding.properties["security_signals"], "k8s_rbac_binding")
	assertEqual(t, binding.properties["role_ref"], "ClusterRole/cluster-admin")
	assertSliceContains(t, binding.properties["subjects"], "ServiceAccount:ci/deployer")

	sa := byName(t, files, "ServiceAccount/node-agent")
	assertEqual(t, sa.properties["security_role"], RoleAuthBoundary)
	assertEqual(t, sa.properties["automount_service_account_token"], false)

	np := byName(t, files, "NetworkPolicy/default-deny")
	assertEqual(t, np.properties["security_subtype"], "k8s_network_policy")
	assertSliceContains(t, np.properties["policy_types"], "Egress")
}

func TestParseKubernetesSecretRecordsKeysNeverValues(t *testing.T) {
	files := k8sFixture(t, "secret.yaml")
	sec := byName(t, files, "Secret/agent-token")
	assertEqual(t, sec.properties["security_role"], RoleSensitiveSink)
	assertEqual(t, sec.properties["security_subtype"], "k8s_secret_resource")
	assertEqual(t, sec.properties["secret_type"], "Opaque")
	assertSliceContains(t, sec.properties["secret_keys"], "token")
	assertSliceContains(t, sec.properties["secret_keys"], "password")
	for k, v := range sec.properties {
		if s, ok := v.(string); ok && (s == "hunter2-do-not-index" || s == "c3VwZXItc2VjcmV0LXRva2Vu") {
			t.Fatalf("secret value leaked into property %q", k)
		}
	}
}

func TestParseKubernetesKustomizationAndNonManifests(t *testing.T) {
	files := k8sFixture(t, "kustomization.yaml")
	kz := byName(t, files, "Kustomization")
	assertEqual(t, kz.properties["infra_type"], "k8s-kustomization")
	assertSliceContains(t, kz.properties["resources"], "../base")

	if got := k8sFixture(t, "not-k8s.yaml"); len(got) != 0 {
		t.Errorf("GitHub workflow parsed as Kubernetes: %+v", got)
	}
	if got := k8sFixture(t, "helm-template.yaml"); len(got) != 0 {
		t.Errorf("unrendered Helm template parsed as Kubernetes: %+v", got)
	}
}

// The infra pass must produce nodes that query_security_surfaces will report:
// tagged with security_role, carrying a file path, and passing
// store.IsSurfaceableCodeNode.
func TestKubernetesManifestsFeedSecuritySurfaces(t *testing.T) {
	s, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	repo, err := filepath.Abs(filepath.Join("testdata", "k8s"))
	if err != nil {
		t.Fatal(err)
	}
	p := New(context.Background(), s, repo, discover.ModeFull)
	err = s.WithTransaction(context.Background(), func(tx *store.Store) error {
		p.Store = tx
		_ = tx.UpsertProject(p.ProjectName, repo)
		p.passInfraFiles()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		RolePrivilegeEscalation: "Deployment/node-agent",
		RoleInputEntryPoint:     "Ingress/web",
		RoleAuthBoundary:        "NetworkPolicy/default-deny",
		RoleSensitiveSink:       "Secret/agent-token",
	}
	for role, name := range want {
		nodes, err := s.FindNodesByProperty(p.ProjectName, "", "security_role", role)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, n := range nodes {
			if n.Name != name {
				continue
			}
			found = true
			if n.Label != "InfraFile" {
				t.Errorf("%s: label = %s, want InfraFile", name, n.Label)
			}
			if !store.IsSurfaceableCodeNode(n.Label, n.FilePath) {
				t.Errorf("%s: not surfaceable (file_path=%q)", name, n.FilePath)
			}
			if n.Properties["infra_type"] != "k8s-resource" {
				t.Errorf("%s: infra_type = %v", name, n.Properties["infra_type"])
			}
		}
		if !found {
			names := make([]string, 0, len(nodes))
			for _, n := range nodes {
				names = append(names, n.Name)
			}
			t.Errorf("role %s: %s not among %v", role, name, names)
		}
	}

	// Distinct qualified names per document, including namespaced ones.
	nodes, err := s.FindNodesByLabel(p.ProjectName, "InfraFile")
	if err != nil {
		t.Fatal(err)
	}
	qns := map[string]bool{}
	for _, n := range nodes {
		if qns[n.QualifiedName] {
			t.Errorf("duplicate InfraFile QN %s", n.QualifiedName)
		}
		qns[n.QualifiedName] = true
	}
	if len(nodes) != 11 {
		t.Errorf("InfraFile nodes = %d, want 11 (3+1+5+1 manifests + 1 kustomization); got %v", len(nodes), qns)
	}
}
