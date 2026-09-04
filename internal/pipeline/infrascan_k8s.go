package pipeline

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// --- Kubernetes manifest parser ---
//
// Any *.yaml / *.yml file that is not a compose or cloudbuild file is probed
// for Kubernetes documents (apiVersion + kind + metadata.name, or a
// Kustomization). Each document becomes one InfraFile node with
// infra_type "k8s-resource" named "Kind/name", carrying the security-relevant
// facts of the manifest. Resources that open an attack surface are tagged with
// security_role / security_subtype so query_security_surfaces reports them
// next to code surfaces (ported from upstream codebase-memory-mcp's
// extract_k8s, which emitted a Resource definition per manifest; this fork
// keeps infrastructure in the InfraFile pass and adds the security facts).
//
// Secret VALUES are never stored: for kind Secret only the key names and the
// secret type are recorded. Helm templates ({{ ... }}) are skipped because
// they are not valid YAML until rendered.

// k8sDocument is the subset of a manifest we look at. Spec and the RBAC
// fields are kept generic because their shapes vary per kind.
type k8sDocument struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name      string `yaml:"name"`
		Namespace string `yaml:"namespace"`
	} `yaml:"metadata"`
	Spec                         map[string]any   `yaml:"spec"`
	Type                         string           `yaml:"type"`
	Data                         map[string]any   `yaml:"data"`
	StringData                   map[string]any   `yaml:"stringData"`
	Rules                        []map[string]any `yaml:"rules"`
	RoleRef                      map[string]any   `yaml:"roleRef"`
	Subjects                     []map[string]any `yaml:"subjects"`
	AutomountServiceAccountToken *bool            `yaml:"automountServiceAccountToken"`
	Resources                    []string         `yaml:"resources"`
	Webhooks                     []map[string]any `yaml:"webhooks"`
}

// dangerousCapabilities are Linux capabilities whose addition to a container
// is equivalent to (or a short step from) root on the node.
var dangerousCapabilities = map[string]bool{
	"ALL": true, "SYS_ADMIN": true, "SYS_PTRACE": true, "SYS_MODULE": true,
	"NET_ADMIN": true, "SYS_RAWIO": true, "DAC_READ_SEARCH": true, "SYS_BOOT": true,
}

func isKubernetesYAML(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".yaml") || strings.HasSuffix(lower, ".yml")
}

// parseKubernetesFile returns one infraFile per Kubernetes document in the
// file, or nil when the file holds no Kubernetes documents.
func parseKubernetesFile(absPath, relPath string) []infraFile {
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil
	}
	// Cheap prefilter before a YAML decode: every manifest names apiVersion and
	// kind; Helm templates are not YAML until rendered.
	if !bytes.Contains(data, []byte("apiVersion")) || !bytes.Contains(data, []byte("kind")) || bytes.Contains(data, []byte("{{")) {
		return nil
	}

	var result []infraFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var doc k8sDocument
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// A malformed document poisons the decoder; keep what parsed.
			break
		}
		if doc.APIVersion == "" || doc.Kind == "" {
			continue
		}
		if doc.Kind == "Kustomization" {
			result = append(result, kustomizationInfra(relPath, &doc))
			continue
		}
		if doc.Metadata.Name == "" {
			continue
		}
		result = append(result, kubernetesResourceInfra(relPath, &doc))
	}
	return result
}

func kustomizationInfra(relPath string, doc *k8sDocument) infraFile {
	props := map[string]any{
		"infra_type":  "k8s-kustomization",
		"api_version": doc.APIVersion,
		"kind":        doc.Kind,
	}
	setNonEmpty(props, "resources", doc.Resources)
	return infraFile{relPath: relPath, infraType: "k8s-kustomization", name: "Kustomization", properties: props}
}

// k8sFacts accumulates the security-relevant observations for one manifest.
type k8sFacts struct {
	props   map[string]any
	signals map[string][]string // role -> ordered signals
}

