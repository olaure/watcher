package api

import (
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// requireAdmin checks that the requesting token has wildcard permissions (* on *).
// Returns true if authorized, false if it wrote a 403 response.
func requireAdmin(database *sql.DB, w http.ResponseWriter, r *http.Request) bool {
	tokenID := TokenIDFromContext(r.Context())
	if !HasPermission(database, tokenID, "*", "*") {
		writeError(w, http.StatusForbidden, "admin access required")
		return false
	}
	return true
}

// OverviewHandler handles GET /api/overview — dashboard summary.
func OverviewHandler(database *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(database, w, r) {
			return
		}

		var scriptsTotal, scriptsEnabled, scriptsRemote int
		database.QueryRow("SELECT COUNT(*) FROM scripts").Scan(&scriptsTotal)
		database.QueryRow("SELECT COUNT(*) FROM scripts WHERE enabled = 1").Scan(&scriptsEnabled)
		database.QueryRow("SELECT COUNT(*) FROM scripts WHERE watcher_id IS NOT NULL").Scan(&scriptsRemote)

		var tokensTotal, tokensRevoked int
		database.QueryRow("SELECT COUNT(*) FROM tokens").Scan(&tokensTotal)
		database.QueryRow("SELECT COUNT(*) FROM tokens WHERE revoked = 1").Scan(&tokensRevoked)

		var runsActive, recentFailures int
		database.QueryRow("SELECT COUNT(*) FROM runs WHERE status = 'running'").Scan(&runsActive)
		database.QueryRow(
			"SELECT COUNT(*) FROM runs WHERE status = 'failed' AND finished_at > datetime('now', '-1 day')",
		).Scan(&recentFailures)

		// Watcher health checks in parallel
		type watcherRow struct {
			id, url, token, headers string
		}
		rows, err := database.Query("SELECT id, url, token, headers FROM watchers")
		var watchers []watcherRow
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var wr watcherRow
				if rows.Scan(&wr.id, &wr.url, &wr.token, &wr.headers) == nil {
					watchers = append(watchers, wr)
				}
			}
		}

		var healthy, unhealthy int
		if len(watchers) > 0 {
			var wg sync.WaitGroup
			var mu sync.Mutex
			for _, wr := range watchers {
				wg.Add(1)
				go func(wr watcherRow) {
					defer wg.Done()
					wc := NewWatcherClient(wr.url, wr.token, wr.headers)
					wc.Client.Timeout = 5 * time.Second
					mu.Lock()
					defer mu.Unlock()
					if wc.Health() == nil {
						healthy++
					} else {
						unhealthy++
					}
				}(wr)
			}
			wg.Wait()
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"scripts":  map[string]int{"total": scriptsTotal, "enabled": scriptsEnabled, "remote": scriptsRemote},
			"watchers": map[string]int{"total": len(watchers), "healthy": healthy, "unhealthy": unhealthy},
			"runs":     map[string]int{"active": runsActive, "recent_failures": recentFailures},
			"tokens":   map[string]int{"total": tokensTotal, "revoked": tokensRevoked},
		})
	}
}

// ScriptsHandler handles GET /api/scripts — all scripts with last run info.
func ScriptsHandler(database *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(database, w, r) {
			return
		}

		rows, err := database.Query(`
			SELECT s.id, s.name, s.enabled, s.path, s.args, s.watcher_id,
			       w.name, w.url,
			       lr.id, lr.status, lr.exit_code, lr.started_at, lr.finished_at
			FROM scripts s
			LEFT JOIN watchers w ON s.watcher_id = w.id
			LEFT JOIN (
				SELECT r1.* FROM runs r1
				INNER JOIN (
					SELECT script_id, MAX(started_at) AS max_start
					FROM runs GROUP BY script_id
				) r2 ON r1.script_id = r2.script_id AND r1.started_at = r2.max_start
			) lr ON lr.script_id = s.id
			ORDER BY s.name
		`)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "database error")
			return
		}
		defer rows.Close()

		var scripts []map[string]any
		for rows.Next() {
			var (
				id, name, path, argsJSON                        string
				enabled                                         int
				watcherID, watcherName, watcherURL               sql.NullString
				runID, runStatus, runStarted, runFinished        sql.NullString
				runExitCode                                      sql.NullInt64
			)
			if err := rows.Scan(&id, &name, &enabled, &path, &argsJSON,
				&watcherID, &watcherName, &watcherURL,
				&runID, &runStatus, &runExitCode, &runStarted, &runFinished,
			); err != nil {
				continue
			}

			scriptType := "local"
			if watcherID.Valid {
				scriptType = "remote"
			}

			entry := map[string]any{
				"id":      id,
				"name":    name,
				"enabled": enabled == 1,
				"type":    scriptType,
				"path":    path,
				"args":    argsJSON,
			}

			if watcherID.Valid {
				entry["watcher"] = map[string]string{
					"name": watcherName.String,
					"url":  watcherURL.String,
				}
			} else {
				entry["watcher"] = nil
			}

			if runID.Valid {
				lastRun := map[string]any{
					"id":          runID.String,
					"status":      runStatus.String,
					"started_at":  runStarted.String,
					"finished_at": runFinished.String,
				}
				if runExitCode.Valid {
					lastRun["exit_code"] = runExitCode.Int64
				}
				entry["last_run"] = lastRun
			} else {
				entry["last_run"] = nil
			}

			scripts = append(scripts, entry)
		}

		if scripts == nil {
			scripts = []map[string]any{}
		}
		writeJSON(w, http.StatusOK, scripts)
	}
}

