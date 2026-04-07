package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"watcher/config"
	"watcher/id"
	"watcher/runner"
)

type launchRequest struct {
	ScriptID string   `json:"script_id"`
	Args     []string `json:"args,omitempty"`
}

type launchResponse struct {
	RunID  string `json:"run_id"`
	Status string `json:"status"`
}

// LaunchHandler handles POST /launch.
func LaunchHandler(database *sql.DB, cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req launchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.ScriptID == "" {
			writeError(w, http.StatusBadRequest, "missing script_id")
			return
		}

		// Look up script
		var scriptPath, argsJSON string
		var enabled int
		var watcherID, remoteScriptID sql.NullString
		err := database.QueryRow(
			"SELECT path, args, enabled, watcher_id, remote_script_id FROM scripts WHERE id = ?", req.ScriptID,
		).Scan(&scriptPath, &argsJSON, &enabled, &watcherID, &remoteScriptID)
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, "script not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "database error")
			return
		}
		if enabled == 0 {
			writeError(w, http.StatusNotFound, "script not found or disabled")
			return
		}

		// Check permission
		tokenID := TokenIDFromContext(r.Context())
		if !HasPermission(database, tokenID, req.ScriptID, "launch") {
			writeError(w, http.StatusForbidden, "insufficient permissions")
			return
		}

		// Remote script: proxy to watcher
		if watcherID.Valid && watcherID.String != "" {
			var url, token, headers string
			err := database.QueryRow(
				"SELECT url, token, headers FROM watchers WHERE id = ?", watcherID.String,
			).Scan(&url, &token, &headers)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "watcher not found")
				return
			}
			wc := NewWatcherClient(url, token, headers)
			respBody, statusCode, err := wc.Launch(remoteScriptID.String, req.Args)
			if err != nil {
				writeError(w, http.StatusBadGateway, fmt.Sprintf("remote watcher error: %v", err))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(statusCode)
			w.Write(respBody)
			return
		}

		// Verify script file still exists
		if _, err := os.Stat(scriptPath); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("script file not accessible: %v", err))
			return
		}

		// Generate run ID
		runID, err := id.New()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to generate run ID")
			return
		}

		// Prepend registered args before any API-provided args
		var registeredArgs []string
		if argsJSON != "" && argsJSON != "[]" {
			if err := json.Unmarshal([]byte(argsJSON), &registeredArgs); err != nil {
				writeError(w, http.StatusInternalServerError, "invalid script args configuration")
				return
			}
		}
		finalArgs := append(registeredArgs, req.Args...)

		logFile := filepath.Join(cfg.LogsDir, runID+".log")

		// Insert run record
		_, err = database.Exec(
			"INSERT INTO runs (id, script_id, log_file) VALUES (?, ?, ?)",
			runID, req.ScriptID, logFile,
		)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create run")
			return
		}

		// Start script execution
		if err := runner.Start(database, cfg.LogsDir, runID, req.ScriptID, scriptPath, finalArgs); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to start script: %v", err))
			return
		}

		writeJSON(w, http.StatusOK, launchResponse{
			RunID:  runID,
			Status: "running",
		})
	}
}
