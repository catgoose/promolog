package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/catgoose/promolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sampleRetentionRule() promolog.RetentionRule {
	return promolog.RetentionRule{
		Name:     "short-lived health checks",
		Field:    "route",
		Operator: "starts_with",
		Value:    "/health",
		TTLHours: 1,
		Enabled:  true,
	}
}

// --- CRUD tests ---

func TestCreateRetentionRule_ReturnsIDAndTimestamp(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	created, err := store.CreateRetentionRule(ctx, sampleRetentionRule())
	require.NoError(t, err)
	assert.Greater(t, created.ID, 0)
	assert.False(t, created.CreatedAt.IsZero())
	assert.Equal(t, "short-lived health checks", created.Name)
	assert.Equal(t, "route", created.Field)
	assert.Equal(t, 1, created.TTLHours)
	assert.True(t, created.Enabled)
}

func TestListRetentionRules_ReturnsAll(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	r1 := sampleRetentionRule()
	r2 := promolog.RetentionRule{
		Name: "keep 5xx longer", Field: "status_code", Operator: "starts_with",
		Value: "5", TTLHours: 720, Enabled: true,
	}

	_, err := store.CreateRetentionRule(ctx, r1)
	require.NoError(t, err)
	_, err = store.CreateRetentionRule(ctx, r2)
	require.NoError(t, err)

	rules, err := store.ListRetentionRules(ctx)
	require.NoError(t, err)
	assert.Len(t, rules, 2)
	assert.Equal(t, "short-lived health checks", rules[0].Name)
	assert.Equal(t, "keep 5xx longer", rules[1].Name)
}

func TestListRetentionRules_Empty(t *testing.T) {
	store := newTestStore(t)
	rules, err := store.ListRetentionRules(context.Background())
	require.NoError(t, err)
	assert.Empty(t, rules)
}

func TestUpdateRetentionRule(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	created, err := store.CreateRetentionRule(ctx, sampleRetentionRule())
	require.NoError(t, err)

	created.Name = "updated name"
	created.TTLHours = 48
	created.Enabled = false
	require.NoError(t, store.UpdateRetentionRule(ctx, created))

	rules, err := store.ListRetentionRules(ctx)
	require.NoError(t, err)
	require.Len(t, rules, 1)
	assert.Equal(t, "updated name", rules[0].Name)
	assert.Equal(t, 48, rules[0].TTLHours)
	assert.False(t, rules[0].Enabled)
}

func TestUpdateRetentionRule_NotFound(t *testing.T) {
	store := newTestStore(t)
	rule := sampleRetentionRule()
	rule.ID = 999
	err := store.UpdateRetentionRule(context.Background(), rule)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "999")
}

func TestDeleteRetentionRule(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	created, err := store.CreateRetentionRule(ctx, sampleRetentionRule())
	require.NoError(t, err)

	require.NoError(t, store.DeleteRetentionRule(ctx, created.ID))

	rules, err := store.ListRetentionRules(ctx)
	require.NoError(t, err)
	assert.Empty(t, rules)
}

func TestDeleteRetentionRule_NotFound(t *testing.T) {
	store := newTestStore(t)
	err := store.DeleteRetentionRule(context.Background(), 999)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "999")
}

// --- LoadRetentionEngine tests ---

func TestLoadRetentionEngine(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	_, err := store.CreateRetentionRule(ctx, promolog.RetentionRule{
		Name: "short health", Field: "route", Operator: "equals",
		Value: "/health", TTLHours: 1, Enabled: true,
	})
	require.NoError(t, err)
	_, err = store.CreateRetentionRule(ctx, promolog.RetentionRule{
		Name: "disabled rule", Field: "method", Operator: "equals",
		Value: "DELETE", TTLHours: 2, Enabled: false,
	})
	require.NoError(t, err)

	engine, err := store.LoadRetentionEngine(ctx)
	require.NoError(t, err)

	// Enabled rule should match.
	rule, matched := engine.Match(map[string]string{"route": "/health"})
	assert.True(t, matched)
	assert.Equal(t, 1, rule.TTLHours)

	// Disabled rule should not match.
	_, matched = engine.Match(map[string]string{"method": "DELETE"})
	assert.False(t, matched)
}

func TestLoadRetentionEngine_NoRules(t *testing.T) {
	store := newTestStore(t)
	engine, err := store.LoadRetentionEngine(context.Background())
	require.NoError(t, err)
	assert.False(t, engine.HasRules())

	_, matched := engine.Match(map[string]string{"method": "GET"})
	assert.False(t, matched)
}

// --- Cleanup with retention rules ---

