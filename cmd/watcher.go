package cmd

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"watcher/api"
	"watcher/id"
)

func WatcherCmd(database *sql.DB, args []string) {
	if len(args) == 0 {
		printWatcherUsage()
		os.Exit(1)
	}

	switch args[0] {
	case "add":
		watcherAdd(database, args[1:])
	case "list":
		watcherList(database)
	case "remove":
		watcherRemove(database, args[1:])
	case "rename":
		watcherRename(database, args[1:])
	case "set-url":
		watcherSetURL(database, args[1:])
	case "set-token":
		watcherSetToken(database, args[1:])
	case "test":
		watcherTest(database, args[1:])
	case "link":
		watcherLink(database, args[1:])
	default:
		printWatcherUsage()
		os.Exit(1)
	}
}

func watcherAdd(database *sql.DB, args []string) {
	if len(args) < 3 {
		fmt.Fprintln(os.Stderr, "Usage: watcher watcher add <name> <url> <token> [--header Key:Value]...")
		os.Exit(1)
	}
	name := args[0]
	url := args[1]
	token := args[2]

	// Parse optional --header flags
	headers := make(map[string]string)
	for i := 3; i < len(args); i++ {
		if args[i] == "--header" && i+1 < len(args) {
			i++
			parts := strings.SplitN(args[i], ":", 2)
			if len(parts) != 2 {
				fmt.Fprintf(os.Stderr, "Invalid header format %q (expected Key:Value)\n", args[i])
				os.Exit(1)
			}
			headers[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}

	headersJSON, _ := json.Marshal(headers)

	watcherID, err := id.New()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error generating ID: %v\n", err)
		os.Exit(1)
	}

	_, err = database.Exec(
		"INSERT INTO watchers (id, name, url, token, headers) VALUES (?, ?, ?, ?, ?)",
		watcherID, name, url, token, string(headersJSON),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error adding watcher: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Added watcher %q (%s)\n", name, url)
}

func watcherList(database *sql.DB) {
	rows, err := database.Query("SELECT id, name, url, headers, created_at FROM watchers ORDER BY name")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing watchers: %v\n", err)
		os.Exit(1)
	}
	defer rows.Close()

	fmt.Printf("%-24s %-16s %-40s %-20s %s\n", "ID", "NAME", "URL", "HEADERS", "CREATED")
	for rows.Next() {
		var wid, name, url, headersJSON, createdAt string
		if err := rows.Scan(&wid, &name, &url, &headersJSON, &createdAt); err != nil {
			fmt.Fprintf(os.Stderr, "Error reading row: %v\n", err)
			os.Exit(1)
		}
		headerCount := 0
		var h map[string]string
		if json.Unmarshal([]byte(headersJSON), &h) == nil {
			headerCount = len(h)
		}
		headersDisplay := "-"
		if headerCount > 0 {
			headersDisplay = fmt.Sprintf("%d header(s)", headerCount)
		}
		fmt.Printf("%-24s %-16s %-40s %-20s %s\n", wid, name, url, headersDisplay, createdAt)
	}
}

func watcherRemove(database *sql.DB, args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: watcher watcher remove <name>")
		os.Exit(1)
	}
	name := args[0]

	var watcherID string
	err := database.QueryRow("SELECT id FROM watchers WHERE name = ?", name).Scan(&watcherID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Watcher %q not found\n", name)
		os.Exit(1)
	}

	var scriptCount int
	database.QueryRow("SELECT COUNT(*) FROM scripts WHERE watcher_id = ?", watcherID).Scan(&scriptCount)
	if scriptCount > 0 {
		fmt.Fprintf(os.Stderr, "Cannot remove watcher %q: %d script(s) still linked\n", name, scriptCount)
		os.Exit(1)
	}

	_, err = database.Exec("DELETE FROM watchers WHERE id = ?", watcherID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error removing watcher: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Removed watcher %q\n", name)
}

func watcherRename(database *sql.DB, args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: watcher watcher rename <old-name> <new-name>")
		os.Exit(1)
	}
	oldName := args[0]
	newName := args[1]

	res, err := database.Exec("UPDATE watchers SET name = ? WHERE name = ?", newName, oldName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error renaming watcher: %v\n", err)
		os.Exit(1)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		fmt.Fprintf(os.Stderr, "Watcher %q not found\n", oldName)
		os.Exit(1)
	}
	fmt.Printf("Renamed watcher %q to %q\n", oldName, newName)
}

