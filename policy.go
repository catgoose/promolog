package promolog

import (
	"net/http"
	"path"
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
