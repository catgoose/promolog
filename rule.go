package promolog

import (
	"path"
	"strings"
	"time"
)

// FilterRule represents a runtime filter rule that can suppress, promote,
// tag, or set a short TTL on traces based on request metadata.
type FilterRule struct {
	ID        int       `json:"id"`
	Name      string    `json:"name"`
	Field     string    `json:"field"`     // "remote_ip", "route", "status_code", "method", "user_agent", "user_id"
	Operator  string    `json:"operator"`  // "equals", "contains", "starts_with", "matches_glob"
	Value     string    `json:"value"`
	Action    string    `json:"action"`    // "suppress", "always_promote", "tag", "short_ttl"
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
}

// RuleAction is the result returned when a filter rule matches.
type RuleAction struct {
	// Action is the action to take: "suppress", "always_promote", "tag", "short_ttl".
	Action string

	// Rule is the filter rule that matched.
	Rule FilterRule
}

// RuleEngine evaluates filter rules against request metadata. It holds
// rules in memory for fast evaluation. A nil or zero-value RuleEngine
// never matches (preserving existing behavior when no rules are configured).
type RuleEngine struct {
	rules []FilterRule
}

// NewRuleEngine creates a RuleEngine loaded with the given rules. Only
// enabled rules are retained.
func NewRuleEngine(rules []FilterRule) *RuleEngine {
	var enabled []FilterRule
	for _, r := range rules {
		if r.Enabled {
			enabled = append(enabled, r)
		}
	}
	return &RuleEngine{rules: enabled}
}

// Match evaluates all loaded rules against the provided field values.
// It returns the first matching rule's action. If no rule matches,
// matched is false.
func (re *RuleEngine) Match(fields map[string]string) (RuleAction, bool) {
	if re == nil || len(re.rules) == 0 {
		return RuleAction{}, false
	}
	for _, r := range re.rules {
		fieldValue, ok := fields[r.Field]
		if !ok {
			continue
		}
		if matchOperator(r.Operator, fieldValue, r.Value) {
			return RuleAction{Action: r.Action, Rule: r}, true
		}
	}
	return RuleAction{}, false
}

// matchOperator evaluates a single operator against a field value and
// the rule's expected value.
func matchOperator(op, fieldValue, ruleValue string) bool {
	switch op {
	case "equals":
		return fieldValue == ruleValue
	case "contains":
		return strings.Contains(fieldValue, ruleValue)
	case "starts_with":
		return strings.HasPrefix(fieldValue, ruleValue)
	case "matches_glob":
		matched, err := path.Match(ruleValue, fieldValue)
		return err == nil && matched
	default:
		return false
	}
}

// TraceFields extracts the standard field map from a Trace, suitable for
// passing to RuleEngine.Match.
func TraceFields(t Trace) map[string]string {
	return map[string]string{
		"remote_ip":   t.RemoteIP,
		"route":       t.Route,
		"status_code": strings.TrimSpace(StatusCodeStr(t.StatusCode)),
		"method":      t.Method,
		"user_agent":  t.UserAgent,
		"user_id":     t.UserID,
	}
}

// RetentionEngine evaluates retention rules against trace metadata.
// It holds rules in memory for fast evaluation. A nil or zero-value
// RetentionEngine never matches (the default TTL applies).
type RetentionEngine struct {
	rules []RetentionRule
}

// NewRetentionEngine creates a RetentionEngine loaded with the given rules.
// Only enabled rules are retained.
func NewRetentionEngine(rules []RetentionRule) *RetentionEngine {
	var enabled []RetentionRule
	for _, r := range rules {
		if r.Enabled {
			enabled = append(enabled, r)
		}
	}
	return &RetentionEngine{rules: enabled}
}

// HasRules reports whether the engine has any enabled rules loaded.
func (re *RetentionEngine) HasRules() bool {
	return re != nil && len(re.rules) > 0
}

// Match evaluates all loaded retention rules against the provided field values.
// It returns the matching rule with the shortest TTL (most aggressive retention).
// If no rule matches, matched is false.
func (re *RetentionEngine) Match(fields map[string]string) (RetentionRule, bool) {
	if re == nil || len(re.rules) == 0 {
		return RetentionRule{}, false
	}
	var best RetentionRule
	found := false
	for _, r := range re.rules {
		fieldValue, ok := fields[r.Field]
		if !ok {
			continue
		}
		if matchOperator(r.Operator, fieldValue, r.Value) {
			if !found || r.TTLHours < best.TTLHours {
				best = r
				found = true
			}
		}
	}
	return best, found
}

func StatusCodeStr(code int) string {
	// Simple int-to-string without importing strconv to keep deps minimal.
	if code == 0 {
		return "0"
	}
	buf := [10]byte{}
	i := len(buf)
	neg := code < 0
	if neg {
		code = -code
	}
	for code > 0 {
		i--
		buf[i] = byte('0' + code%10)
		code /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
