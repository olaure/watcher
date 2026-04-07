package api

import (
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"os"
)

type pollResponse struct {
	RunID      string `json:"run_id"`
	Status     string `json:"status"`
	Logs       string `json:"logs"`
	Finished   bool   `json:"finished"`
	ExitCode   *int   `json:"exit_code,omitempty"`
	LogsPruned bool   `json:"logs_pruned,omitempty"`
}

// PollHandler handles GET /poll?script_id=...&run_id=...
func PollHandler(database *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		scriptID := r.URL.Query().Get("script_id")
		runID := r.URL.Query().Get("run_id")

		if scriptID == "" || runID == "" {
			writeError(w, http.StatusBadRequest, "missing script_id or run_id")
			return
		}

		tokenID := TokenIDFromContext(r.Context())

		// Check permission
		if !HasPermission(database, tokenID, scriptID, "poll") {
			writeError(w, http.StatusForbidden, "insufficient permissions")
			return
		}

		// Check if this is a remote script — proxy to watcher
		var watcherID, remoteScriptID sql.NullString
		database.QueryRow(
			"SELECT watcher_id, remote_script_id FROM scripts WHERE id = ?", scriptID,
		).Scan(&watcherID, &remoteScriptID)
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
			respBody, statusCode, err := wc.Poll(remoteScriptID.String, runID)
			if err != nil {
				writeError(w, http.StatusBadGateway, fmt.Sprintf("remote watcher error: %v", err))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(statusCode)
			w.Write(respBody)
			return
		}

		// Look up run and verify it belongs to the given script
		var status, logFile string
		var exitCode sql.NullInt64
		var logsPruned int
		err := database.QueryRow(
			"SELECT status, log_file, exit_code, logs_pruned FROM runs WHERE id = ? AND script_id = ?",
			runID, scriptID,
		).Scan(&status, &logFile, &exitCode, &logsPruned)
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, "run not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "database error")
			return
		}

		finished := status != "running"

		// If logs are pruned, return metadata only
		if logsPruned == 1 {
			resp := pollResponse{
				RunID:      runID,
				Status:     status,
				Logs:       "",
				Finished:   finished,
				LogsPruned: true,
			}
			if exitCode.Valid {
				ec := int(exitCode.Int64)
				resp.ExitCode = &ec
			}
			writeJSON(w, http.StatusOK, resp)
			return
		}

		// Get or create poll cursor
		var byteOffset int64
		err = database.QueryRow(
			"SELECT byte_offset FROM poll_cursors WHERE token_id = ? AND run_id = ?",
			tokenID, runID,
		).Scan(&byteOffset)
		if err == sql.ErrNoRows {
			// Create cursor
			_, err = database.Exec(
				"INSERT INTO poll_cursors (token_id, run_id, byte_offset) VALUES (?, ?, 0)",
				tokenID, runID,
			)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "database error")
				return
			}
			byteOffset = 0
		} else if err != nil {
			writeError(w, http.StatusInternalServerError, "database error")
			return
		}

		// Read new log data from file
		var logs string
		f, err := os.Open(logFile)
		if err == nil {
			defer f.Close()
			if byteOffset > 0 {
				f.Seek(byteOffset, io.SeekStart)
			}
			data, err := io.ReadAll(f)
			if err == nil && len(data) > 0 {
				logs = string(data)
				newOffset := byteOffset + int64(len(data))
				_, _ = database.Exec(
					"UPDATE poll_cursors SET byte_offset = ? WHERE token_id = ? AND run_id = ?",
					newOffset, tokenID, runID,
				)
			}
		}

		resp := pollResponse{
			RunID:    runID,
			Status:   status,
			Logs:     logs,
			Finished: finished,
		}
		if exitCode.Valid {
			ec := int(exitCode.Int64)
			resp.ExitCode = &ec
		}
		writeJSON(w, http.StatusOK, resp)
	}
}
