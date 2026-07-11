package middleware

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

func BearerToken(token string) func(http.Handler) http.Handler {
	return BearerTokenWithRateLimits(token, nil, nil)
}

// BearerTokenWithRateLimits authenticates before selecting independent limits
// for accepted traffic and rejected authentication attempts.
func BearerTokenWithRateLimits(
	token string,
	authenticatedLimit func(http.Handler) http.Handler,
	rejectedLimit func(http.Handler) http.Handler,
) func(http.Handler) http.Handler {
	expected := []byte(strings.TrimSpace(token))
	return func(next http.Handler) http.Handler {
		authorized := next
		if authenticatedLimit != nil {
			authorized = authenticatedLimit(authorized)
		}
		unauthorized := http.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="metrics"`)
			WriteProblem(w, r, http.StatusUnauthorized, "authentication required")
		}))
		if rejectedLimit != nil {
			unauthorized = rejectedLimit(unauthorized)
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			provided := strings.TrimSpace(r.Header.Get("Authorization"))
			value, ok := strings.CutPrefix(provided, "Bearer ")
			if len(expected) == 0 || !ok || subtle.ConstantTimeCompare([]byte(value), expected) != 1 {
				unauthorized.ServeHTTP(w, r)
				return
			}
			authorized.ServeHTTP(w, r)
		})
	}
}
