package middleware

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/emfont/emfont/backend/internal/controller/transport/http/httpx"
)

type CORSConfig struct {
	AllowedOrigins      []string
	MaxAge              time.Duration
	PreflightMiddleware func(http.Handler) http.Handler
}

func CORS(cfg CORSConfig) func(http.Handler) http.Handler {
	allowed := make(map[string]struct{}, len(cfg.AllowedOrigins))
	wildcard := false
	for _, origin := range cfg.AllowedOrigins {
		origin = strings.TrimSpace(origin)
		if origin == "*" {
			wildcard = true
			continue
		}
		if origin != "" {
			allowed[origin] = struct{}{}
		}
	}
	if cfg.MaxAge <= 0 {
		cfg.MaxAge = 10 * time.Minute
	}
	maxAge := strconv.FormatInt(int64(cfg.MaxAge/time.Second), 10)
	allowedPreflight := http.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Request-ID")
		w.Header().Set("Access-Control-Max-Age", maxAge)
		w.WriteHeader(http.StatusNoContent)
	}))
	deniedPreflight := http.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		WriteProblemCode(w, r, http.StatusForbidden, httpx.CodeForbidden, "origin is not allowed")
	}))
	if cfg.PreflightMiddleware != nil {
		allowedPreflight = cfg.PreflightMiddleware(allowedPreflight)
		deniedPreflight = cfg.PreflightMiddleware(deniedPreflight)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := strings.TrimSpace(r.Header.Get("Origin"))
			if origin == "" {
				next.ServeHTTP(w, r)
				return
			}

			_, explicitlyAllowed := allowed[origin]
			originAllowed := wildcard || explicitlyAllowed
			addVary(w.Header(), "Origin")
			if !originAllowed {
				if isPreflight(r) {
					deniedPreflight.ServeHTTP(w, r)
					return
				}
				next.ServeHTTP(w, r)
				return
			}

			if wildcard {
				w.Header().Set("Access-Control-Allow-Origin", "*")
			} else {
				w.Header().Set("Access-Control-Allow-Origin", origin)
			}
			w.Header().Set("Access-Control-Expose-Headers", "X-Request-ID, X-Next-Cursor, Retry-After, Link, Allow")
			if isPreflight(r) {
				addVary(w.Header(), "Access-Control-Request-Method")
				addVary(w.Header(), "Access-Control-Request-Headers")
				allowedPreflight.ServeHTTP(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func isPreflight(r *http.Request) bool {
	return r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""
}

func addVary(header http.Header, value string) {
	for _, existing := range header.Values("Vary") {
		for _, field := range strings.Split(existing, ",") {
			if strings.EqualFold(strings.TrimSpace(field), value) {
				return
			}
		}
	}
	header.Add("Vary", value)
}
