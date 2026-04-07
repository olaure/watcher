package cmd

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"watcher/id"
)

// SetupCmd creates a script, role, and token in one step.
// Usage: watcher setup [--watcher <name>] <name> <script-path|remote-script-id> [args...]
func SetupCmd(database *sql.DB, scriptsDir string, args []string) {
	// Parse optional --watcher flag
	var watcherName string
	var remaining []string
	for i := 0; i < len(args); i++ {
		if (args[i] == "--watcher" || args[i] == "-watcher") && i+1 < len(args) {
			watcherName = args[i+1]
			i++
		} else {
			remaining = append(remaining, args[i])
		}
	}

	if watcherName != "" {
		setupRemote(database, watcherName, remaining)
		return
	}

	if len(remaining) < 2 {
		printSetupUsage()
		os.Exit(1)
	}

	name := remaining[0]
	scriptPath := remaining[1]
	scriptArgs := remaining[2:]

	// Resolve script path
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

	argsJSON, _ := json.Marshal(scriptArgs)

	// 1. Register script
	scriptID, _ := id.New()
	_, err = database.Exec(
		"INSERT INTO scripts (id, name, path, args) VALUES (?, ?, ?, ?)",
		scriptID, name, scriptPath, string(argsJSON),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating script: %v\n", err)
		os.Exit(1)
	}

	// 2–4. Create role + permissions + token
	tokenValue := setupRoleAndToken(database, name, scriptID)
	if tokenValue == "" {
		return
	}

	// Print summary
	fmt.Printf("Setup complete for %q\n\n", name)
	fmt.Printf("  Script:    %s\n", name)
	fmt.Printf("  Script ID: %s\n", scriptID)
	fmt.Printf("  Path:      %s\n", scriptPath)
	if len(scriptArgs) > 0 {
		fmt.Printf("  Args:      %s\n", strings.Join(scriptArgs, " "))
	}
	fmt.Printf("  Role:      %s (all permissions on this script)\n", name)
	fmt.Printf("  Token:     %s\n\n", tokenValue)
	fmt.Println("Use the token and script ID to call the API:")
	fmt.Printf("  curl -X POST -H \"Authorization: Bearer %s\" \\\n", tokenValue)
	fmt.Printf("    -d '{\"script_id\":\"%s\"}' http://localhost:8079/launch\n", scriptID)

	PushToHub(database)
}

func setupRemote(database *sql.DB, watcherName string, args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: watcher setup --watcher <watcher> <name> <remote-script-id>")
		os.Exit(1)
	}
	name := args[0]
	remoteScriptID := args[1]

	var watcherID string
	err := database.QueryRow("SELECT id FROM watchers WHERE name = ?", watcherName).Scan(&watcherID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Watcher %q not found\n", watcherName)
		os.Exit(1)
	}

	// 1. Create proxy script
	scriptID, _ := id.New()
	_, err = database.Exec(
		"INSERT INTO scripts (id, name, path, watcher_id, remote_script_id) VALUES (?, ?, '', ?, ?)",
		scriptID, name, watcherID, remoteScriptID,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating proxy script: %v\n", err)
		os.Exit(1)
	}

	// 2–4. Create role + permissions + token
	tokenValue := setupRoleAndToken(database, name, scriptID)
	if tokenValue == "" {
		return
	}

	fmt.Printf("Setup complete for %q (remote)\n\n", name)
	fmt.Printf("  Script:          %s (proxy)\n", name)
	fmt.Printf("  Script ID:       %s\n", scriptID)
	fmt.Printf("  Watcher:         %s\n", watcherName)
	fmt.Printf("  Remote Script:   %s\n", remoteScriptID)
	fmt.Printf("  Role:            %s (all permissions on this script)\n", name)
	fmt.Printf("  Token:           %s\n\n", tokenValue)
	fmt.Println("Use the token and script ID to call the API:")
	fmt.Printf("  curl -X POST -H \"Authorization: Bearer %s\" \\\n", tokenValue)
	fmt.Printf("    -d '{\"script_id\":\"%s\"}' http://localhost:8079/launch\n", scriptID)
}

// setupRoleAndToken creates a role with all permissions on the script and a token.
// Returns the token value, or empty string on failure (error is printed).
func setupRoleAndToken(database *sql.DB, name, scriptID string) string {
	roleID, _ := id.New()
	_, err := database.Exec("INSERT INTO roles (id, name) VALUES (?, ?)", roleID, name)
	if err != nil {
		database.Exec("DELETE FROM scripts WHERE id = ?", scriptID)
		fmt.Fprintf(os.Stderr, "Error creating role: %v\n", err)
		os.Exit(1)
	}

	database.Exec(
		"INSERT INTO role_permissions (role_id, script_id, action) VALUES (?, ?, '*')",
		roleID, scriptID,
	)

	tokenID, _ := id.New()
	tokenValue, _ := id.New()
	_, err = database.Exec(
		"INSERT INTO tokens (id, token, label, role_id) VALUES (?, ?, ?, ?)",
		tokenID, tokenValue, name, roleID,
	)
	if err != nil {
		database.Exec("DELETE FROM role_permissions WHERE role_id = ?", roleID)
		database.Exec("DELETE FROM roles WHERE id = ?", roleID)
		database.Exec("DELETE FROM scripts WHERE id = ?", scriptID)
		fmt.Fprintf(os.Stderr, "Error creating token: %v\n", err)
		os.Exit(1)
	}
	return tokenValue
}

func printSetupUsage() {
	fmt.Fprintln(os.Stderr, `Usage: watcher setup [--watcher <name>] <name> <script-path|remote-id> [args...]

Creates a script, a dedicated role with all permissions on it, and a token.
All three resources share the given name for easy management.

Local example:
  watcher setup deploy pull_and_build.sh /opt/myapp main build renew

Remote example:
  watcher setup --watcher production deploy abc123def456`)
}