func (f *k8sFacts) signal(role, name string) {
	for _, s := range f.signals[role] {
		if s == name {
			return
		}
	}
	f.signals[role] = append(f.signals[role], name)
}

func kubernetesResourceInfra(relPath string, doc *k8sDocument) infraFile {
	f := &k8sFacts{
		props: map[string]any{
			"infra_type":  "k8s-resource",
			"api_version": doc.APIVersion,
			"kind":        doc.Kind,
			"name":        doc.Metadata.Name,
		},
		signals: map[string][]string{},
	}
	setNonEmptyStr(f.props, "namespace", doc.Metadata.Namespace)

	switch doc.Kind {
	case "Service":
		f.service(doc.Spec)
	case "Ingress":
		f.ingress(doc.Spec)
	case "HTTPRoute", "GRPCRoute", "TLSRoute", "TCPRoute", "UDPRoute", "Gateway", "VirtualService":
		f.gatewayRoute(doc.Kind, doc.Spec)
	case "NetworkPolicy":
		f.networkPolicy(doc.Spec)
	case "Role", "ClusterRole":
		f.rbacRole(doc.Rules)
	case "RoleBinding", "ClusterRoleBinding":
		f.rbacBinding(doc.RoleRef, doc.Subjects)
	case "ServiceAccount":
		f.signal(RoleAuthBoundary, "service_account")
		setBoolPtr(f.props, "automount_service_account_token", doc.AutomountServiceAccountToken)
	case "Secret":
		f.secret(doc)
	case "ValidatingWebhookConfiguration", "MutatingWebhookConfiguration", "PodSecurityPolicy":
		f.signal(RoleAuthBoundary, "admission_control")
		if len(doc.Webhooks) > 0 {
			f.props["webhooks"] = len(doc.Webhooks)
		}
	default:
		// Workloads and anything else that may embed a pod template.
	}
	if podSpec := findPodSpec(doc.Spec); podSpec != nil {
		f.podSpec(podSpec)
	}

	f.classify()
	return infraFile{
		relPath:    relPath,
		infraType:  "k8s-resource",
		name:       doc.Kind + "/" + doc.Metadata.Name,
		properties: f.props,
	}
}

// classify picks the single security_role for the node (most severe first) and
// records every signal so the caller can see why.
func (f *k8sFacts) classify() {
	var all []string
	for _, role := range []string{RolePrivilegeEscalation, RoleInputEntryPoint, RoleAuthBoundary, RoleSensitiveSink} {
		sigs := f.signals[role]
		if len(sigs) == 0 {
			continue
		}
		if _, tagged := f.props["security_role"]; !tagged {
			f.props["security_role"] = role
			f.props["security_subtype"] = "k8s_" + sigs[0]
		}
		for _, s := range sigs {
			all = append(all, "k8s_"+s)
		}
	}
	setNonEmpty(f.props, "security_signals", all)
}

func (f *k8sFacts) service(spec map[string]any) {
	svcType := stringField(spec, "type")
	if svcType == "" {
		svcType = "ClusterIP"
	}
	f.props["service_type"] = svcType
	var ports []string
	for _, p := range listField(spec, "ports") {
		port := fmt.Sprint(anyField(p, "port"))
		if np := anyField(p, "nodePort"); np != nil {
			port += ":nodePort=" + fmt.Sprint(np)
		}
		if proto := stringField(p, "protocol"); proto != "" && proto != "TCP" {
			port += "/" + proto
		}
		ports = append(ports, port)
	}
	setNonEmpty(f.props, "service_ports", ports)
	externalIPs := stringList(spec["externalIPs"])
	setNonEmpty(f.props, "external_ips", externalIPs)
	if svcType == "LoadBalancer" || svcType == "NodePort" || len(externalIPs) > 0 {
		f.signal(RoleInputEntryPoint, "external_service")
	}
}

