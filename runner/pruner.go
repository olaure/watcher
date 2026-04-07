package runner

import (
	"database/sql"
	"fmt"
	"os"
	"time"
)

// StartPruner runs a background goroutine that periodically cleans up log files
// for finished runs where auto_cleanup is enabled.
// Metadata (runs rows) is preserved; only log files are deleted.
func StartPruner(database *sql.DB, interval time.Duration, stop <-chan struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			pruneExpired(database)
			pruneFullyConsumed(database)
		case <-stop:
			return
		}
	}
}

// pruneExpired deletes log files for runs where TTL has elapsed since completion.
func pruneExpired(database *sql.DB) {
	rows, err := database.Query(`
		SELECT r.id, r.log_file
		FROM runs r
		JOIN scripts s ON r.script_id = s.id
		WHERE r.status != 'running'
		  AND r.logs_pruned = 0
		  AND s.auto_cleanup = 1
		  AND r.finished_at IS NOT NULL
		  AND (julianday('now') - julianday(r.finished_at)) * 86400 >= s.log_ttl_sec
	`)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Pruner (expired) query error: %v\n", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var runID, logFile string
		if err := rows.Scan(&runID, &logFile); err != nil {
			fmt.Fprintf(os.Stderr, "Pruner scan error: %v\n", err)
			continue
		}
		pruneRun(database, runID, logFile)
	}
}

// pruneFullyConsumed deletes log files for finished runs where all poll cursors
// have consumed the entire log.
func pruneFullyConsumed(database *sql.DB) {
	rows, err := database.Query(`
		SELECT r.id, r.log_file
		FROM runs r
		JOIN scripts s ON r.script_id = s.id
		WHERE r.status != 'running'
		  AND r.logs_pruned = 0
		  AND s.auto_cleanup = 1
		  AND r.finished_at IS NOT NULL
	`)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Pruner (consumed) query error: %v\n", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var runID, logFile string
		if err := rows.Scan(&runID, &logFile); err != nil {
			fmt.Fprintf(os.Stderr, "Pruner scan error: %v\n", err)
			continue
		}

		// Check if there are any cursors at all
		var cursorCount int
		err := database.QueryRow(
			"SELECT COUNT(*) FROM poll_cursors WHERE run_id = ?", runID,
		).Scan(&cursorCount)
		if err != nil || cursorCount == 0 {
			continue // No pollers yet, skip
		}

		// Get file size
		info, err := os.Stat(logFile)
		if err != nil {
			continue // File missing or inaccessible
		}
		fileSize := info.Size()

		// Check if all cursors have consumed the full log
		var behind int
		err = database.QueryRow(
			"SELECT COUNT(*) FROM poll_cursors WHERE run_id = ? AND byte_offset < ?",
			runID, fileSize,
		).Scan(&behind)
		if err != nil || behind > 0 {
			continue // Some pollers haven't caught up
		}

		pruneRun(database, runID, logFile)
	}
}

// pruneRun deletes the log file and marks the run as pruned.
func pruneRun(database *sql.DB, runID, logFile string) {
	// Delete log file (ignore error if already gone)
	os.Remove(logFile)

	// Mark as pruned
	_, err := database.Exec("UPDATE runs SET logs_pruned = 1 WHERE id = ?", runID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Pruner: error marking run %s as pruned: %v\n", runID, err)
		return
	}

	// Clean up poll cursors (no longer needed)
	_, err = database.Exec("DELETE FROM poll_cursors WHERE run_id = ?", runID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Pruner: error deleting cursors for run %s: %v\n", runID, err)
	}
}
