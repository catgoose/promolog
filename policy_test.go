package promolog

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// --- StatusPolicy ---

func TestStatusPolicy_MatchesAtThreshold(t *testing.T) {
	p := StatusPolicy(500)
	r := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	assert.True(t, p.Predicate(r, 500))
}

func TestStatusPolicy_MatchesAboveThreshold(t *testing.T) {
	p := StatusPolicy(500)
	r := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	assert.True(t, p.Predicate(r, 503))
}

func TestStatusPolicy_DoesNotMatchBelowThreshold(t *testing.T) {
	p := StatusPolicy(500)
	r := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	assert.False(t, p.Predicate(r, 499))
	assert.False(t, p.Predicate(r, 200))
}

func TestStatusPolicy_CustomThreshold(t *testing.T) {
	p := StatusPolicy(400)
	r := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	assert.True(t, p.Predicate(r, 400))
	assert.True(t, p.Predicate(r, 500))
	assert.False(t, p.Predicate(r, 399))
}

func TestStatusPolicy_Name(t *testing.T) {
	p := StatusPolicy(500)
	assert.Equal(t, "status", p.Name)
}

// --- RoutePolicy ---

func TestRoutePolicy_MatchesExactPath(t *testing.T) {
	p := RoutePolicy("/api/users", func(code int) bool { return code >= 400 })
	r := httptest.NewRequest(http.MethodGet, "/api/users", http.NoBody)
	assert.True(t, p.Predicate(r, 500))
}

func TestRoutePolicy_MatchesWildcard(t *testing.T) {
	p := RoutePolicy("/api/*", func(code int) bool { return code >= 500 })

	r1 := httptest.NewRequest(http.MethodGet, "/api/users", http.NoBody)
	assert.True(t, p.Predicate(r1, 500))

	r2 := httptest.NewRequest(http.MethodGet, "/api/orders", http.NoBody)
	assert.True(t, p.Predicate(r2, 502))
}

func TestRoutePolicy_DoesNotMatchDifferentPath(t *testing.T) {
	p := RoutePolicy("/api/*", func(code int) bool { return code >= 500 })
	r := httptest.NewRequest(http.MethodGet, "/health", http.NoBody)
	assert.False(t, p.Predicate(r, 500))
}

func TestRoutePolicy_DoesNotMatchWhenPredicateFails(t *testing.T) {
	p := RoutePolicy("/api/*", func(code int) bool { return code >= 500 })
	r := httptest.NewRequest(http.MethodGet, "/api/users", http.NoBody)
	assert.False(t, p.Predicate(r, 200))
}

func TestRoutePolicy_Name(t *testing.T) {
	p := RoutePolicy("/api/*", func(int) bool { return true })
	assert.Equal(t, "route:/api/*", p.Name)
}

func TestRoutePolicy_InvalidPattern(t *testing.T) {
	// path.Match returns an error for patterns with unclosed brackets
	p := RoutePolicy("[invalid", func(int) bool { return true })
	r := httptest.NewRequest(http.MethodGet, "/anything", http.NoBody)
	assert.False(t, p.Predicate(r, 500))
}