func watcherSetURL(database *sql.DB, args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: watcher watcher set-url <name> <url>")
		os.Exit(1)
	}
	name := args[0]
	url := args[1]

	res, err := database.Exec("UPDATE watchers SET url = ? WHERE name = ?", url, name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error updating URL: %v\n", err)
		os.Exit(1)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		fmt.Fprintf(os.Stderr, "Watcher %q not found\n", name)
		os.Exit(1)
	}
	fmt.Printf("Watcher %q URL set to %s\n", name, url)
}

func watcherSetToken(database *sql.DB, args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: watcher watcher set-token <name> <token>")
		os.Exit(1)
	}
	name := args[0]
	token := args[1]

	res, err := database.Exec("UPDATE watchers SET token = ? WHERE name = ?", token, name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error updating token: %v\n", err)
		os.Exit(1)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		fmt.Fprintf(os.Stderr, "Watcher %q not found\n", name)
		os.Exit(1)
	}
	fmt.Printf("Watcher %q token updated\n", name)
}

func watcherTest(database *sql.DB, args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: watcher watcher test <name>")
		os.Exit(1)
	}
	name := args[0]

	var url, token, headers string
	err := database.QueryRow(
		"SELECT url, token, headers FROM watchers WHERE name = ?", name,
	).Scan(&url, &token, &headers)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Watcher %q not found\n", name)
		os.Exit(1)
	}

	wc := api.NewWatcherClient(url, token, headers)
	if err := wc.Health(); err != nil {
		fmt.Fprintf(os.Stderr, "Health check failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Watcher %q is healthy (%s)\n", name, url)
}

func watcherLink(database *sql.DB, args []string) {
	if len(args) < 3 {
		fmt.Fprintln(os.Stderr, `Usage: watcher watcher link <local-name> <watcher> <remote-script-id>

Creates a proxy script linked to a remote watcher's script.

Example:
  watcher watcher link deploy production abc123def456`)
		os.Exit(1)
	}
	localName := args[0]
	watcherName := args[1]
	remoteScriptID := args[2]

	var watcherID string
	err := database.QueryRow("SELECT id FROM watchers WHERE name = ?", watcherName).Scan(&watcherID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Watcher %q not found\n", watcherName)
		os.Exit(1)
	}

	scriptID, err := id.New()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error generating ID: %v\n", err)
		os.Exit(1)
	}

	_, err = database.Exec(
		"INSERT INTO scripts (id, name, path, watcher_id, remote_script_id) VALUES (?, ?, '', ?, ?)",
		scriptID, localName, watcherID, remoteScriptID,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating proxy script: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Linked script %q to watcher %q (remote script: %s)\n", localName, watcherName, remoteScriptID)
	fmt.Printf("  Script ID: %s\n", scriptID)
}

func printWatcherUsage() {
	fmt.Fprintln(os.Stderr, `Usage: watcher watcher <command>

Commands:
  add <name> <url> <token> [--header K:V]...  Register a remote watcher
  list                                          List all watchers
  remove <name>                                 Remove a watcher
  rename <old-name> <new-name>                  Rename a watcher
  set-url <name> <url>                          Update watcher URL
  set-token <name> <token>                      Update watcher token
  test <name>                                   Test connectivity
  link <local-name> <watcher> <remote-id>       Create a proxy script`)
}
