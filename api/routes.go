package api

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"watcher/config"
)

// NewMux creates the HTTP handler with auth middleware and all routes.
func NewMux(database *sql.DB, cfg config.Config) http.Handler {
	authedMux := http.NewServeMux()
	authedMux.HandleFunc("POST /launch", LaunchHandler(database, cfg))
	authedMux.HandleFunc("GET /poll", PollHandler(database))
	authedMux.HandleFunc("POST /federation/sync", FederationSyncHandler(database))

	if cfg.EnableAPI {
		authedMux.HandleFunc("GET /api/overview", OverviewHandler(database))
		authedMux.HandleFunc("GET /api/scripts", ScriptsHandler(database))
		authedMux.HandleFunc("GET /api/runs", RunsHandler(database))
		authedMux.HandleFunc("GET /api/watchers", WatchersHandler(database))
		authedMux.HandleFunc("GET /api/watchers/{name}/scripts", WatcherRemoteScriptsHandler(database))
		authedMux.HandleFunc("GET /api/watchers/{name}/runs", WatcherRemoteRunsHandler(database))
	}

	// Top-level mux: health is public, everything else requires auth.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.Handle("/", AuthMiddleware(database)(authedMux))

	return LoggingMiddleware(mux)
}

// responseRecorder captures the status code written by downstream handlers.
type responseRecorder struct {
	http.ResponseWriter
	status int
}

func (rr *responseRecorder) WriteHeader(code int) {
	rr.status = code
	rr.ResponseWriter.WriteHeader(code)
}

// LoggingMiddleware logs method, path, status, and duration for every request.
func LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rr := &responseRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rr, r)
		log.Printf("%s %s %d %s [%s]", r.Method, r.URL.String(), rr.status, time.Since(start).Round(time.Millisecond), r.RemoteAddr)
	})
}

// writeJSON writes a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// writeError writes a JSON error response.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
