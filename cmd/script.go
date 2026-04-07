package cmd

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"watcher/id"
)

func ScriptCmd(database *sql.DB, scriptsDir string, args []string) {
	if len(args) == 0 {
		printScriptUsage()
		os.Exit(1)
	}

	// Commands that mutate scripts should trigger a federation push.
	pushAfter := false

	switch args[0] {
	case "register":
		scriptRegister(database, scriptsDir, args[1:])
		pushAfter = true
	case "list":
		scriptList(database)
	case "enable":
		scriptSetEnabled(database, args[1:], true)
		pushAfter = true
	case "disable":
		scriptSetEnabled(database, args[1:], false)
		pushAfter = true
	case "set-ttl":
		scriptSetTTL(database, args[1:])
	case "set-cleanup":
		scriptSetCleanup(database, args[1:])
	case "rename":
		scriptRename(database, args[1:])
		pushAfter = true
	case "set-path":
		scriptSetPath(database, scriptsDir, args[1:])
	case "set-args":
		scriptSetArgs(database, args[1:])
	case "clear-args":
		scriptClearArgs(database, args[1:])
	default:
		printScriptUsage()
		os.Exit(1)
	}

	if pushAfter {
		PushToHub(database)
	}
}

func scriptRegister(database *sql.DB, scriptsDir string, args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: watcher script register <name> <path> [args...]")
		os.Exit(1)
	}
	name := args[0]
	scriptPath := args[1]
	scriptArgs := args[2:] // trailing args stored with the script

	// Resolve path: if not absolute, resolve against scripts_dir
	if !filepath.IsAbs(scriptPath) {
		scriptPath = filepath.Join(scriptsDir, scriptPath)
	}
	scriptPath, err := filepath.Abs(scriptPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving path: %v\n", err)
		os.Exit(1)
	}

	// Validate file exists
	info, err := os.Stat(scriptPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot access %s: %v\n", scriptPath, err)
		os.Exit(1)
	}
	if info.IsDir() {
		fmt.Fprintf(os.Stderr, "Error: %s is a directory\n", scriptPath)
		os.Exit(1)
	}

	argsJSON, err := json.Marshal(scriptArgs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error encoding args: %v\n", err)
		os.Exit(1)
	}

	scriptID, err := id.New()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error generating ID: %v\n", err)
		os.Exit(1)
	}

	_, err = database.Exec(
		"INSERT INTO scripts (id, name, path, args) VALUES (?, ?, ?, ?)",
		scriptID, name, scriptPath, string(argsJSON),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error registering script: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Registered script %q (id: %s)\n", name, scriptID)
	fmt.Printf("  path: %s\n", scriptPath)
	if len(scriptArgs) > 0 {
		fmt.Printf("  args: %s\n", strings.Join(scriptArgs, " "))
	}
}

func scriptList(database *sql.DB) {
	rows, err := database.Query("SELECT id, name, path, args, enabled, auto_cleanup, log_ttl_sec, created_at FROM scripts ORDER BY created_at")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing scripts: %v\n", err)
		os.Exit(1)
	}
	defer rows.Close()

	fmt.Printf("%-24s %-16s %-40s %-20s %-8s %-10s %-10s %s\n", "ID", "NAME", "PATH", "ARGS", "ENABLED", "CLEANUP", "TTL", "CREATED")
	for rows.Next() {
		var sid, name, path, argsJSON, createdAt string
		var enabled, autoCleanup, logTTL int
		if err := rows.Scan(&sid, &name, &path, &argsJSON, &enabled, &autoCleanup, &logTTL, &createdAt); err != nil {
			fmt.Fprintf(os.Stderr, "Error reading row: %v\n", err)
			os.Exit(1)
		}
		enabledStr := "yes"
		if enabled == 0 {
			enabledStr = "no"
		}
		cleanupStr := "off"
		if autoCleanup == 1 {
			cleanupStr = "on"
		}
		ttlStr := (time.Duration(logTTL) * time.Second).String()
		argsDisplay := formatArgs(argsJSON)
		fmt.Printf("%-24s %-16s %-40s %-20s %-8s %-10s %-10s %s\n", sid, name, path, argsDisplay, enabledStr, cleanupStr, ttlStr, createdAt)
	}
}

func formatArgs(argsJSON string) string {
	var args []string
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil || len(args) == 0 {
		return "-"
	}
	return strings.Join(args, " ")
}

func scriptSetEnabled(database *sql.DB, args []string, enabled bool) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: watcher script enable|disable <name>")
		os.Exit(1)
	}
	name := args[0]
	val := 0
	if enabled {
		val = 1
	}

	res, err := database.Exec("UPDATE scripts SET enabled = ? WHERE name = ?", val, name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		fmt.Fprintf(os.Stderr, "Script %q not found\n", name)
		os.Exit(1)
	}
	action := "disabled"
	if enabled {
		action = "enabled"
	}
	fmt.Printf("Script %q %s\n", name, action)
}

