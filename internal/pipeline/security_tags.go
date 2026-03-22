package pipeline

import (
	"log/slog"
	"regexp"

	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

// Security role constants.
const (
	RoleAuthBoundary        = "auth_boundary"
	RoleInputEntryPoint     = "input_entry_point"
	RoleSensitiveSink       = "sensitive_sink"
	RoleCryptoOperation     = "crypto_operation"
	RolePrivilegeEscalation = "privilege_escalation"
	RoleSessionManagement   = "session_management"
	RoleAuditLogging        = "audit_logging"
	RoleSanitizer           = "sanitizer"
)

// Security subtype constants — granular classification within each role.
const (
	// input_entry_point subtypes
	SubtypeHTTPHandler      = "http_handler"
	SubtypeCLIEntry         = "cli_entry"
	SubtypeGRPCHandler      = "grpc_handler"
	SubtypeWebSocketHandler = "websocket_handler"

	// sensitive_sink subtypes
	SubtypeSQLQuery    = "sql_query"
	SubtypeShellExec   = "shell_exec"
	SubtypeFileWrite   = "file_write"
	SubtypeNetworkSend = "network_send"
	SubtypeHardwareIO  = "hardware_io"

	// crypto_operation subtypes
	SubtypeEncryption    = "encryption"
	SubtypeHashing       = "hashing"
	SubtypeSigning       = "signing"
	SubtypeKeyGeneration = "key_generation"

	// auth_boundary subtypes
	SubtypeAuthCheck = "auth_check"

	// sanitizer subtypes
	SubtypeInputValidation = "input_validation"
	SubtypeTypeCheck       = "type_check"
	SubtypeEscapeEncode    = "escape_encode"
	SubtypeBoundsCheck     = "bounds_check"
)

var authNamePatterns = regexp.MustCompile(`(?i)(require_?auth|check_?auth|verify_?token|authenticate|authorize|is_?authenticated|validate_?session|check_?permission|require_?login)`)
var authDecoratorPatterns = regexp.MustCompile(`(?i)(login_required|requires_auth|authenticated|authorize|permission_required|auth_required|protect|guard)`)
var authFilePatterns = regexp.MustCompile(`(?i)(middleware[/\\]auth|auth[/\\]middleware|guards?[/\\]|policies[/\\])`)
var entryPointDecorators = regexp.MustCompile(`(?i)(@app\.(get|post|put|delete|patch)|@router\.|@api_view|@(Get|Post|Put|Delete|Patch)Mapping|#\[axum::|#\[actix_web::|#\[rocket::(get|post|put|delete)|@\w+\.command|@celery\.task|@pytest\.fixture|@receiver\(|@\w+\.on_event|@(task|shared_task)\b)`)
var entryPointNamePatterns = regexp.MustCompile(`(?i)(^handle_|_handler$|^on_request|^on_message|^process_request|^serve_|^dispatch_|^route_)`)
var entryPointFilePatterns = regexp.MustCompile(`(?i)(handler[s]?[/\\]|route[s]?[/\\]|endpoint[s]?[/\\]|api[/\\]|server[/\\].*handler|controller[s]?[/\\])`)
var sinkNamePatterns = regexp.MustCompile(`(?i)(execute_?query|exec_?sql|raw_?query|run_?command|subprocess|write_?file|remove_?file|send_?email|write_all|send_?frame|write_?frame|execute|spawn|std::process|Command::new|remove_dir|remove_dir_all|set_permissions|File::create|OpenOptions)`)
var sinkFilePatterns = regexp.MustCompile(`(?i)(db[/\\]|database[/\\]|repository[/\\]|repositories[/\\]|queries[/\\]|dal[/\\])`)
var sinkExcludeNames = regexp.MustCompile(`^(unwrap|expect|clone|default|new|fmt|from|into|as_ref|as_mut|deref|drop|len|is_empty|to_string|to_owned|display|debug|eq|ne|cmp|hash|index|iter|map|filter|fold|collect|ok|err|some|none|test_\w+)$`)
var sinkFileNameHint = regexp.MustCompile(`(?i)(query|exec|insert|update|delete|upsert|write|save|store|put|remove|create|drop|truncate|migrate|commit|rollback|transact|persist|flush)`)
var cryptoPatterns = regexp.MustCompile(`(?i)(encrypt|decrypt|hash_?password|sign_?token|verify_?signature|(?:^|[^a-zA-Z])(?:hmac|aes|rsa|pbkdf|argon|bcrypt|scrypt)(?:[^a-zA-Z]|$))`)
var cryptoFilePatterns = regexp.MustCompile(`(?i)(crypto[/\\]|encryption[/\\]|certs?[/\\]|tls[/\\]|pki[/\\])`)
var privEscPatterns = regexp.MustCompile(`(?i)(escalate_?privile|setuid|seteuid|setgid|sudo_?exec|become_?root|impersonate|assume_?role|sts_assume)`)
var sessionPatterns = regexp.MustCompile(`(?i)(create_?session|destroy_?session|invalidate_?session|refresh_?token|revoke_?token|session_?store|set_?cookie|clear_?cookie)`)
var sessionFilePatterns = regexp.MustCompile(`(?i)(session[/\\]|sessions[/\\])`)
var auditPatterns = regexp.MustCompile(`(?i)(audit_?log|write_?audit|log_?event|record_?event|compliance_?log|security_?log)`)
var auditFilePatterns = regexp.MustCompile(`(?i)(audit[/\\]|auditing[/\\]|compliance[/\\])`)
var sanitizerNamePatterns = regexp.MustCompile(`(?i)(^validate_|^sanitize_|^escape_|^encode_|^clean_|^normalize_|^check_bounds|^verify_|^assert_valid|^ensure_|^parse_int|^parse_uint|^parse_float|^parse_uuid|^parse_id|^parse_bool|^try_from|^from_str|_validator$|_sanitizer$|_checker$)`)
var sanitizerFilePatterns = regexp.MustCompile(`(?i)(valid(at(e|or|ion))?[/\\]|sanitiz(e|er)[/\\]|input[_-]?check)`)

// Subtype patterns for sanitizer subtypes.
var subtypeInputValidationPatterns = regexp.MustCompile(`(?i)(^validate_|^sanitize_|^clean_|^normalize_|_validator$|_sanitizer$|^ensure_valid|^check_input|^verify_input)`)
var subtypeTypeCheckPatterns = regexp.MustCompile(`(?i)(^parse_int|^parse_uint|^parse_float|^parse_uuid|^parse_id|^parse_bool|^try_from|^from_str|^to_int|^to_float|^as_int|_type_check|^coerce_|^cast_)`)
var subtypeEscapeEncodePatterns = regexp.MustCompile(`(?i)(^escape_|^encode_|^quote_|^html_escape|^url_encode|^sql_escape|^shell_escape|^xml_escape)`)
var subtypeBoundsCheckPatterns = regexp.MustCompile(`(?i)(^check_bounds|^check_range|^check_length|^check_size|^clamp|^limit_|^cap_|^constrain|^assert_in_range)`)

// Subtype patterns for granular classification within roles.
var subtypeSQLPatterns = regexp.MustCompile(`(?i)(execute_?query|exec_?sql|raw_?query|query_?row|prepare_?statement|sql\.Open|sqlx::query|diesel::insert|sea_orm.*insert|rusqlite.*execute)`)
var subtypeShellPatterns = regexp.MustCompile(`(?i)(run_?command|subprocess|exec\.Command|os\.system|popen|shell_exec|child_process|Command::new|std::process|spawn|process::Command)`)
var subtypeFileWritePatterns = regexp.MustCompile(`(?i)(write_?file|remove_?file|os\.Create|os\.Remove|os\.WriteFile|unlink|truncate|fwrite|write_all|File::create|OpenOptions|remove_dir|remove_dir_all|set_permissions|std::fs::write|tokio::fs::write)`)
var subtypeNetworkSendPatterns = regexp.MustCompile(`(?i)(send_?email|http\.Post|fetch|send_?request|smtp|net\.Dial|TcpStream|UdpSocket|hyper::Client|reqwest|connect|send_?to|send_?msg)`)
var subtypeHardwareIOPatterns = regexp.MustCompile(`(?i)(send_?frame|write_?frame|can_?write|can_?send|socketcan|canbus|serial_?write|gpio_?write|spi_?transfer|i2c_?write|ioctl)`)
var subtypeGRPCPatterns = regexp.MustCompile(`(?i)(grpc|RegisterService|pb\.Register|\.proto)`)
var subtypeWebSocketPatterns = regexp.MustCompile(`(?i)(websocket|ws_handler|on_?message|upgrade_?connection)`)
var subtypeEncryptPatterns = regexp.MustCompile(`(?i)(encrypt|decrypt|(?:^|[^a-zA-Z])(?:aes|rsa|chacha)(?:[^a-zA-Z]|$))`)
var subtypeHashPatterns = regexp.MustCompile(`(?i)(hash|(?:^|[^a-zA-Z])(?:sha256|sha512|md5|blake|pbkdf|argon|bcrypt|scrypt)(?:[^a-zA-Z]|$))`)
var subtypeSignPatterns = regexp.MustCompile(`(?i)(sign|verify_?signature|(?:^|[^a-zA-Z])(?:hmac|ed25519|ecdsa)(?:[^a-zA-Z]|$))`)
var subtypeKeyGenPatterns = regexp.MustCompile(`(?i)(generate_?key|new_?key|key_?pair|keygen)`)

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
	// Rust handler functions: named *_handler, handle_*, or in handler/routes files
	if entryPointNamePatterns.MatchString(name) || entryPointFilePatterns.MatchString(filePath) {
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

	if privEscPatterns.MatchString(name) {
		return RolePrivilegeEscalation
	}
	if sessionPatterns.MatchString(name) || sessionFilePatterns.MatchString(filePath) {
		return RoleSessionManagement
	}
	if auditPatterns.MatchString(name) || auditFilePatterns.MatchString(filePath) {
		return RoleAuditLogging
	}

	if sanitizerNamePatterns.MatchString(name) || sanitizerFilePatterns.MatchString(filePath) {
		return RoleSanitizer
	}

	if sinkNamePatterns.MatchString(name) {
		return RoleSensitiveSink
	}
	if sinkFilePatterns.MatchString(filePath) && !sinkExcludeNames.MatchString(name) && sinkFileNameHint.MatchString(name) {
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

// classifySecuritySubtype returns a granular subtype for a node within its security role.
// Returns empty string if no subtype matches (the role alone is still valid).
func classifySecuritySubtype(n *store.Node, role string) string {
	name := n.Name
	decorators := nodeDecorators(n)

	switch role {
	case RoleInputEntryPoint:
		if n.Label == "Route" {
			return SubtypeHTTPHandler
		}
		for _, dec := range decorators {
			if subtypeGRPCPatterns.MatchString(dec) {
				return SubtypeGRPCHandler
			}
			if subtypeWebSocketPatterns.MatchString(dec) {
				return SubtypeWebSocketHandler
			}
		}
		if subtypeGRPCPatterns.MatchString(name) || subtypeGRPCPatterns.MatchString(n.FilePath) {
			return SubtypeGRPCHandler
		}
		if subtypeWebSocketPatterns.MatchString(name) {
			return SubtypeWebSocketHandler
		}
		if name == "main" || name == "Main" {
			return SubtypeCLIEntry
		}
		if entryPointNamePatterns.MatchString(name) || entryPointFilePatterns.MatchString(n.FilePath) {
			return SubtypeHTTPHandler
		}
		return SubtypeHTTPHandler

	case RoleSensitiveSink:
		if subtypeHardwareIOPatterns.MatchString(name) {
			return SubtypeHardwareIO
		}
		if subtypeSQLPatterns.MatchString(name) {
			return SubtypeSQLQuery
		}
		if subtypeShellPatterns.MatchString(name) {
			return SubtypeShellExec
		}
		if subtypeFileWritePatterns.MatchString(name) {
			return SubtypeFileWrite
		}
		if subtypeNetworkSendPatterns.MatchString(name) {
			return SubtypeNetworkSend
		}
		return ""

	case RoleCryptoOperation:
		if subtypeKeyGenPatterns.MatchString(name) {
			return SubtypeKeyGeneration
		}
		if subtypeSignPatterns.MatchString(name) {
			return SubtypeSigning
		}
		if subtypeHashPatterns.MatchString(name) {
			return SubtypeHashing
		}
		if subtypeEncryptPatterns.MatchString(name) {
			return SubtypeEncryption
		}
		return ""

	case RoleAuthBoundary:
		return SubtypeAuthCheck

	case RoleSanitizer:
		if subtypeEscapeEncodePatterns.MatchString(name) {
			return SubtypeEscapeEncode
		}
		if subtypeTypeCheckPatterns.MatchString(name) {
			return SubtypeTypeCheck
		}
		if subtypeBoundsCheckPatterns.MatchString(name) {
			return SubtypeBoundsCheck
		}
		return SubtypeInputValidation
	}

	return ""
}

// passSecurityTags enriches Function/Method/Class/Route nodes with security_role
// and security_subtype properties based on pattern matching. Runs as a post-flush pass.
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
			if subtype := classifySecuritySubtype(n, role); subtype != "" {
				n.Properties["security_subtype"] = subtype
			}
			_, _ = p.Store.UpsertNode(n)
			tagged++
		}
	}

	if tagged > 0 {
		slog.Info("pass.security_tags", "tagged", tagged)
	}
}
