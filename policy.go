package promolog

import (
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"path"
	"time"
)

// PromotionPolicy decides whether a completed request's buffer should be
// promoted to the store. Policies are evaluated by AutoPromoteMiddleware after
// the downstream handler returns.
type PromotionPolicy struct {
	// Name is a human-readable label for debugging / logging.
	Name string

	// Predicate returns true when the request should be promoted.
	Predicate func(r *http.Request, statusCode int) bool
}

// StatusPolicy returns a PromotionPolicy that promotes when the response
// status code is >= minCode. A typical default is 500 (server errors only).
func StatusPolicy(minCode int) PromotionPolicy {
	return PromotionPolicy{
		Name: "status",
		Predicate: func(_ *http.Request, statusCode int) bool {
			return statusCode >= minCode
		},
	}
}

// RoutePolicy returns a PromotionPolicy that promotes when the request path
// matches pattern (using path.Match) and predicate reports true for the
// response status code. pattern follows the same rules as path.Match
// (e.g. "/api/*").
func RoutePolicy(pattern string, predicate func(statusCode int) bool) PromotionPolicy {
	return PromotionPolicy{
		Name: "route:" + pattern,
		Predicate: func(r *http.Request, statusCode int) bool {
			matched, err := path.Match(pattern, r.URL.Path)
			if err != nil {
				return false
			}
			return matched && predicate(statusCode)
		},
	}
}

// SamplePolicy returns a PromotionPolicy that promotes a random fraction of
// requests. rate must be in the closed interval [0, 1] (e.g. 0.01 = 1%).
// Values outside that range, or NaN, panic at construction time because they
// indicate a configuration bug that should fail loudly rather than silently
// misbehave. An optional *rand.Rand source can be provided for deterministic
// testing; when nil a default source is used.
func SamplePolicy(rate float64, rng *rand.Rand) PromotionPolicy {
	if math.IsNaN(rate) || rate < 0 || rate > 1 {
		panic(fmt.Sprintf("promolog: SamplePolicy rate must be in [0, 1], got %v", rate))
	}
	if rng == nil {
		rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	return PromotionPolicy{
		Name: fmt.Sprintf("sample:%.4f", rate),
		Predicate: func(_ *http.Request, _ int) bool {
			return rng.Float64() < rate
		},
	}
}

// LatencyPolicy returns a PromotionPolicy that promotes any request whose
// duration exceeds threshold. It reads the request start time stored in the
// context by CorrelationMiddleware; if no start time is present the predicate
// returns false.
func LatencyPolicy(threshold time.Duration) PromotionPolicy {
	return PromotionPolicy{
		Name: fmt.Sprintf("latency:%s", threshold),
		Predicate: func(r *http.Request, _ int) bool {
			start := GetRequestStartTime(r.Context())
			if start.IsZero() {
				return false
			}
			return time.Since(start) >= threshold
		},
	}
}
