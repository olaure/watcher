package db

import (
	"database/sql"

	_ "github.com/mattn/go-sqlite3"

	"watcher/id"
)

// Open opens (or creates) the SQLite database at path, configures WAL mode,
// and runs migrations.
func Open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", path+"?_foreign_keys=on")
	if err != nil {
		return nil, err
	}

	// WAL mode: concurrent reads while writing
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, err
	}

	// Wait up to 5s instead of returning SQLITE_BUSY
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		db.Close()
		return nil, err
	}

	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)

	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}

	return db, nil
}

func migrate(database *sql.DB) error {
	if _, err := database.Exec(schema); err != nil {
		return err
	}
	// Add pid column to existing runs tables (ignored if already present).
	database.Exec("ALTER TABLE runs ADD COLUMN pid INTEGER")
	// Add args column to scripts (ignored if already present).
	database.Exec("ALTER TABLE scripts ADD COLUMN args TEXT NOT NULL DEFAULT '[]'")
	// RBAC: add role_id to tokens, seed default roles.
	database.Exec("ALTER TABLE tokens ADD COLUMN role_id TEXT")
	// Unique index on token names (label column); partial to allow legacy empty names.
	database.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_tokens_label ON tokens(label) WHERE label != ''")
	seedRoles(database)
	// Federation: add watcher columns to scripts.
	database.Exec("ALTER TABLE scripts ADD COLUMN watcher_id TEXT REFERENCES watchers(id)")
	database.Exec("ALTER TABLE scripts ADD COLUMN remote_script_id TEXT")
	// Federation: link watcher to its hub auth token.
	database.Exec("ALTER TABLE watchers ADD COLUMN token_id TEXT REFERENCES tokens(id)")
	return nil
}

// seedRoles creates viewer, deployer, and admin roles if they don't exist,
// and assigns all unassigned tokens to admin.
func seedRoles(database *sql.DB) {
	// Only seed if no roles exist yet.
	var count int
	database.QueryRow("SELECT COUNT(*) FROM roles").Scan(&count)
	if count > 0 {
		return
	}

	viewerID, _ := id.New()
	deployerID, _ := id.New()
	adminID, _ := id.New()

	database.Exec("INSERT INTO roles (id, name) VALUES (?, 'viewer')", viewerID)
	database.Exec("INSERT INTO roles (id, name, parent_id) VALUES (?, 'deployer', ?)", deployerID, viewerID)
	database.Exec("INSERT INTO roles (id, name) VALUES (?, 'admin')", adminID)

	// viewer: poll on all scripts
	database.Exec("INSERT INTO role_permissions (role_id, script_id, action) VALUES (?, '*', 'poll')", viewerID)
	// admin: all actions on all scripts
	database.Exec("INSERT INTO role_permissions (role_id, script_id, action) VALUES (?, '*', '*')", adminID)

	// Assign existing tokens without a role to admin.
	database.Exec("UPDATE tokens SET role_id = ? WHERE role_id IS NULL", adminID)
}