func (f *k8sFacts) ingress(spec map[string]any) {
	f.signal(RoleInputEntryPoint, "ingress")
	var hosts, paths, backends []string
	for _, rule := range listField(spec, "rules") {
		if h := stringField(rule, "host"); h != "" {
			hosts = append(hosts, h)
		}
		httpRule, _ := rule["http"].(map[string]any)
		for _, p := range listField(httpRule, "paths") {
			if path := stringField(p, "path"); path != "" {
				paths = append(paths, path)
			}
			if backend, ok := p["backend"].(map[string]any); ok {
				if svc, ok := backend["service"].(map[string]any); ok {
					if name := stringField(svc, "name"); name != "" {
						backends = append(backends, name)
					}
				}
			}
		}
	}
	setNonEmpty(f.props, "ingress_hosts", hosts)
	setNonEmpty(f.props, "ingress_paths", paths)
	setNonEmpty(f.props, "backend_services", dedupe(backends))
	if len(listField(spec, "tls")) > 0 {
		f.props["tls"] = true
	}
}

func (f *k8sFacts) gatewayRoute(kind string, spec map[string]any) {
	f.signal(RoleInputEntryPoint, "gateway_route")
	setNonEmpty(f.props, "ingress_hosts", append(stringList(spec["hostnames"]), stringList(spec["hosts"])...))
	var paths []string
	for _, rule := range listField(spec, "rules") {
		for _, m := range listField(rule, "matches") {
			if path, ok := m["path"].(map[string]any); ok {
				if v := stringField(path, "value"); v != "" {
					paths = append(paths, v)
				}
			}
		}
	}
	setNonEmpty(f.props, "ingress_paths", paths)
	if kind == "Gateway" {
		var listeners []string
		for _, l := range listField(spec, "listeners") {
			listeners = append(listeners, fmt.Sprintf("%s:%v", stringField(l, "protocol"), anyField(l, "port")))
		}
		setNonEmpty(f.props, "listeners", listeners)
	}
}

func (f *k8sFacts) networkPolicy(spec map[string]any) {
	f.signal(RoleAuthBoundary, "network_policy")
	setNonEmpty(f.props, "policy_types", stringList(spec["policyTypes"]))
	f.props["ingress_rules"] = len(listField(spec, "ingress"))
	f.props["egress_rules"] = len(listField(spec, "egress"))
}

func (f *k8sFacts) rbacRole(rules []map[string]any) {
	f.signal(RoleAuthBoundary, "rbac_role")
	var summary []string
	for _, rule := range rules {
		verbs := stringList(rule["verbs"])
		resources := stringList(rule["resources"])
		groups := stringList(rule["apiGroups"])
		summary = append(summary, fmt.Sprintf("apiGroups=%s resources=%s verbs=%s",
			strings.Join(groups, ","), strings.Join(resources, ","), strings.Join(verbs, ",")))
		if contains(verbs, "*") || contains(resources, "*") {
			f.signal(RolePrivilegeEscalation, "wildcard_rbac_rule")
		}
		if contains(resources, "secrets") && (contains(verbs, "get") || contains(verbs, "list") || contains(verbs, "watch") || contains(verbs, "*")) {
			f.signal(RolePrivilegeEscalation, "secrets_read_rbac")
		}
		if contains(resources, "pods/exec") || contains(resources, "pods/attach") {
			f.signal(RolePrivilegeEscalation, "pod_exec_rbac")
		}
		if contains(verbs, "escalate") || contains(verbs, "bind") || contains(verbs, "impersonate") {
			f.signal(RolePrivilegeEscalation, "rbac_escalate_verb")
		}
	}
	setNonEmpty(f.props, "rbac_rules", summary)
}

func (f *k8sFacts) rbacBinding(roleRef map[string]any, subjects []map[string]any) {
	f.signal(RoleAuthBoundary, "rbac_binding")
	if ref := stringField(roleRef, "name"); ref != "" {
		f.props["role_ref"] = stringField(roleRef, "kind") + "/" + ref
		if ref == "cluster-admin" || ref == "admin" || ref == "system:masters" {
			f.signal(RolePrivilegeEscalation, "admin_role_binding")
		}
	}
	var subs []string
	for _, s := range subjects {
		entry := stringField(s, "kind") + ":" + stringField(s, "name")
		if ns := stringField(s, "namespace"); ns != "" {
			entry = stringField(s, "kind") + ":" + ns + "/" + stringField(s, "name")
		}
		subs = append(subs, entry)
		if name := stringField(s, "name"); name == "system:authenticated" || name == "system:unauthenticated" || name == "system:anonymous" {
			f.signal(RolePrivilegeEscalation, "broad_subject_binding")
		}
	}
	setNonEmpty(f.props, "subjects", subs)
}

