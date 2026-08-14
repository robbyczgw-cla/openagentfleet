package policy

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxIDLength        = 160
	maxOperationLength = 96
	maxParameterName   = 96
	maxParameterValue  = 16 * 1024
)

var errWildcard = errors.New("wildcards are not supported")

// ValidateConfig validates a complete immutable broker configuration.
func ValidateConfig(config Config) error {
	_, err := validateConfig(config)
	return err
}

// ValidateAction validates an action without evaluating it.
func ValidateAction(action Action) error { return validateAction(action) }

// ValidateApproval validates an approval without evaluating it.
func ValidateApproval(approval Approval) error { return validateApproval(approval) }

func validateConfig(config Config) (Config, error) {
	if config.Version == 0 && !config.Enabled && len(config.Rules) == 0 {
		config.Version = CurrentVersion
	}
	if config.Version != CurrentVersion {
		return Config{}, fmt.Errorf("unsupported policy version %d", config.Version)
	}
	seen := make(map[string]struct{}, len(config.Rules))
	for index := range config.Rules {
		if err := validateRule(config.Rules[index]); err != nil {
			return Config{}, fmt.Errorf("rule %d: %w", index, err)
		}
		if _, exists := seen[config.Rules[index].ID]; exists {
			return Config{}, fmt.Errorf("duplicate rule id %q", config.Rules[index].ID)
		}
		seen[config.Rules[index].ID] = struct{}{}
	}
	config.Rules = cloneRules(config.Rules)
	return config, nil
}

func validateRule(rule Rule) error {
	if err := validateToken("rule id", rule.ID, maxIDLength); err != nil {
		return err
	}
	if err := validatePrincipal(rule.Principal); err != nil {
		return err
	}
	if !validEffect(rule.Effect) {
		return fmt.Errorf("invalid effect %q", rule.Effect)
	}
	if err := validateScope(rule.Scope); err != nil {
		return err
	}
	if len(rule.Operations) == 0 {
		return errors.New("at least one exact operation is required")
	}
	seen := make(map[string]struct{}, len(rule.Operations))
	for _, operation := range rule.Operations {
		if err := validateToken("operation", operation, maxOperationLength); err != nil {
			return err
		}
		if _, exists := seen[operation]; exists {
			return fmt.Errorf("duplicate operation %q", operation)
		}
		seen[operation] = struct{}{}
	}
	if rule.ExpiresAt != nil && rule.ExpiresAt.IsZero() {
		return errors.New("expires_at must be omitted or non-zero")
	}
	return nil
}

func validateAction(action Action) error {
	if err := validatePrincipal(action.Principal); err != nil {
		return err
	}
	if err := validateToken("run id", action.RunID, maxIDLength); err != nil {
		return err
	}
	if err := validateResource(action.Resource); err != nil {
		return err
	}
	if err := validateToken("operation", action.Operation, maxOperationLength); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(action.Parameters))
	for _, parameter := range action.Parameters {
		if err := validateToken("parameter name", parameter.Name, maxParameterName); err != nil {
			return err
		}
		if isSecretParameterName(parameter.Name) {
			return fmt.Errorf("parameter %q may contain a secret; use an opaque handoff reference instead", parameter.Name)
		}
		if _, exists := seen[parameter.Name]; exists {
			return fmt.Errorf("duplicate parameter name %q", parameter.Name)
		}
		seen[parameter.Name] = struct{}{}
		if !utf8.ValidString(parameter.Value) {
			return fmt.Errorf("parameter %q is not valid UTF-8", parameter.Name)
		}
		if len(parameter.Value) > maxParameterValue {
			return fmt.Errorf("parameter %q exceeds %d bytes", parameter.Name, maxParameterValue)
		}
		if strings.IndexByte(parameter.Value, 0) >= 0 {
			return fmt.Errorf("parameter %q contains NUL", parameter.Name)
		}
	}
	return nil
}

