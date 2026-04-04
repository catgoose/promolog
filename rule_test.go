package promolog

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRuleEngine_NilEngine_NeverMatches(t *testing.T) {
	var re *RuleEngine
	_, matched := re.Match(map[string]string{"method": "GET"})
	assert.False(t, matched)
}

func TestRuleEngine_NoRules_NeverMatches(t *testing.T) {
	re := NewRuleEngine(nil)
	_, matched := re.Match(map[string]string{"method": "GET"})
	assert.False(t, matched)
}

func TestRuleEngine_DisabledRulesAreSkipped(t *testing.T) {
	rules := []FilterRule{
		{ID: 1, Name: "block bots", Field: "user_agent", Operator: "contains", Value: "bot", Action: "suppress", Enabled: false},
	}
	re := NewRuleEngine(rules)
	_, matched := re.Match(map[string]string{"user_agent": "Googlebot/2.1"})
	assert.False(t, matched)
}

func TestRuleEngine_Equals(t *testing.T) {
	rules := []FilterRule{
		{ID: 1, Name: "block IP", Field: "remote_ip", Operator: "equals", Value: "10.0.0.1", Action: "suppress", Enabled: true},
	}
	re := NewRuleEngine(rules)

	action, matched := re.Match(map[string]string{"remote_ip": "10.0.0.1"})
	assert.True(t, matched)
	assert.Equal(t, "suppress", action.Action)
	assert.Equal(t, 1, action.Rule.ID)

	_, matched = re.Match(map[string]string{"remote_ip": "10.0.0.2"})
	assert.False(t, matched)
}

func TestRuleEngine_Contains(t *testing.T) {
	rules := []FilterRule{
		{ID: 1, Name: "block bots", Field: "user_agent", Operator: "contains", Value: "bot", Action: "suppress", Enabled: true},
	}
	re := NewRuleEngine(rules)

	action, matched := re.Match(map[string]string{"user_agent": "Googlebot/2.1"})
	assert.True(t, matched)
	assert.Equal(t, "suppress", action.Action)

	_, matched = re.Match(map[string]string{"user_agent": "Mozilla/5.0"})
	assert.False(t, matched)
}

func TestRuleEngine_StartsWith(t *testing.T) {
	rules := []FilterRule{
		{ID: 1, Name: "health routes", Field: "route", Operator: "starts_with", Value: "/health", Action: "suppress", Enabled: true},
	}
	re := NewRuleEngine(rules)

	_, matched := re.Match(map[string]string{"route": "/healthz"})
	assert.True(t, matched)

	_, matched = re.Match(map[string]string{"route": "/api/health"})
	assert.False(t, matched)
}

func TestRuleEngine_MatchesGlob(t *testing.T) {
	rules := []FilterRule{
		{ID: 1, Name: "api routes", Field: "route", Operator: "matches_glob", Value: "/api/*", Action: "always_promote", Enabled: true},
	}
	re := NewRuleEngine(rules)

	action, matched := re.Match(map[string]string{"route": "/api/users"})
	assert.True(t, matched)
	assert.Equal(t, "always_promote", action.Action)

	_, matched = re.Match(map[string]string{"route": "/web/home"})
	assert.False(t, matched)
}

func TestRuleEngine_FirstMatchWins(t *testing.T) {
	rules := []FilterRule{
		{ID: 1, Name: "suppress bots", Field: "user_agent", Operator: "contains", Value: "bot", Action: "suppress", Enabled: true},
		{ID: 2, Name: "promote all", Field: "method", Operator: "equals", Value: "GET", Action: "always_promote", Enabled: true},
	}
	re := NewRuleEngine(rules)

	action, matched := re.Match(map[string]string{"user_agent": "Googlebot", "method": "GET"})
	assert.True(t, matched)
	assert.Equal(t, "suppress", action.Action)
	assert.Equal(t, 1, action.Rule.ID)
}

func TestRuleEngine_FieldNotPresent_Skips(t *testing.T) {
	rules := []FilterRule{
		{ID: 1, Name: "check user", Field: "user_id", Operator: "equals", Value: "admin", Action: "tag", Enabled: true},
	}
	re := NewRuleEngine(rules)

	// Field not in map — should not match
	_, matched := re.Match(map[string]string{"method": "GET"})
	assert.False(t, matched)
}

func TestRuleEngine_UnknownOperator_NeverMatches(t *testing.T) {
	rules := []FilterRule{
		{ID: 1, Name: "bad op", Field: "method", Operator: "regex", Value: ".*", Action: "suppress", Enabled: true},
	}
	re := NewRuleEngine(rules)

	_, matched := re.Match(map[string]string{"method": "GET"})
	assert.False(t, matched)
}