func (f *k8sFacts) secret(doc *k8sDocument) {
	f.signal(RoleSensitiveSink, "secret_resource")
	setNonEmptyStr(f.props, "secret_type", doc.Type)
	keys := make([]string, 0, len(doc.Data)+len(doc.StringData))
	for k := range doc.Data {
		keys = append(keys, k)
	}
	for k := range doc.StringData {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	setNonEmpty(f.props, "secret_keys", keys) // names only, never values
}

// findPodSpec locates the pod spec inside a workload spec: the spec itself
// (Pod), spec.template.spec (Deployment, StatefulSet, DaemonSet, Job,
// ReplicaSet), or spec.jobTemplate.spec.template.spec (CronJob).
func findPodSpec(spec map[string]any) map[string]any {
	for depth := 0; spec != nil && depth < 4; depth++ {
		if _, ok := spec["containers"]; ok {
			return spec
		}
		next := mapField(spec, "template")
		if next == nil {
			next = mapField(spec, "jobTemplate")
		}
		if next == nil {
			return nil
		}
		spec = mapField(next, "spec")
	}
	return nil
}

func (f *k8sFacts) podSpec(pod map[string]any) {
	if boolField(pod, "hostNetwork") {
		f.props["host_network"] = true
		f.signal(RoleInputEntryPoint, "host_network")
	}
	if boolField(pod, "hostPID") {
		f.props["host_pid"] = true
		f.signal(RolePrivilegeEscalation, "host_pid")
	}
	if boolField(pod, "hostIPC") {
		f.props["host_ipc"] = true
		f.signal(RolePrivilegeEscalation, "host_ipc")
	}
	setNonEmptyStr(f.props, "service_account", stringField(pod, "serviceAccountName"))
	if v, ok := pod["automountServiceAccountToken"].(bool); ok {
		f.props["automount_service_account_token"] = v
	}
	podCtx := mapField(pod, "securityContext")
	podRunsAsRoot := runsAsRoot(podCtx)

	var images, names, ports, hostPorts, caps, secretRefs, configMapRefs, hostPaths []string
	for _, key := range []string{"initContainers", "containers", "ephemeralContainers"} {
		for _, c := range listField(pod, key) {
			names = append(names, stringField(c, "name"))
			if img := stringField(c, "image"); img != "" {
				images = append(images, img)
			}
			for _, p := range listField(c, "ports") {
				if cp := anyField(p, "containerPort"); cp != nil {
					port := fmt.Sprint(cp)
					if proto := stringField(p, "protocol"); proto != "" && proto != "TCP" {
						port += "/" + proto
					}
					ports = append(ports, port)
				}
				if hp := anyField(p, "hostPort"); hp != nil {
					hostPorts = append(hostPorts, fmt.Sprint(hp))
				}
			}
			ctx := mapField(c, "securityContext")
			if boolField(ctx, "privileged") {
				f.props["privileged"] = true
				f.signal(RolePrivilegeEscalation, "privileged_container")
			}
			if boolField(ctx, "allowPrivilegeEscalation") {
				f.props["allow_privilege_escalation"] = true
				f.signal(RolePrivilegeEscalation, "allow_privilege_escalation")
			}
			if runsAsRoot(ctx) || (podRunsAsRoot && ctx["runAsUser"] == nil && ctx["runAsNonRoot"] == nil) {
				f.props["runs_as_root"] = true
				f.signal(RolePrivilegeEscalation, "runs_as_root")
			}
			for _, capName := range stringList(anyField(mapField(ctx, "capabilities"), "add")) {
				caps = append(caps, capName)
				if dangerousCapabilities[strings.TrimPrefix(strings.ToUpper(capName), "CAP_")] {
					f.signal(RolePrivilegeEscalation, "dangerous_capability")
				}
			}
			for _, e := range listField(c, "env") {
				if ref := mapField(mapField(e, "valueFrom"), "secretKeyRef"); ref != nil {
					secretRefs = append(secretRefs, stringField(ref, "name"))
				}
				if ref := mapField(mapField(e, "valueFrom"), "configMapKeyRef"); ref != nil {
					configMapRefs = append(configMapRefs, stringField(ref, "name"))
				}
			}
			for _, e := range listField(c, "envFrom") {
				if ref := mapField(e, "secretRef"); ref != nil {
					secretRefs = append(secretRefs, stringField(ref, "name"))
				}
				if ref := mapField(e, "configMapRef"); ref != nil {
					configMapRefs = append(configMapRefs, stringField(ref, "name"))
				}
			}
		}
	}
	for _, v := range listField(pod, "volumes") {
		if sec := mapField(v, "secret"); sec != nil {
			secretRefs = append(secretRefs, stringField(sec, "secretName"))
		}
		if cm := mapField(v, "configMap"); cm != nil {
			configMapRefs = append(configMapRefs, stringField(cm, "name"))
		}
		if hp := mapField(v, "hostPath"); hp != nil {
			hostPaths = append(hostPaths, stringField(hp, "path"))
		}
	}
	if len(hostPorts) > 0 {
		f.signal(RoleInputEntryPoint, "host_port")
	}
	if len(hostPaths) > 0 {
		f.signal(RolePrivilegeEscalation, "host_path_volume")
	}
	secretRefs = dedupe(secretRefs)
	if len(secretRefs) > 0 {
		f.signal(RoleSensitiveSink, "secret_reference")
	}
	setNonEmpty(f.props, "containers", names)
	setNonEmpty(f.props, "images", images)
	setNonEmpty(f.props, "container_ports", ports)
	setNonEmpty(f.props, "host_ports", hostPorts)
	setNonEmpty(f.props, "capabilities_add", caps)
	setNonEmpty(f.props, "secret_refs", secretRefs)
	setNonEmpty(f.props, "configmap_refs", dedupe(configMapRefs))
	setNonEmpty(f.props, "host_paths", hostPaths)
}

// runsAsRoot reports an explicit root configuration in a securityContext:
// runAsUser 0 or runAsNonRoot false.
func runsAsRoot(ctx map[string]any) bool {
	if ctx == nil {
		return false
	}
	if v, ok := ctx["runAsNonRoot"].(bool); ok && !v {
		return true
	}
	switch v := ctx["runAsUser"].(type) {
	case int:
		return v == 0
	case int64:
		return v == 0
	case float64:
		return v == 0
	}
	return false
}

// --- small accessors for generic YAML maps ---

func mapField(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	v, _ := m[key].(map[string]any)
	return v
}

func listField(m map[string]any, key string) []map[string]any {
	if m == nil {
		return nil
	}
	raw, _ := m[key].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if im, ok := item.(map[string]any); ok {
			out = append(out, im)
		}
	}
	return out
}

func anyField(m map[string]any, key string) any {
	if m == nil {
		return nil
	}
	return m[key]
}

func stringField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	switch v := m[key].(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

func boolField(m map[string]any, key string) bool {
	if m == nil {
		return false
	}
	v, _ := m[key].(bool)
	return v
}

func stringList(v any) []string {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			out = append(out, s)
		} else if item != nil {
			out = append(out, fmt.Sprint(item))
		}
	}
	return out
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func dedupe(list []string) []string {
	seen := make(map[string]bool, len(list))
	out := make([]string, 0, len(list))
	for _, s := range list {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func setBoolPtr(m map[string]any, key string, v *bool) {
	if v != nil {
		m[key] = *v
	}
}
