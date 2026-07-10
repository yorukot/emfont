package middleware

import "net/http"

type SecurityConfig struct {
	ContentSecurityPolicy string
	FrameOptions          string
	ReferrerPolicy        string
	PermissionsPolicy     string
	HSTS                  bool
	DisableHSTS           bool
	HSTSValue             string
}

func DefaultSecurityConfig() SecurityConfig {
	return SecurityConfig{
		ContentSecurityPolicy: "default-src 'self'; script-src 'self' https://cdn.jsdelivr.net; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; connect-src 'self'; frame-ancestors 'none'",
		FrameOptions:          "DENY",
		ReferrerPolicy:        "no-referrer",
		PermissionsPolicy:     "geolocation=(), microphone=(), camera=()",
		HSTS:                  true,
		HSTSValue:             "max-age=31536000; includeSubDomains",
	}
}

func SecurityHeaders(cfg SecurityConfig) func(http.Handler) http.Handler {
	defaults := DefaultSecurityConfig()
	if cfg.ContentSecurityPolicy == "" {
		cfg.ContentSecurityPolicy = defaults.ContentSecurityPolicy
	}
	if cfg.FrameOptions == "" {
		cfg.FrameOptions = defaults.FrameOptions
	}
	if cfg.ReferrerPolicy == "" {
		cfg.ReferrerPolicy = defaults.ReferrerPolicy
	}
	if cfg.PermissionsPolicy == "" {
		cfg.PermissionsPolicy = defaults.PermissionsPolicy
	}
	if cfg.HSTSValue == "" {
		cfg.HSTSValue = defaults.HSTSValue
	}
	if !cfg.DisableHSTS {
		cfg.HSTS = true
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			header := w.Header()
			header.Set("X-Content-Type-Options", "nosniff")
			header.Set("X-Frame-Options", cfg.FrameOptions)
			header.Set("Referrer-Policy", cfg.ReferrerPolicy)
			header.Set("Permissions-Policy", cfg.PermissionsPolicy)
			header.Set("Content-Security-Policy", cfg.ContentSecurityPolicy)
			if cfg.HSTS && !cfg.DisableHSTS {
				header.Set("Strict-Transport-Security", cfg.HSTSValue)
			}

			next.ServeHTTP(w, r)
		})
	}
}
