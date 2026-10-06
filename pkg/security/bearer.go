package security

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

func IsBearerAuthorized(r *http.Request, token string) bool {
	if len(token) < 32 {
		return false
	}

	provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if len(provided) != len(token) {
		return false
	}

	return subtle.ConstantTimeCompare([]byte(provided), []byte(token)) == 1
}

func RequireBearer(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !IsBearerAuthorized(r, token) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"ok":false,"error":"authorization required"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}
