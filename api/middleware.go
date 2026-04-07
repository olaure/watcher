package api

import (
	"context"
	"database/sql"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type contextKey string

const tokenIDKey contextKey = "token_id"

// TokenIDFromContext returns the authenticated token ID from the request context.
func TokenIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(tokenIDKey).(string)
	return v
}

// AuthMiddleware checks the Authorization: Bearer <token> header against the DB.
func AuthMiddleware(database *sql.DB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth := r.Header.Get("Authorization")
			if auth == "" {
				denyAuth(w, r)
				return
			}

			parts := strings.SplitN(auth, " ", 2)
			if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
				denyAuth(w, r)
				return
			}
			tokenValue := parts[1]

			var tokenID string
			err := database.QueryRow(
				"SELECT id FROM tokens WHERE token = ? AND revoked = 0",
				tokenValue,
			).Scan(&tokenID)
			if err != nil {
				denyAuth(w, r)
				return
			}

			throttle.reset(clientIP(r))
			ctx := context.WithValue(r.Context(), tokenIDKey, tokenID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func denyAuth(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	delay := throttle.fail(ip)
	time.Sleep(delay)
	log.Printf("AUTH DENIED %s %s [%s] (backoff %s)", r.Method, r.URL.String(), r.RemoteAddr, delay)
	writeError(w, http.StatusUnauthorized, "unauthorized")
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// --- per-IP exponential backoff ---

const (
	backoffBase   = 1 * time.Second
	backoffMax    = 5 // 1s << 5 = 32s cap
	backoffDecay  = 15 * time.Minute
	throttleLimit = 10000 // max tracked IPs
)

var throttle = ipThrottle{attempts: make(map[string]*failInfo)}

type ipThrottle struct {
	mu       sync.Mutex
	attempts map[string]*failInfo
}

type failInfo struct {
	count    int
	lastFail time.Time
}

// fail records a failure for the given IP and returns the delay to apply.
func (t *ipThrottle) fail(ip string) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()

	info, ok := t.attempts[ip]
	if !ok || time.Since(info.lastFail) > backoffDecay {
		if !ok {
			t.evictIfFull()
		}
		info = &failInfo{}
		t.attempts[ip] = info
	}
	info.count++
	info.lastFail = time.Now()

	shift := info.count - 1
	if shift > backoffMax {
		shift = backoffMax
	}
	return backoffBase << shift
}

// reset clears the failure record for the given IP (called on successful auth).
func (t *ipThrottle) reset(ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.attempts, ip)
}

// evictIfFull removes a random entry when the map is at capacity.
// Random selection via map iteration order (Go randomizes map iteration).
// Must be called with t.mu held.
func (t *ipThrottle) evictIfFull() {
	if len(t.attempts) < throttleLimit {
		return
	}
	for k := range t.attempts {
		delete(t.attempts, k)
		return
	}
}

// HasPermission checks whether the given token has the specified action
// on the specified script, walking the role hierarchy via recursive CTE.
func HasPermission(database *sql.DB, tokenID, scriptID, action string) bool {
	var ok int
	err := database.QueryRow(`
		WITH RECURSIVE role_chain(role_id) AS (
			SELECT role_id FROM tokens WHERE id = ?
			UNION ALL
			SELECT r.parent_id
			FROM roles r
			JOIN role_chain rc ON r.id = rc.role_id
			WHERE r.parent_id IS NOT NULL
		)
		SELECT 1
		FROM role_permissions rp
		JOIN role_chain rc ON rp.role_id = rc.role_id
		WHERE (rp.script_id = ? OR rp.script_id = '*')
		  AND (rp.action = ? OR rp.action = '*')
		LIMIT 1
	`, tokenID, scriptID, action).Scan(&ok)
	return err == nil && ok == 1
}
