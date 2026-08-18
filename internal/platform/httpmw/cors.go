package httpmw

import (
	"net/http"
	"strings"
)

// CORS returns a middleware configuring Cross-Origin Resource Sharing.
func CORS(allowedOrigins []string, allowCredentials bool) func(http.Handler) http.Handler {
	originSet := make(map[string]bool, len(allowedOrigins))
	for _, o := range allowedOrigins {
		clean := strings.TrimRight(strings.TrimSpace(o), "/")
		if clean != "" {
			originSet[clean] = true
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			cleanOrigin := strings.TrimRight(strings.TrimSpace(origin), "/")

			if cleanOrigin != "" && originSet[cleanOrigin] {
				// Specific allowed origin
				w.Header().Set("Access-Control-Allow-Origin", origin)
				if allowCredentials {
					// Boundary checklist: never reflect * when credentials enabled
					w.Header().Set("Access-Control-Allow-Credentials", "true")
				}
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Accept, Authorization, Content-Type, X-Signature, X-Correlation-ID")
				w.Header().Set("Access-Control-Max-Age", "86400")

				if r.Method == http.MethodOptions {
					w.WriteHeader(http.StatusNoContent)
					return
				}
			} else if r.Method == http.MethodOptions && cleanOrigin != "" {
				// Origin not allowed on preflight
				w.WriteHeader(http.StatusForbidden)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
