package runner

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// ActiveRuns tracks in-flight runs for status checks.
var ActiveRuns sync.Map // map[runID]*RunState

type RunState struct {
	mu      sync.Mutex
	logFile *os.File
	Done    bool
}

// lockedWriter serializes writes to the log file from concurrent stdout/stderr goroutines.
type lockedWriter struct {
	mu *sync.Mutex
	f  *os.File
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.f.Write(p)
}

// Start launches a script in a goroutine and writes output to a log file.
// It returns immediately. The run status is updated in the DB on completion.
func Start(database *sql.DB, logsDir, runID, scriptID, scriptPath string, args []string) error {
	logPath := filepath.Join(logsDir, runID+".log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return fmt.Errorf("creating log file: %w", err)
	}

	state := &RunState{logFile: logFile}
	ActiveRuns.Store(runID, state)

	lw := &lockedWriter{mu: &state.mu, f: logFile}

	cmdArgs := append([]string{scriptPath}, args...)
	cmd := exec.Command("bash", cmdArgs...)
	cmd.Stdout = lw
	cmd.Stderr = lw

	if err := cmd.Start(); err != nil {
		lw.Write([]byte(fmt.Sprintf("Error starting script: %v\n", err)))
		finishRun(database, runID, 1, "failed")
		logFile.Close()
		state.mu.Lock()
		state.Done = true
		state.mu.Unlock()
		ActiveRuns.Delete(runID)
		return nil
	}

	// Store PID in DB for recovery after server restart.
	pid := cmd.Process.Pid
	database.Exec("UPDATE runs SET pid = ? WHERE id = ?", pid, runID)

	go func() {
		defer func() {
			logFile.Close()
			state.mu.Lock()
			state.Done = true
			state.mu.Unlock()
			ActiveRuns.Delete(runID)
		}()

		waitErr := cmd.Wait()
		exitCode := 0
		status := "success"
		if waitErr != nil {
			if exitErr, ok := waitErr.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			} else {
				exitCode = 1
			}
			status = "failed"
		}
		finishRun(database, runID, exitCode, status)
	}()

	return nil
}

// DetachActiveRuns marks all currently running runs as 'detached' in the DB.
// Called during graceful shutdown so the next server instance can pick them up.
func DetachActiveRuns(database *sql.DB) {
	result, err := database.Exec("UPDATE runs SET status = 'detached' WHERE status = 'running'")
	if err != nil {
		log.Printf("Error detaching active runs: %v", err)
		return
	}
	if n, _ := result.RowsAffected(); n > 0 {
		log.Printf("Detached %d active run(s) for recovery", n)
	}
}

// RecoverRuns picks up runs left in 'detached' (or legacy 'running') state
// from a previous server instance. For each, it monitors the PID if still
// alive, or marks as successful if the process has already finished.
func RecoverRuns(database *sql.DB, logsDir string) {
	rows, err := database.Query("SELECT id, pid FROM runs WHERE status IN ('running', 'detached')")
	if err != nil {
		log.Printf("Error querying recoverable runs: %v", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var runID string
		var pid sql.NullInt64
		if err := rows.Scan(&runID, &pid); err != nil {
			log.Printf("Error scanning run: %v", err)
			continue
		}

		if pid.Valid && pid.Int64 > 0 && processAlive(int(pid.Int64)) {
			log.Printf("Resuming monitoring of run %s (PID %d)", runID, pid.Int64)
			go monitorProcess(database, runID, int(pid.Int64))
			continue
		}

		// Process finished (or PID unknown) — the server restarted, so the
		// script that triggered the restart completed successfully.
		log.Printf("Recovered run %s: process finished, marking as success", runID)
		finishRun(database, runID, 0, "success")
	}
}

func finishRun(database *sql.DB, runID string, exitCode int, status string) {
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	_, err := database.Exec(
		"UPDATE runs SET status = ?, exit_code = ?, finished_at = ? WHERE id = ?",
		status, exitCode, now, runID,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error updating run %s: %v\n", runID, err)
	}
}

func processAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}

// monitorProcess polls a PID until it dies, then marks the run as successful.
func monitorProcess(database *sql.DB, runID string, pid int) {
	for {
		if !processAlive(pid) {
			log.Printf("Run %s: monitored process (PID %d) finished", runID, pid)
			finishRun(database, runID, 0, "success")
			return
		}
		time.Sleep(time.Second)
	}
}
