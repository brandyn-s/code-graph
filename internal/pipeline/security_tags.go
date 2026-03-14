package pipeline

import (
	"log/slog"
	"regexp"

	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

// Security role constants.
const (
	RoleAuthBoundary    = "auth_boundary"
	RoleInputEntryPoint = "input_entry_point"
	RoleSensitiveSink   = "sensitive_sink"
	RoleCryptoOperation = "crypto_operation"
)

var authNamePatterns = regexp.MustCompile(`(?i)(require_?auth|check_?auth|verify_?token|authenticate|authorize|is_?authenticated|validate_?session|check_?permission|require_?login)`)
var authDecoratorPatterns = regexp.MustCompile(`(?i)(login_required|requires_auth|authenticated|authorize|permission_required|auth_required|protect|guard)`)
var authFilePatterns = regexp.MustCompile(`(?i)(middleware[/\\]auth|auth[/\\]middleware|guards?[/\\]|policies[/\\])`)
var entryPointDecorators = regexp.MustCompile(`(?i)(@app\.(get|post|put|delete|patch)|@router\.|@api_view|@(Get|Post|Put|Delete|Patch)Mapping|#\[axum::)`)
var sinkNamePatterns = regexp.MustCompile(`(?i)(execute_?query|exec_?sql|raw_?query|run_?command|subprocess|write_?file|remove_?file|send_?email)`)
var sinkFilePatterns = regexp.MustCompile(`(?i)(db[/\\]|database[/\\]|repository[/\\]|repositories[/\\]|queries[/\\]|dal[/\\])`)
var cryptoPatterns = regexp.MustCompile(`(?i)(encrypt|decrypt|hash_?password|sign_?token|verify_?signature|(?:^|[^a-zA-Z])(?:hmac|aes|rsa|pbkdf|argon|bcrypt|scrypt)(?:[^a-zA-Z]|$))`)
var cryptoFilePatterns = regexp.MustCompile(`(?i)(crypto[/\\]|encryption[/\\]|certs?[/\\]|tls[/\\]|pki[/\\])`)

// classifySecurityRole determines the security role for a node based on its
// name, decorators, file path, and label. Returns empty string if no role matches.
func classifySecurityRole(n *store.Node) string {
	name := n.Name
	filePath := n.FilePath
	label := n.Label
	decorators := nodeDecorators(n)

	if label == "Route" {
		return RoleInputEntryPoint
	}

	if authNamePatterns.MatchString(name) || authFilePatterns.MatchString(filePath) {
		return RoleAuthBoundary
	}
	for _, dec := range decorators {
		if authDecoratorPatterns.MatchString(dec) {
			return RoleAuthBoundary
		}
	}

	if name == "main" || name == "Main" {
		return RoleInputEntryPoint
	}
	for _, dec := range decorators {
		if entryPointDecorators.MatchString(dec) {
			return RoleInputEntryPoint
		}
	}

	if cryptoPatterns.MatchString(name) || cryptoFilePatterns.MatchString(filePath) {
		return RoleCryptoOperation
	}

	if sinkNamePatterns.MatchString(name) || sinkFilePatterns.MatchString(filePath) {
		return RoleSensitiveSink
	}

	return ""
}

// nodeDecorators extracts decorator strings from a node's properties.
func nodeDecorators(n *store.Node) []string {
	if n.Properties == nil {
		return nil
	}
	raw, ok := n.Properties["decorators"]
	if !ok {
		return nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(arr))
	for _, v := range arr {
		if s, ok := v.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

// passSecurityTags enriches Function/Method/Class/Route nodes with security_role
// properties based on pattern matching. Runs as a post-flush pass.
func (p *Pipeline) passSecurityTags() {
	labels := []string{"Function", "Method", "Class", "Route"}
	tagged := 0

	for _, label := range labels {
		nodes, err := p.Store.FindNodesByLabel(p.ProjectName, label)
		if err != nil {
			continue
		}
		for _, n := range nodes {
			role := classifySecurityRole(n)
			if role == "" {
				continue
			}
			if n.Properties == nil {
				n.Properties = map[string]any{}
			}
			n.Properties["security_role"] = role
			_, _ = p.Store.UpsertNode(n)
			tagged++
		}
	}

	if tagged > 0 {
		slog.Info("pass.security_tags", "tagged", tagged)
	}
}