func TestRuleEngine_StatusCode(t *testing.T) {
	rules := []FilterRule{
		{ID: 1, Name: "suppress 200", Field: "status_code", Operator: "equals", Value: "200", Action: "suppress", Enabled: true},
	}
	re := NewRuleEngine(rules)

	action, matched := re.Match(map[string]string{"status_code": "200"})
	assert.True(t, matched)
	assert.Equal(t, "suppress", action.Action)
}

func TestRuleEngine_AllActions(t *testing.T) {
	actions := []string{"suppress", "always_promote", "tag", "short_ttl"}
	for _, a := range actions {
		rules := []FilterRule{
			{ID: 1, Name: "test " + a, Field: "method", Operator: "equals", Value: "POST", Action: a, Enabled: true},
		}
		re := NewRuleEngine(rules)
		action, matched := re.Match(map[string]string{"method": "POST"})
		assert.True(t, matched, "action %s should match", a)
		assert.Equal(t, a, action.Action)
	}
}

// --- RetentionEngine tests ---

func TestRetentionEngine_NilEngine_NeverMatches(t *testing.T) {
	var re *RetentionEngine
	_, matched := re.Match(map[string]string{"method": "GET"})
	assert.False(t, matched)
	assert.False(t, re.HasRules())
}

func TestRetentionEngine_NoRules_NeverMatches(t *testing.T) {
	re := NewRetentionEngine(nil)
	_, matched := re.Match(map[string]string{"method": "GET"})
	assert.False(t, matched)
	assert.False(t, re.HasRules())
}

func TestRetentionEngine_DisabledRulesAreSkipped(t *testing.T) {
	rules := []RetentionRule{
		{ID: 1, Name: "short health", Field: "route", Operator: "equals", Value: "/health", TTLHours: 1, Enabled: false},
	}
	re := NewRetentionEngine(rules)
	_, matched := re.Match(map[string]string{"route": "/health"})
	assert.False(t, matched)
	assert.False(t, re.HasRules())
}

func TestRetentionEngine_BasicMatch(t *testing.T) {
	rules := []RetentionRule{
		{ID: 1, Name: "short health", Field: "route", Operator: "equals", Value: "/health", TTLHours: 1, Enabled: true},
	}
	re := NewRetentionEngine(rules)
	assert.True(t, re.HasRules())

	rule, matched := re.Match(map[string]string{"route": "/health"})
	assert.True(t, matched)
	assert.Equal(t, 1, rule.TTLHours)
	assert.Equal(t, 1, rule.ID)

	_, matched = re.Match(map[string]string{"route": "/api/users"})
	assert.False(t, matched)
}

func TestRetentionEngine_ShortestTTLWins(t *testing.T) {
	rules := []RetentionRule{
		{ID: 1, Name: "longer", Field: "method", Operator: "equals", Value: "GET", TTLHours: 24, Enabled: true},
		{ID: 2, Name: "shorter", Field: "route", Operator: "equals", Value: "/health", TTLHours: 1, Enabled: true},
	}
	re := NewRetentionEngine(rules)

	// Both rules match; shortest TTL (1h) should win.
	rule, matched := re.Match(map[string]string{"method": "GET", "route": "/health"})
	assert.True(t, matched)
	assert.Equal(t, 1, rule.TTLHours)
	assert.Equal(t, 2, rule.ID)
}

func TestRetentionEngine_AllOperators(t *testing.T) {
	tests := []struct {
		op    string
		value string
		input string
		match bool
	}{
		{"equals", "/health", "/health", true},
		{"equals", "/health", "/healthz", false},
		{"contains", "health", "/api/health/check", true},
		{"starts_with", "/api", "/api/users", true},
		{"starts_with", "/api", "/web/api", false},
		{"matches_glob", "/api/*", "/api/users", true},
		{"matches_glob", "/api/*", "/web/home", false},
	}
	for _, tc := range tests {
		rules := []RetentionRule{
			{ID: 1, Name: "test", Field: "route", Operator: tc.op, Value: tc.value, TTLHours: 1, Enabled: true},
		}
		re := NewRetentionEngine(rules)
		_, matched := re.Match(map[string]string{"route": tc.input})
		assert.Equal(t, tc.match, matched, "op=%s value=%s input=%s", tc.op, tc.value, tc.input)
	}
}

func TestTraceFields(t *testing.T) {
	tr := Trace{
		RemoteIP:   "192.168.1.1",
		Route:      "/api/test",
		StatusCode: 503,
		Method:     "PUT",
		UserAgent:  "TestAgent",
		UserID:     "user-99",
	}
	fields := TraceFields(tr)
	assert.Equal(t, "192.168.1.1", fields["remote_ip"])
	assert.Equal(t, "/api/test", fields["route"])
	assert.Equal(t, "503", fields["status_code"])
	assert.Equal(t, "PUT", fields["method"])
	assert.Equal(t, "TestAgent", fields["user_agent"])
	assert.Equal(t, "user-99", fields["user_id"])
}
