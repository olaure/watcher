package cmd

import (
	"database/sql"
	"fmt"
	"os"
	"strings"

	"watcher/id"
)

func RoleCmd(database *sql.DB, args []string) {
	if len(args) == 0 {
		printRoleUsage()
		os.Exit(1)
	}

	switch args[0] {
	case "create":
		roleCreate(database, args[1:])
	case "list":
		roleList(database)
	case "delete":
		roleDelete(database, args[1:])
	case "rename":
		roleRename(database, args[1:])
	case "set-parent":
		roleSetParent(database, args[1:])
	case "grant":
		roleGrant(database, args[1:])
	case "revoke":
		roleRevoke(database, args[1:])
	default:
		printRoleUsage()
		os.Exit(1)
	}
}

func roleCreate(database *sql.DB, args []string) {
	var name, parentName string
	for i := 0; i < len(args); i++ {
		if (args[i] == "--parent" || args[i] == "-parent") && i+1 < len(args) {
			parentName = args[i+1]
			i++
		} else if args[i][0] != '-' {
			name = args[i]
		}
	}

	if name == "" {
		fmt.Fprintln(os.Stderr, "Usage: watcher role create <name> [--parent <name>]")
		os.Exit(1)
	}

	var parentID *string
	if parentName != "" {
		var pid string
		err := database.QueryRow("SELECT id FROM roles WHERE name = ?", parentName).Scan(&pid)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Parent role %q not found\n", parentName)
			os.Exit(1)
		}
		parentID = &pid
	}

	roleID, err := id.New()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error generating ID: %v\n", err)
		os.Exit(1)
	}

	_, err = database.Exec(
		"INSERT INTO roles (id, name, parent_id) VALUES (?, ?, ?)",
		roleID, name, parentID,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating role: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Created role %q (id: %s)\n", name, roleID)
}

func roleList(database *sql.DB) {
	rows, err := database.Query(`
		SELECT r.id, r.name, COALESCE(p.name, '-'), r.created_at
		FROM roles r
		LEFT JOIN roles p ON r.parent_id = p.id
		ORDER BY r.name
	`)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing roles: %v\n", err)
		os.Exit(1)
	}
	defer rows.Close()

	fmt.Printf("%-24s %-16s %-16s %s\n", "ID", "NAME", "PARENT", "CREATED")
	for rows.Next() {
		var rid, name, parent, createdAt string
		if err := rows.Scan(&rid, &name, &parent, &createdAt); err != nil {
			fmt.Fprintf(os.Stderr, "Error reading row: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("%-24s %-16s %-16s %s\n", rid, name, parent, createdAt)
	}

	// Show permissions
	permRows, err := database.Query(`
		SELECT r.name, COALESCE(s.name, rp.script_id), rp.action
		FROM role_permissions rp
		JOIN roles r ON rp.role_id = r.id
		LEFT JOIN scripts s ON rp.script_id = s.id
		ORDER BY r.name, rp.script_id
	`)
	if err != nil {
		return
	}
	defer permRows.Close()

	fmt.Println("\nPermissions:")
	fmt.Printf("  %-16s %-24s %s\n", "ROLE", "SCRIPT", "ACTION")
	for permRows.Next() {
		var roleName, scriptDisplay, action string
		if err := permRows.Scan(&roleName, &scriptDisplay, &action); err != nil {
			continue
		}
		fmt.Printf("  %-16s %-24s %s\n", roleName, scriptDisplay, action)
	}
}

func roleDelete(database *sql.DB, args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: watcher role delete <name>")
		os.Exit(1)
	}
	name := args[0]

	var roleID string
	err := database.QueryRow("SELECT id FROM roles WHERE name = ?", name).Scan(&roleID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Role %q not found\n", name)
		os.Exit(1)
	}

	var tokenCount int
	database.QueryRow("SELECT COUNT(*) FROM tokens WHERE role_id = ?", roleID).Scan(&tokenCount)
	if tokenCount > 0 {
		fmt.Fprintf(os.Stderr, "Cannot delete role %q: %d token(s) still assigned\n", name, tokenCount)
		os.Exit(1)
	}

	var childCount int
	database.QueryRow("SELECT COUNT(*) FROM roles WHERE parent_id = ?", roleID).Scan(&childCount)
	if childCount > 0 {
		fmt.Fprintf(os.Stderr, "Cannot delete role %q: %d child role(s) still reference it\n", name, childCount)
		os.Exit(1)
	}

	_, err = database.Exec("DELETE FROM roles WHERE id = ?", roleID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error deleting role: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Deleted role %q\n", name)
}

func roleRename(database *sql.DB, args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: watcher role rename <old-name> <new-name>")
		os.Exit(1)
	}
	oldName := args[0]
	newName := args[1]

	res, err := database.Exec("UPDATE roles SET name = ? WHERE name = ?", newName, oldName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error renaming role: %v\n", err)
		os.Exit(1)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		fmt.Fprintf(os.Stderr, "Role %q not found\n", oldName)
		os.Exit(1)
	}
	fmt.Printf("Renamed role %q to %q\n", oldName, newName)
}

