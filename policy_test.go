package promolog

import (
	"context"
	"math"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

// --- SamplePolicy ---

func TestSamplePolicy_AlwaysPromotesAtRate1(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	p := SamplePolicy(1.0, rng)
	r := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	for i := 0; i < 100; i++ {
		assert.True(t, p.Predicate(r, 200))
	}
}

func TestSamplePolicy_NeverPromotesAtRate0(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	p := SamplePolicy(0.0, rng)
	r := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	for i := 0; i < 100; i++ {
		assert.False(t, p.Predicate(r, 200))
	}
}

func TestSamplePolicy_ApproximateRate(t *testing.T) {
	rng := rand.New(rand.NewSource(99))
	p := SamplePolicy(0.5, rng)
	r := httptest.NewRequest(http.MethodGet, "/", http.NoBody)

	hits := 0
	n := 10000
	for i := 0; i < n; i++ {
		if p.Predicate(r, 200) {
			hits++
		}
	}
	rate := float64(hits) / float64(n)
	assert.InDelta(t, 0.5, rate, 0.05)
}

func TestSamplePolicy_Deterministic(t *testing.T) {
	results1 := make([]bool, 20)
	results2 := make([]bool, 20)
	r := httptest.NewRequest(http.MethodGet, "/", http.NoBody)

	p1 := SamplePolicy(0.3, rand.New(rand.NewSource(7)))
	for i := range results1 {
		results1[i] = p1.Predicate(r, 200)
	}

	p2 := SamplePolicy(0.3, rand.New(rand.NewSource(7)))
	for i := range results2 {
		results2[i] = p2.Predicate(r, 200)
	}

	assert.Equal(t, results1, results2)
}

func TestSamplePolicy_Name(t *testing.T) {
	p := SamplePolicy(0.01, nil)
	assert.Equal(t, "sample:0.0100", p.Name)
}

func TestSamplePolicy_PanicsOnNegativeRate(t *testing.T) {
	assert.PanicsWithValue(t,
		"promolog: SamplePolicy rate must be in [0, 1], got -0.1",
		func() { _ = SamplePolicy(-0.1, nil) },
	)
}

func TestSamplePolicy_PanicsOnRateAboveOne(t *testing.T) {
	assert.PanicsWithValue(t,
		"promolog: SamplePolicy rate must be in [0, 1], got 1.5",
		func() { _ = SamplePolicy(1.5, nil) },
	)
}

func TestSamplePolicy_PanicsOnNaN(t *testing.T) {
	assert.Panics(t, func() { _ = SamplePolicy(math.NaN(), nil) })
}

func TestSamplePolicy_BoundaryRatesDoNotPanic(t *testing.T) {
	assert.NotPanics(t, func() { _ = SamplePolicy(0, nil) })
	assert.NotPanics(t, func() { _ = SamplePolicy(1, nil) })
}

// --- LatencyPolicy ---

func TestLatencyPolicy_PromotesSlowRequest(t *testing.T) {
	p := LatencyPolicy(50 * time.Millisecond)
	// Simulate a request that started 100ms ago.
	ctx := context.WithValue(context.Background(), startTimeKey, time.Now().Add(-100*time.Millisecond))
	r := httptest.NewRequest(http.MethodGet, "/", http.NoBody).WithContext(ctx)
	assert.True(t, p.Predicate(r, 200))
}

func TestLatencyPolicy_DoesNotPromoteFastRequest(t *testing.T) {
	p := LatencyPolicy(500 * time.Millisecond)
	// Simulate a request that just started.
	ctx := context.WithValue(context.Background(), startTimeKey, time.Now())
	r := httptest.NewRequest(http.MethodGet, "/", http.NoBody).WithContext(ctx)
	assert.False(t, p.Predicate(r, 200))
}

func TestLatencyPolicy_NoStartTime(t *testing.T) {
	p := LatencyPolicy(50 * time.Millisecond)
	r := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	assert.False(t, p.Predicate(r, 200))
}

func TestLatencyPolicy_Name(t *testing.T) {
	p := LatencyPolicy(100 * time.Millisecond)
	assert.Equal(t, "latency:100ms", p.Name)
}
