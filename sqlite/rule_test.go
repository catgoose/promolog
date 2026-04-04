package sqlite

import (
	"context"
	"testing"

	"github.com/catgoose/promolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sampleRule() promolog.FilterRule {
	return promolog.FilterRule{
		Name:     "block bots",
		Field:    "user_agent",
		Operator: "contains",
		Value:    "bot",
		Action:   "suppress",
		Enabled:  true,
	}
}

func TestCreateRule_ReturnsIDAndTimestamp(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	created, err := store.CreateRule(ctx, sampleRule())
	require.NoError(t, err)
	assert.Greater(t, created.ID, 0)
	assert.False(t, created.CreatedAt.IsZero())
	assert.Equal(t, "block bots", created.Name)
	assert.Equal(t, "user_agent", created.Field)
	assert.True(t, created.Enabled)
}

func TestListRules_ReturnsAllRules(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	r1 := sampleRule()
	r2 := sampleRule()
	r2.Name = "suppress health"
	r2.Field = "route"
	r2.Operator = "starts_with"
	r2.Value = "/health"

	_, err := store.CreateRule(ctx, r1)
	require.NoError(t, err)
	_, err = store.CreateRule(ctx, r2)
	require.NoError(t, err)

	rules, err := store.ListRules(ctx)
	require.NoError(t, err)
	assert.Len(t, rules, 2)
	assert.Equal(t, "block bots", rules[0].Name)
	assert.Equal(t, "suppress health", rules[1].Name)
}

func TestListRules_Empty(t *testing.T) {
	store := newTestStore(t)
	rules, err := store.ListRules(context.Background())
	require.NoError(t, err)
	assert.Empty(t, rules)
}

func TestUpdateRule(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	created, err := store.CreateRule(ctx, sampleRule())
	require.NoError(t, err)

	created.Name = "updated name"
	created.Enabled = false
	created.Value = "crawler"
	require.NoError(t, store.UpdateRule(ctx, created))

	rules, err := store.ListRules(ctx)
	require.NoError(t, err)
	require.Len(t, rules, 1)
	assert.Equal(t, "updated name", rules[0].Name)
	assert.False(t, rules[0].Enabled)
	assert.Equal(t, "crawler", rules[0].Value)
}

func TestUpdateRule_NotFound(t *testing.T) {
	store := newTestStore(t)
	rule := sampleRule()
	rule.ID = 999
	err := store.UpdateRule(context.Background(), rule)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "999")
}

func TestDeleteRule(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	created, err := store.CreateRule(ctx, sampleRule())
	require.NoError(t, err)

	require.NoError(t, store.DeleteRule(ctx, created.ID))

	rules, err := store.ListRules(ctx)
	require.NoError(t, err)
	assert.Empty(t, rules)
}

func TestDeleteRule_NotFound(t *testing.T) {
	store := newTestStore(t)
	err := store.DeleteRule(context.Background(), 999)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "999")
}

func TestLoadRuleEngine(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	_, err := store.CreateRule(ctx, promolog.FilterRule{
		Name: "block IP", Field: "remote_ip", Operator: "equals",
		Value: "10.0.0.1", Action: "suppress", Enabled: true,
	})
	require.NoError(t, err)
	_, err = store.CreateRule(ctx, promolog.FilterRule{
		Name: "disabled rule", Field: "method", Operator: "equals",
		Value: "DELETE", Action: "suppress", Enabled: false,
	})
	require.NoError(t, err)

	engine, err := store.LoadRuleEngine(ctx)
	require.NoError(t, err)

	// Enabled rule should match.
	action, matched := engine.Match(map[string]string{"remote_ip": "10.0.0.1"})
	assert.True(t, matched)
	assert.Equal(t, "suppress", action.Action)

	// Disabled rule should not match.
	_, matched = engine.Match(map[string]string{"method": "DELETE"})
	assert.False(t, matched)
}

func TestLoadRuleEngine_NoRules(t *testing.T) {
	store := newTestStore(t)
	engine, err := store.LoadRuleEngine(context.Background())
	require.NoError(t, err)

	_, matched := engine.Match(map[string]string{"method": "GET"})
	assert.False(t, matched)
}

func TestInitSchema_FilterRulesIdempotent(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db)
	require.NoError(t, store.InitSchema())
	require.NoError(t, store.InitSchema())

	// Verify we can use filter_rules after double init.
	_, err := store.CreateRule(context.Background(), sampleRule())
	require.NoError(t, err)
}

func TestCreateRule_AllFields(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	fields := []string{"remote_ip", "route", "status_code", "method", "user_agent", "user_id"}
	operators := []string{"equals", "contains", "starts_with", "matches_glob"}
	actions := []string{"suppress", "always_promote", "tag", "short_ttl"}

	for i, f := range fields {
		rule := promolog.FilterRule{
			Name:     "rule-" + f,
			Field:    f,
			Operator: operators[i%len(operators)],
			Value:    "test-value",
			Action:   actions[i%len(actions)],
			Enabled:  true,
		}
		created, err := store.CreateRule(ctx, rule)
		require.NoError(t, err)
		assert.Equal(t, f, created.Field)
	}

	rules, err := store.ListRules(ctx)
	require.NoError(t, err)
	assert.Len(t, rules, len(fields))
}
