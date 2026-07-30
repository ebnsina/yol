package httpx

import (
	"net/http"
	"strconv"
	"time"
)

const corsMaxAge = 12 * time.Hour

// CORS allows exactly one origin with credentials. A wildcard is impossible here by
// design: browsers refuse to send cookies to a wildcard origin, and echoing back whatever
// origin asked would let any site make authenticated requests on a user's behalf.
func CORS(allowedOrigin string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin == allowedOrigin {
				h := w.Header()
				h.Set("Access-Control-Allow-Origin", allowedOrigin)
				h.Set("Access-Control-Allow-Credentials", "true")
				h.Set("Vary", "Origin")

				if r.Method == http.MethodOptions {
					h.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
					h.Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
					h.Set("Access-Control-Max-Age", strconv.Itoa(int(corsMaxAge.Seconds())))
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