func TestStartCleanup_UsesRetentionRules(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	cleanupCtx, cleanupCancel := context.WithCancel(ctx)
	defer cleanupCancel()

	// Create a retention rule: health check traces expire after 1 hour.
	_, err := store.CreateRetentionRule(ctx, promolog.RetentionRule{
		Name: "short health", Field: "route", Operator: "equals",
		Value: "/health", TTLHours: 1, Enabled: true,
	})
	require.NoError(t, err)

	// Insert a health check trace 2 hours old (should be deleted by retention rule).
	healthTrace := sampleTrace("req-health", 200, "GET")
	healthTrace.Route = "/health"
	require.NoError(t, store.PromoteAt(ctx, healthTrace, time.Now().Add(-2*time.Hour)))

	// Insert an API trace 2 hours old (should survive with 24h default TTL).
	apiTrace := sampleTrace("req-api", 500, "GET")
	apiTrace.Route = "/api/users"
	require.NoError(t, store.PromoteAt(ctx, apiTrace, time.Now().Add(-2*time.Hour)))

	// Insert a fresh health check trace (should survive even with 1h rule).
	freshHealth := sampleTrace("req-health-fresh", 200, "GET")
	freshHealth.Route = "/health"
	require.NoError(t, store.Promote(ctx, freshHealth))

	store.StartCleanup(cleanupCtx, 24*time.Hour, 50*time.Millisecond)
	time.Sleep(200 * time.Millisecond)

	// Old health check should be deleted (2h old > 1h TTL).
	got, err := store.Get(ctx, "req-health")
	require.NoError(t, err)
	assert.Nil(t, got, "old health trace should be deleted by retention rule")

	// API trace should survive (2h old < 24h default TTL).
	got, err = store.Get(ctx, "req-api")
	require.NoError(t, err)
	assert.NotNil(t, got, "API trace should survive with default TTL")

	// Fresh health check should survive (< 1h old).
	got, err = store.Get(ctx, "req-health-fresh")
	require.NoError(t, err)
	assert.NotNil(t, got, "fresh health trace should survive")
}

func TestStartCleanup_NoRetentionRules_UsesDefaultTTL(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	cleanupCtx, cleanupCancel := context.WithCancel(ctx)
	defer cleanupCancel()

	// No retention rules configured.
	old := time.Now().Add(-48 * time.Hour)
	require.NoError(t, store.PromoteAt(ctx, sampleTrace("req-old", 500, "GET"), old))
	require.NoError(t, store.Promote(ctx, sampleTrace("req-new", 500, "GET")))

	store.StartCleanup(cleanupCtx, 24*time.Hour, 50*time.Millisecond)
	time.Sleep(200 * time.Millisecond)

	got, err := store.Get(ctx, "req-old")
	require.NoError(t, err)
	assert.Nil(t, got, "expired trace should be deleted")

	got, err = store.Get(ctx, "req-new")
	require.NoError(t, err)
	assert.NotNil(t, got, "fresh trace should survive")
}

func TestStartCleanup_RetentionRuleShortestTTLWins(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	cleanupCtx, cleanupCancel := context.WithCancel(ctx)
	defer cleanupCancel()

	// Two rules matching the same trace: 6h and 2h. Shortest (2h) should win.
	_, err := store.CreateRetentionRule(ctx, promolog.RetentionRule{
		Name: "method rule", Field: "method", Operator: "equals",
		Value: "GET", TTLHours: 6, Enabled: true,
	})
	require.NoError(t, err)
	_, err = store.CreateRetentionRule(ctx, promolog.RetentionRule{
		Name: "route rule", Field: "route", Operator: "equals",
		Value: "/health", TTLHours: 2, Enabled: true,
	})
	require.NoError(t, err)

	// Trace is 3 hours old, matching both rules.
	trace := sampleTrace("req-multi-match", 200, "GET")
	trace.Route = "/health"
	require.NoError(t, store.PromoteAt(ctx, trace, time.Now().Add(-3*time.Hour)))

	store.StartCleanup(cleanupCtx, 24*time.Hour, 50*time.Millisecond)
	time.Sleep(200 * time.Millisecond)

	// Should be deleted: 3h old > 2h shortest TTL.
	got, err := store.Get(ctx, "req-multi-match")
	require.NoError(t, err)
	assert.Nil(t, got, "trace should be deleted using shortest matching TTL")
}

// --- Schema idempotency ---

func TestInitSchema_RetentionRulesIdempotent(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db)
	require.NoError(t, store.InitSchema())
	require.NoError(t, store.InitSchema())

	_, err := store.CreateRetentionRule(context.Background(), sampleRetentionRule())
	require.NoError(t, err)
}
