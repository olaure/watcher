package cmd

import (
	"database/sql"
	"flag"
	"fmt"
	"os"

	"watcher/id"
)

func TokenCmd(database *sql.DB, args []string) {
	if len(args) == 0 {
		printTokenUsage()
		os.Exit(1)
	}

	switch args[0] {
	case "generate":
		tokenGenerate(database, args[1:])
	case "list":
		tokenList(database)
	case "revoke":
		tokenRevoke(database, args[1:])
	case "rename":
		tokenRename(database, args[1:])
	case "set-role":
		tokenSetRole(database, args[1:])
	default:
		printTokenUsage()
		os.Exit(1)
	}
}

// resolveToken looks up a token by name (label column), returning the raw token value.
func resolveToken(database *sql.DB, name string) string {
	var tokenValue string
	err := database.QueryRow("SELECT token FROM tokens WHERE label = ?", name).Scan(&tokenValue)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Token %q not found\n", name)
		os.Exit(1)
	}
	return tokenValue
}

func tokenGenerate(database *sql.DB, args []string) {
	fs := flag.NewFlagSet("token generate", flag.ExitOnError)
	name := fs.String("name", "", "Unique name for the token (required)")
	roleName := fs.String("role", "", "Role to assign (required)")
	fs.Parse(args)

	if *name == "" || *roleName == "" {
		fmt.Fprintln(os.Stderr, "Error: --name and --role are required")
		fmt.Fprintln(os.Stderr, "Usage: watcher token generate --name <name> --role <role>")
		os.Exit(1)
	}

	var roleID string
	err := database.QueryRow("SELECT id FROM roles WHERE name = ?", *roleName).Scan(&roleID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Role %q not found\n", *roleName)
		os.Exit(1)
	}

	tokenID, err := id.New()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error generating ID: %v\n", err)
		os.Exit(1)
	}

	tokenValue, err := id.New()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error generating token: %v\n", err)
		os.Exit(1)
	}

	// Check name uniqueness with a clear error before INSERT.
	var exists int
	database.QueryRow("SELECT 1 FROM tokens WHERE label = ?", *name).Scan(&exists)
	if exists == 1 {
		fmt.Fprintf(os.Stderr, "Error: token name %q already exists\n", *name)
		os.Exit(1)
	}

	_, err = database.Exec(
		"INSERT INTO tokens (id, token, label, role_id) VALUES (?, ?, ?, ?)",
		tokenID, tokenValue, *name, roleID,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error saving token: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(tokenValue)
}

func tokenList(database *sql.DB) {
	rows, err := database.Query(`
		SELECT t.id, t.token, t.label, COALESCE(r.name, '-'), t.created_at, t.revoked
		FROM tokens t
		LEFT JOIN roles r ON t.role_id = r.id
		ORDER BY t.created_at
	`)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing tokens: %v\n", err)
		os.Exit(1)
	}
	defer rows.Close()

	fmt.Printf("%-24s %-24s %-16s %-12s %-20s %s\n", "ID", "TOKEN", "NAME", "ROLE", "CREATED", "REVOKED")
	for rows.Next() {
		var tid, token, name, roleName, createdAt string
		var revoked int
		if err := rows.Scan(&tid, &token, &name, &roleName, &createdAt, &revoked); err != nil {
			fmt.Fprintf(os.Stderr, "Error reading row: %v\n", err)
			os.Exit(1)
		}
		revokedStr := "no"
		if revoked == 1 {
			revokedStr = "yes"
		}
		fmt.Printf("%-24s %-24s %-16s %-12s %-20s %s\n", tid, token, name, roleName, createdAt, revokedStr)
	}
}

func tokenRevoke(database *sql.DB, args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: watcher token revoke <name>")
		os.Exit(1)
	}
	tokenValue := resolveToken(database, args[0])

	res, err := database.Exec("UPDATE tokens SET revoked = 1 WHERE token = ?", tokenValue)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error revoking token: %v\n", err)
		os.Exit(1)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		fmt.Fprintln(os.Stderr, "Token not found")
		os.Exit(1)
	}
	fmt.Printf("Token %q revoked\n", args[0])
}

func tokenRename(database *sql.DB, args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: watcher token rename <old-name> <new-name>")
		os.Exit(1)
	}
	oldName := args[0]
	newName := args[1]

	res, err := database.Exec("UPDATE tokens SET label = ? WHERE label = ?", newName, oldName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error renaming token: %v\n", err)
		os.Exit(1)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		fmt.Fprintf(os.Stderr, "Token %q not found\n", oldName)
		os.Exit(1)
	}
	fmt.Printf("Renamed token %q to %q\n", oldName, newName)
}

func tokenSetRole(database *sql.DB, args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: watcher token set-role <name> <role>")
		os.Exit(1)
	}
	tokenValue := resolveToken(database, args[0])
	roleName := args[1]

	var roleID string
	err := database.QueryRow("SELECT id FROM roles WHERE name = ?", roleName).Scan(&roleID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Role %q not found\n", roleName)
		os.Exit(1)
	}

	res, err := database.Exec("UPDATE tokens SET role_id = ? WHERE token = ?", roleID, tokenValue)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error updating role: %v\n", err)
		os.Exit(1)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		fmt.Fprintln(os.Stderr, "Token not found")
		os.Exit(1)
	}
	fmt.Printf("Token %q role set to %q\n", args[0], roleName)
}

func printTokenUsage() {
	fmt.Fprintln(os.Stderr, `Usage: watcher token <command>

Commands:
  generate --name <name> --role <role>  Generate a new auth token
  list                                   List all tokens
  revoke <name>                          Revoke a token
  rename <old-name> <new-name>           Rename a token
  set-role <name> <role>                 Change token role`)
}