func validateApproval(approval Approval) error {
	if err := validateToken("approval id", approval.ID, maxIDLength); err != nil {
		return err
	}
	if err := validateActionHash(approval.ActionHash); err != nil {
		return err
	}
	if err := validateToken("approval rule id", approval.RuleID, maxIDLength); err != nil {
		return err
	}
	if err := validatePrincipal(approval.Principal); err != nil {
		return err
	}
	if err := validateToken("approval run id", approval.RunID, maxIDLength); err != nil {
		return err
	}
	switch approval.Status {
	case ApprovalApproved, ApprovalDenied, ApprovalRevoked:
	default:
		return fmt.Errorf("invalid approval status %q", approval.Status)
	}
	if approval.CreatedAt.IsZero() || approval.ExpiresAt.IsZero() {
		return errors.New("approval created_at and expires_at are required")
	}
	if !approval.ExpiresAt.After(approval.CreatedAt) {
		return errors.New("approval expires_at must be after created_at")
	}
	return nil
}

func validateScope(scope Scope) error {
	if scope.Match != MatchExact && scope.Match != MatchTree {
		return fmt.Errorf("invalid scope match %q", scope.Match)
	}
	if scope.Match == MatchTree && scope.Resource.Kind != ResourceFolder {
		return errors.New("tree matching is valid only for folders")
	}
	return validateResource(scope.Resource)
}

func validateResource(resource Resource) error {
	if !validResourceKind(resource.Kind) {
		return fmt.Errorf("invalid resource kind %q", resource.Kind)
	}
	if resource.Kind != ResourceNetwork && containsWildcard(resource.Target) || containsWildcard(resource.Qualifier) {
		return errWildcard
	}
	switch resource.Kind {
	case ResourceFolder:
		if resource.Qualifier != "" {
			return errors.New("folder resource cannot have a qualifier")
		}
		return validateFolder(resource.Target)
	case ResourceBrowserProfile, ResourceComputer, ResourceConnector:
		if resource.Qualifier != "" {
			return fmt.Errorf("%s resource cannot have a qualifier", resource.Kind)
		}
		return validateToken(string(resource.Kind)+" target", resource.Target, maxIDLength)
	case ResourceNativeApp:
		if resource.Qualifier != "" {
			return errors.New("native app resource cannot have a qualifier")
		}
		return validateBundleID(resource.Target)
	case ResourceNetwork:
		if resource.Qualifier != "" {
			return errors.New("network resource cannot have a qualifier")
		}
		return validateNetworkOrigin(resource.Target)
	case ResourceMCP:
		if err := validateToken("MCP server", resource.Target, maxIDLength); err != nil {
			return err
		}
		if resource.Qualifier != "" {
			return validateToken("MCP tool", resource.Qualifier, maxIDLength)
		}
	}
	return nil
}

func validatePrincipal(principal Principal) error {
	switch principal.Kind {
	case PrincipalController, PrincipalLead, PrincipalWorker, PrincipalRoutine, PrincipalMobile, PrincipalPlugin:
	default:
		return fmt.Errorf("invalid principal kind %q", principal.Kind)
	}
	return validateToken("principal id", principal.ID, maxIDLength)
}

func validateToken(name, value string, max int) error {
	if value == "" {
		return fmt.Errorf("%s is required", name)
	}
	if value != strings.TrimSpace(value) {
		return fmt.Errorf("%s must not have surrounding whitespace", name)
	}
	if len(value) > max {
		return fmt.Errorf("%s exceeds %d bytes", name, max)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s is not valid UTF-8", name)
	}
	if containsWildcard(value) {
		return fmt.Errorf("%s: %w", name, errWildcard)
	}
	for _, r := range value {
		if r > unicode.MaxASCII || unicode.IsControl(r) || unicode.IsSpace(r) || !(unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("._-:@", r)) {
			return fmt.Errorf("%s contains unsupported character %q", name, r)
		}
	}
	return nil
}