// RunsHandler handles GET /api/runs?limit=50 — recent runs.
func RunsHandler(database *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(database, w, r) {
			return
		}

		limit := 50
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		if limit > 200 {
			limit = 200
		}

		rows, err := database.Query(`
			SELECT r.id, r.script_id, s.name, r.status, r.exit_code, r.started_at, r.finished_at
			FROM runs r
			JOIN scripts s ON r.script_id = s.id
			ORDER BY r.started_at DESC
			LIMIT ?
		`, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "database error")
			return
		}
		defer rows.Close()

		var runs []map[string]any
		for rows.Next() {
			var (
				id, scriptID, scriptName, status string
				exitCode                         sql.NullInt64
				startedAt                        string
				finishedAt                       sql.NullString
			)
			if err := rows.Scan(&id, &scriptID, &scriptName, &status, &exitCode, &startedAt, &finishedAt); err != nil {
				continue
			}

			entry := map[string]any{
				"id":          id,
				"script_id":   scriptID,
				"script_name": scriptName,
				"status":      status,
				"started_at":  startedAt,
				"finished_at": finishedAt.String,
			}
			if exitCode.Valid {
				entry["exit_code"] = exitCode.Int64
			}

			runs = append(runs, entry)
		}

		if runs == nil {
			runs = []map[string]any{}
		}
		writeJSON(w, http.StatusOK, runs)
	}
}

// watcherClient loads a watcher by name from the DB and returns a WatcherClient.
// Writes an error response and returns nil if the watcher is not found.
func watcherClient(database *sql.DB, w http.ResponseWriter, name string) *WatcherClient {
	var url, token, headers string
	err := database.QueryRow(
		"SELECT url, token, headers FROM watchers WHERE name = ?", name,
	).Scan(&url, &token, &headers)
	if err != nil {
		writeError(w, http.StatusNotFound, "watcher not found")
		return nil
	}
	return NewWatcherClient(url, token, headers)
}

// WatcherRemoteScriptsHandler handles GET /api/watchers/{name}/scripts.
// Proxies to the remote watcher's GET /api/scripts.
func WatcherRemoteScriptsHandler(database *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(database, w, r) {
			return
		}
		name := r.PathValue("name")
		wc := watcherClient(database, w, name)
		if wc == nil {
			return
		}
		body, status, err := wc.FetchAPI("/api/scripts")
		if err != nil {
			writeError(w, http.StatusBadGateway, fmt.Sprintf("remote watcher error: %v", err))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write(body)
	}
}

// WatcherRemoteRunsHandler handles GET /api/watchers/{name}/runs.
// Proxies to the remote watcher's GET /api/runs, forwarding the limit param.
func WatcherRemoteRunsHandler(database *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(database, w, r) {
			return
		}
		name := r.PathValue("name")
		wc := watcherClient(database, w, name)
		if wc == nil {
			return
		}
		path := "/api/runs"
		if limit := r.URL.Query().Get("limit"); limit != "" {
			path += "?limit=" + limit
		}
		body, status, err := wc.FetchAPI(path)
		if err != nil {
			writeError(w, http.StatusBadGateway, fmt.Sprintf("remote watcher error: %v", err))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write(body)
	}
}

// WatchersHandler handles GET /api/watchers — watchers with live health.
func WatchersHandler(database *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(database, w, r) {
			return
		}

		rows, err := database.Query(`
			SELECT w.id, w.name, w.url, w.token, w.headers, w.created_at,
			       COUNT(s.id) AS script_count
			FROM watchers w
			LEFT JOIN scripts s ON s.watcher_id = w.id
			GROUP BY w.id
			ORDER BY w.name
		`)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "database error")
			return
		}
		defer rows.Close()

		type watcherEntry struct {
			result map[string]any
			url    string
			token  string
			headers string
			index  int
		}

		var entries []watcherEntry
		for rows.Next() {
			var (
				id, name, url, token, headers, createdAt string
				scriptCount                               int
			)
			if err := rows.Scan(&id, &name, &url, &token, &headers, &createdAt, &scriptCount); err != nil {
				continue
			}
			entries = append(entries, watcherEntry{
				result: map[string]any{
					"id":         id,
					"name":       name,
					"url":        url,
					"healthy":    false,
					"scripts":    scriptCount,
					"created_at": createdAt,
				},
				url:     url,
				token:   token,
				headers: headers,
				index:   len(entries),
			})
		}

		// Parallel health checks
		var wg sync.WaitGroup
		for i := range entries {
			wg.Add(1)
			go func(e *watcherEntry) {
				defer wg.Done()
				wc := NewWatcherClient(e.url, e.token, e.headers)
				wc.Client.Timeout = 5 * time.Second
				e.result["healthy"] = wc.Health() == nil
			}(&entries[i])
		}
		wg.Wait()

		watchers := make([]map[string]any, len(entries))
		for i, e := range entries {
			watchers[i] = e.result
		}
		writeJSON(w, http.StatusOK, watchers)
	}
}