func scriptSetTTL(database *sql.DB, args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: watcher script set-ttl <name> <duration>")
		fmt.Fprintln(os.Stderr, "  Example: watcher script set-ttl deploy 1h")
		os.Exit(1)
	}
	name := args[0]
	durationStr := args[1]

	d, err := time.ParseDuration(durationStr)
	if err != nil {
		// Try parsing as plain seconds
		sec, err2 := strconv.Atoi(durationStr)
		if err2 != nil {
			fmt.Fprintf(os.Stderr, "Error: invalid duration %q: %v\n", durationStr, err)
			os.Exit(1)
		}
		d = time.Duration(sec) * time.Second
	}

	res, err := database.Exec("UPDATE scripts SET log_ttl_sec = ? WHERE name = ?", int(d.Seconds()), name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		fmt.Fprintf(os.Stderr, "Script %q not found\n", name)
		os.Exit(1)
	}
	fmt.Printf("Script %q log TTL set to %s\n", name, d)
}

func scriptSetCleanup(database *sql.DB, args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: watcher script set-cleanup <name> on|off")
		os.Exit(1)
	}
	name := args[0]
	toggle := args[1]

	var val int
	switch toggle {
	case "on":
		val = 1
	case "off":
		val = 0
	default:
		fmt.Fprintln(os.Stderr, "Error: expected 'on' or 'off'")
		os.Exit(1)
	}

	res, err := database.Exec("UPDATE scripts SET auto_cleanup = ? WHERE name = ?", val, name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		fmt.Fprintf(os.Stderr, "Script %q not found\n", name)
		os.Exit(1)
	}
	fmt.Printf("Script %q auto_cleanup set to %s\n", name, toggle)
}

func scriptRename(database *sql.DB, args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: watcher script rename <old-name> <new-name>")
		os.Exit(1)
	}
	oldName := args[0]
	newName := args[1]

	res, err := database.Exec("UPDATE scripts SET name = ? WHERE name = ?", newName, oldName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error renaming script: %v\n", err)
		os.Exit(1)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		fmt.Fprintf(os.Stderr, "Script %q not found\n", oldName)
		os.Exit(1)
	}
	fmt.Printf("Renamed script %q to %q\n", oldName, newName)
}

func scriptSetPath(database *sql.DB, scriptsDir string, args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: watcher script set-path <name> <path>")
		os.Exit(1)
	}
	name := args[0]
	scriptPath := args[1]

	if !filepath.IsAbs(scriptPath) {
		scriptPath = filepath.Join(scriptsDir, scriptPath)
	}
	scriptPath, err := filepath.Abs(scriptPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving path: %v\n", err)
		os.Exit(1)
	}

	info, err := os.Stat(scriptPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot access %s: %v\n", scriptPath, err)
		os.Exit(1)
	}
	if info.IsDir() {
		fmt.Fprintf(os.Stderr, "Error: %s is a directory\n", scriptPath)
		os.Exit(1)
	}

	res, err := database.Exec("UPDATE scripts SET path = ? WHERE name = ?", scriptPath, name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error updating path: %v\n", err)
		os.Exit(1)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		fmt.Fprintf(os.Stderr, "Script %q not found\n", name)
		os.Exit(1)
	}
	fmt.Printf("Script %q path set to %s\n", name, scriptPath)
}

func scriptSetArgs(database *sql.DB, args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: watcher script set-args <name> <args...>")
		os.Exit(1)
	}
	name := args[0]
	scriptArgs := args[1:]

	argsJSON, err := json.Marshal(scriptArgs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error encoding args: %v\n", err)
		os.Exit(1)
	}

	res, err := database.Exec("UPDATE scripts SET args = ? WHERE name = ?", string(argsJSON), name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error updating args: %v\n", err)
		os.Exit(1)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		fmt.Fprintf(os.Stderr, "Script %q not found\n", name)
		os.Exit(1)
	}
	fmt.Printf("Script %q args set to: %s\n", name, strings.Join(scriptArgs, " "))
}

func scriptClearArgs(database *sql.DB, args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: watcher script clear-args <name>")
		os.Exit(1)
	}
	name := args[0]

	res, err := database.Exec("UPDATE scripts SET args = '[]' WHERE name = ?", name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error clearing args: %v\n", err)
		os.Exit(1)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		fmt.Fprintf(os.Stderr, "Script %q not found\n", name)
		os.Exit(1)
	}
	fmt.Printf("Script %q args cleared\n", name)
}

func printScriptUsage() {
	fmt.Fprintln(os.Stderr, `Usage: watcher script <command>

Commands:
  register <name> <path> [args...]  Register a script with optional default args
  list                              List all scripts
  enable <name>                     Enable a script
  disable <name>                    Disable a script
  set-args <name> <args...>         Set default arguments
  clear-args <name>                 Clear default arguments
  set-ttl <name> <duration>         Set log TTL (e.g. 1h, 30m, 3600)
  set-cleanup <name> on|off         Toggle auto-cleanup of log files
  rename <old-name> <new-name>      Rename a script
  set-path <name> <path>            Update script path

Registered args are prepended to any args provided via the API at launch time.`)
}