func roleSetParent(database *sql.DB, args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: watcher role set-parent <name> <parent|->")
		os.Exit(1)
	}
	name := args[0]
	parentArg := args[1]

	var parentID *string
	if parentArg != "-" {
		var pid string
		err := database.QueryRow("SELECT id FROM roles WHERE name = ?", parentArg).Scan(&pid)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Parent role %q not found\n", parentArg)
			os.Exit(1)
		}
		parentID = &pid
	}

	res, err := database.Exec("UPDATE roles SET parent_id = ? WHERE name = ?", parentID, name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error updating parent: %v\n", err)
		os.Exit(1)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		fmt.Fprintf(os.Stderr, "Role %q not found\n", name)
		os.Exit(1)
	}
	if parentArg == "-" {
		fmt.Printf("Cleared parent of role %q\n", name)
	} else {
		fmt.Printf("Set parent of role %q to %q\n", name, parentArg)
	}
}

func roleGrant(database *sql.DB, args []string) {
	if len(args) < 3 {
		fmt.Fprintln(os.Stderr, "Usage: watcher role grant <role> <script|*> <action>")
		fmt.Fprintln(os.Stderr, "  action: launch, poll, all")
		os.Exit(1)
	}
	roleName := args[0]
	scriptArg := args[1]
	action := normalizeAction(args[2])

	var roleID string
	err := database.QueryRow("SELECT id FROM roles WHERE name = ?", roleName).Scan(&roleID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Role %q not found\n", roleName)
		os.Exit(1)
	}

	scriptID := scriptArg
	scriptDisplay := scriptArg
	if scriptArg != "*" {
		err = database.QueryRow("SELECT id FROM scripts WHERE name = ?", scriptArg).Scan(&scriptID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Script %q not found\n", scriptArg)
			os.Exit(1)
		}
		scriptDisplay = scriptArg
	}

	_, err = database.Exec(
		"INSERT OR IGNORE INTO role_permissions (role_id, script_id, action) VALUES (?, ?, ?)",
		roleID, scriptID, action,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error granting permission: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Granted %s on %s to role %q\n", action, scriptDisplay, roleName)
}

func roleRevoke(database *sql.DB, args []string) {
	if len(args) < 3 {
		fmt.Fprintln(os.Stderr, "Usage: watcher role revoke <role> <script|*> <action>")
		fmt.Fprintln(os.Stderr, "  action: launch, poll, all")
		os.Exit(1)
	}
	roleName := args[0]
	scriptArg := args[1]
	action := normalizeAction(args[2])

	var roleID string
	err := database.QueryRow("SELECT id FROM roles WHERE name = ?", roleName).Scan(&roleID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Role %q not found\n", roleName)
		os.Exit(1)
	}

	scriptID := scriptArg
	if scriptArg != "*" {
		err = database.QueryRow("SELECT id FROM scripts WHERE name = ?", scriptArg).Scan(&scriptID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Script %q not found\n", scriptArg)
			os.Exit(1)
		}
	}

	res, err := database.Exec(
		"DELETE FROM role_permissions WHERE role_id = ? AND script_id = ? AND action = ?",
		roleID, scriptID, action,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error revoking permission: %v\n", err)
		os.Exit(1)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		fmt.Fprintln(os.Stderr, "Permission not found")
		os.Exit(1)
	}
	fmt.Println("Permission revoked")
}

func normalizeAction(s string) string {
	switch strings.ToLower(s) {
	case "all", "*":
		return "*"
	case "launch":
		return "launch"
	case "poll":
		return "poll"
	default:
		fmt.Fprintf(os.Stderr, "Invalid action %q (expected: launch, poll, all)\n", s)
		os.Exit(1)
		return ""
	}
}

func printRoleUsage() {
	fmt.Fprintln(os.Stderr, `Usage: watcher role <command>

Commands:
  create <name> [--parent <name>]    Create a role
  list                                List all roles and permissions
  delete <name>                       Delete a role (no tokens or children)
  rename <old-name> <new-name>        Rename a role
  set-parent <name> <parent|->        Set parent role ('-' to clear)
  grant <role> <script|*> <action>    Grant permission (action: launch, poll, all)
  revoke <role> <script|*> <action>   Revoke permission`)
}
