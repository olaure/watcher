package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"watcher/id"
)

type syncRequest struct {
	URL     string       `json:"url"`
	Token   string       `json:"token"`
	Headers map[string]string `json:"headers"`
	Scripts []syncScript `json:"scripts"`
}

type syncScript struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

type syncResponse struct {
	Watcher   string `json:"watcher"`
	Created   int    `json:"created"`
	Updated   int    `json:"updated"`
	Disabled  int    `json:"disabled"`
	Unchanged int    `json:"unchanged"`
}

// FederationSyncHandler handles POST /federation/sync.
// A remote watcher pushes its script state to the hub.
// Auth is identity-based: the authenticated token's ID is used to look up the watcher via watchers.token_id.
func FederationSyncHandler(database *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tokenID := TokenIDFromContext(r.Context())

		// Look up watcher by token_id
		var watcherID, watcherName string
		err := database.QueryRow(
			"SELECT id, name FROM watchers WHERE token_id = ?", tokenID,
		).Scan(&watcherID, &watcherName)
		if err != nil {
			writeError(w, http.StatusForbidden, "token is not associated with a watcher")
			return
		}

		var req syncRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}

		// Update watcher connection info (hub uses this to proxy launch/poll)
		headersJSON, _ := json.Marshal(req.Headers)
		if req.URL != "" {
			database.Exec(
				"UPDATE watchers SET url = ?, token = ?, headers = ? WHERE id = ?",
				req.URL, req.Token, string(headersJSON), watcherID,
			)
		}

		// Reconcile scripts
		resp := reconcileScripts(database, watcherID, watcherName, req.Scripts)
		log.Printf("federation sync from %q: created=%d updated=%d disabled=%d unchanged=%d",
			watcherName, resp.Created, resp.Updated, resp.Disabled, resp.Unchanged)
		writeJSON(w, http.StatusOK, resp)
	}
}

// reconcileScripts creates, updates, or disables proxy scripts on the hub
// to match the remote watcher's current script state.
func reconcileScripts(database *sql.DB, watcherID, watcherName string, remoteScripts []syncScript) syncResponse {
	resp := syncResponse{Watcher: watcherName}

	// Index existing proxy scripts for this watcher by remote_script_id
	existing := make(map[string]struct {
		id      string
		name    string
		enabled int
	})
	rows, err := database.Query(
		"SELECT id, name, remote_script_id, enabled FROM scripts WHERE watcher_id = ?",
		watcherID,
	)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var sid, sname, remoteID string
			var enabled int
			rows.Scan(&sid, &sname, &remoteID, &enabled)
			existing[remoteID] = struct {
				id      string
				name    string
				enabled int
			}{sid, sname, enabled}
		}
	}

	// Track which remote script IDs are still present
	seen := make(map[string]bool)

	for _, rs := range remoteScripts {
		seen[rs.ID] = true
		proxyName := fmt.Sprintf("%s/%s", watcherName, rs.Name)
		enabledVal := 0
		if rs.Enabled {
			enabledVal = 1
		}

		if ex, ok := existing[rs.ID]; ok {
			// Existing proxy — check if update needed
			if ex.name != proxyName || ex.enabled != enabledVal {
				database.Exec(
					"UPDATE scripts SET name = ?, enabled = ? WHERE id = ?",
					proxyName, enabledVal, ex.id,
				)
				resp.Updated++
			} else {
				resp.Unchanged++
			}
		} else {
			// New proxy script
			scriptID, _ := id.New()
			database.Exec(
				"INSERT INTO scripts (id, name, path, watcher_id, remote_script_id, enabled) VALUES (?, ?, '', ?, ?, ?)",
				scriptID, proxyName, watcherID, rs.ID, enabledVal,
			)
			resp.Created++
		}
	}

	// Disable proxies for scripts that disappeared from remote
	for remoteID, ex := range existing {
		if !seen[remoteID] && ex.enabled == 1 {
			database.Exec("UPDATE scripts SET enabled = 0 WHERE id = ?", ex.id)
			resp.Disabled++
		}
	}

	return resp
}