func validateFolder(path string) error {
	if path == "" {
		return errors.New("folder path is required")
	}
	if !utf8.ValidString(path) || strings.IndexByte(path, 0) >= 0 {
		return errors.New("folder path is not valid UTF-8 or contains NUL")
	}
	if containsWildcard(path) {
		return fmt.Errorf("folder path: %w", errWildcard)
	}
	if strings.Contains(path, "\\") {
		return errors.New("folder path must use canonical platform separators")
	}
	if !filepath.IsAbs(path) {
		return errors.New("folder path must be absolute")
	}
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		if part == "." || part == ".." {
			return errors.New("folder path contains traversal segment")
		}
	}
	if cleaned := filepath.Clean(path); cleaned != path {
		return fmt.Errorf("folder path must be canonical: use %q", cleaned)
	}
	if filepath.Dir(path) == path {
		return errors.New("filesystem root cannot be granted")
	}
	for _, r := range path {
		if unicode.IsControl(r) {
			return errors.New("folder path contains control characters")
		}
	}
	return nil
}

func validateBundleID(value string) error {
	if value == "" || value != strings.TrimSpace(value) || len(value) > maxIDLength || !utf8.ValidString(value) || containsWildcard(value) {
		return errors.New("native app target must be a canonical bundle identifier")
	}
	parts := strings.Split(value, ".")
	if len(parts) < 2 {
		return errors.New("native app target must be a bundle identifier")
	}
	for _, part := range parts {
		if part == "" {
			return errors.New("native app bundle id has an empty segment")
		}
		for _, r := range part {
			if r > unicode.MaxASCII || !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-') {
				return errors.New("native app bundle id contains unsupported characters")
			}
		}
	}
	return nil
}

func validateNetworkOrigin(value string) error {
	if value == "" {
		return errors.New("network origin is required")
	}
	if containsNetworkWildcard(value) {
		return fmt.Errorf("network origin: %w", errWildcard)
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return fmt.Errorf("invalid network origin: %w", err)
	}
	switch parsed.Scheme {
	case "http", "https", "ws", "wss", "tcp", "tls":
	default:
		return fmt.Errorf("unsupported network scheme %q", parsed.Scheme)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" || parsed.RawPath != "" {
		return errors.New("network target must be an exact origin without credentials, path, query, or fragment")
	}
	host := parsed.Hostname()
	if host == "" || host != strings.ToLower(host) || !isASCII(host) {
		return errors.New("network hostname must be non-empty, lowercase ASCII or an IP literal")
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsUnspecified() {
			return errors.New("unspecified network address cannot be a destination")
		}
	} else {
		for _, label := range strings.Split(host, ".") {
			if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
				return errors.New("network hostname has an invalid label")
			}
			for _, r := range label {
				if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '-' {
					return errors.New("network hostname contains unsupported characters")
				}
			}
		}
	}
	port := parsed.Port()
	if (parsed.Scheme == "tcp" || parsed.Scheme == "tls") && port == "" {
		return errors.New("tcp and tls origins require an exact port")
	}
	if port != "" {
		number, err := strconv.ParseUint(port, 10, 16)
		if err != nil || number == 0 {
			return errors.New("network port must be between 1 and 65535")
		}
	}
	canonicalHost := host
	if strings.Contains(host, ":") {
		canonicalHost = "[" + host + "]"
	}
	if port != "" {
		canonicalHost = net.JoinHostPort(host, port)
	}
	canonical := parsed.Scheme + "://" + canonicalHost
	if value != canonical {
		return fmt.Errorf("network origin must be canonical: use %q", canonical)
	}
	return nil
}

func validEffect(effect Effect) bool {
	return effect == EffectAllow || effect == EffectDeny || effect == EffectAsk
}

func validResourceKind(kind ResourceKind) bool {
	switch kind {
	case ResourceFolder, ResourceBrowserProfile, ResourceNativeApp, ResourceNetwork, ResourceMCP, ResourceComputer, ResourceConnector:
		return true
	default:
		return false
	}
}

func containsWildcard(value string) bool { return strings.ContainsAny(value, "*?[]{}") }

func containsNetworkWildcard(value string) bool { return strings.ContainsAny(value, "*?{}") }

func isSecretParameterName(value string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(value, "-", "_"))
	switch normalized {
	case "password", "passwd", "secret", "token", "api_key", "auth_token", "access_token", "refresh_token", "credential", "credentials", "otp", "one_time_password":
		return true
	default:
		return false
	}
}

func isASCII(value string) bool {
	for _, r := range value {
		if r > unicode.MaxASCII || unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}
