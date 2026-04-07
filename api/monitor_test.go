package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// setupTestDB creates an in-memory SQLite database with the full schema and seed data.
func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:?_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}

	stmts := []string{
		`CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`CREATE TABLE roles (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, parent_id TEXT REFERENCES roles(id), created_at TEXT NOT NULL DEFAULT (datetime('now')))`,
		`CREATE TABLE role_permissions (role_id TEXT NOT NULL REFERENCES roles(id) ON DELETE CASCADE, script_id TEXT NOT NULL, action TEXT NOT NULL CHECK(action IN ('launch','poll','*')), PRIMARY KEY (role_id, script_id, action))`,
		`CREATE TABLE tokens (id TEXT PRIMARY KEY, token TEXT NOT NULL UNIQUE, label TEXT NOT NULL DEFAULT '', role_id TEXT, created_at TEXT NOT NULL DEFAULT (datetime('now')), revoked INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE watchers (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, url TEXT NOT NULL, token TEXT NOT NULL, headers TEXT NOT NULL DEFAULT '{}', created_at TEXT NOT NULL DEFAULT (datetime('now')))`,
		`CREATE TABLE scripts (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, path TEXT NOT NULL, args TEXT NOT NULL DEFAULT '[]', enabled INTEGER NOT NULL DEFAULT 1, auto_cleanup INTEGER NOT NULL DEFAULT 0, log_ttl_sec INTEGER NOT NULL DEFAULT 3600, watcher_id TEXT REFERENCES watchers(id), remote_script_id TEXT, created_at TEXT NOT NULL DEFAULT (datetime('now')))`,
		`CREATE TABLE runs (id TEXT PRIMARY KEY, script_id TEXT NOT NULL REFERENCES scripts(id), pid INTEGER, status TEXT NOT NULL DEFAULT 'running', exit_code INTEGER, log_file TEXT NOT NULL, logs_pruned INTEGER NOT NULL DEFAULT 0, started_at TEXT NOT NULL DEFAULT (datetime('now')), finished_at TEXT)`,
		`CREATE TABLE poll_cursors (token_id TEXT NOT NULL REFERENCES tokens(id), run_id TEXT NOT NULL REFERENCES runs(id), byte_offset INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (token_id, run_id))`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("schema: %s: %v", s, err)
		}
	}

	// Seed: admin role + token
	db.Exec(`INSERT INTO roles (id, name) VALUES ('role-admin', 'admin')`)
	db.Exec(`INSERT INTO role_permissions (role_id, script_id, action) VALUES ('role-admin', '*', '*')`)
	db.Exec(`INSERT INTO tokens (id, token, label, role_id) VALUES ('tok-admin', 'secret-admin', 'admin', 'role-admin')`)

	// Seed: viewer role + token (non-admin)
	db.Exec(`INSERT INTO roles (id, name) VALUES ('role-viewer', 'viewer')`)
	db.Exec(`INSERT INTO role_permissions (role_id, script_id, action) VALUES ('role-viewer', '*', 'poll')`)
	db.Exec(`INSERT INTO tokens (id, token, label, role_id) VALUES ('tok-viewer', 'secret-viewer', 'viewer', 'role-viewer')`)

	// Seed: revoked token
	db.Exec(`INSERT INTO tokens (id, token, label, role_id, revoked) VALUES ('tok-revoked', 'secret-revoked', 'old', 'role-admin', 1)`)

	// Seed: local scripts
	db.Exec(`INSERT INTO scripts (id, name, path, enabled) VALUES ('s1', 'deploy', '/opt/deploy.sh', 1)`)
	db.Exec(`INSERT INTO scripts (id, name, path, enabled) VALUES ('s2', 'backup', '/opt/backup.sh', 1)`)
	db.Exec(`INSERT INTO scripts (id, name, path, enabled) VALUES ('s3', 'disabled', '/opt/disabled.sh', 0)`)

	// Seed: runs
	db.Exec(`INSERT INTO runs (id, script_id, status, exit_code, log_file, started_at, finished_at) VALUES ('r1', 's1', 'success', 0, '/tmp/r1.log', datetime('now', '-1 hour'), datetime('now', '-59 minutes'))`)
	db.Exec(`INSERT INTO runs (id, script_id, status, exit_code, log_file, started_at, finished_at) VALUES ('r2', 's1', 'failed', 1, '/tmp/r2.log', datetime('now', '-30 minutes'), datetime('now', '-29 minutes'))`)
	db.Exec(`INSERT INTO runs (id, script_id, status, log_file, started_at) VALUES ('r3', 's2', 'running', '/tmp/r3.log', datetime('now', '-5 minutes'))`)

	return db
}

// reqWithToken creates a GET request with a token ID injected into context.
func reqWithToken(path, tokenID string) *http.Request {
	req := httptest.NewRequest("GET", path, nil)
	ctx := context.WithValue(req.Context(), tokenIDKey, tokenID)
	return req.WithContext(ctx)
}

func TestOverviewHandler_AdminAccess(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	handler := OverviewHandler(db)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, reqWithToken("/api/overview", "tok-admin"))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]map[string]int
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}

	if resp["scripts"]["total"] != 3 {
		t.Errorf("scripts.total = %d, want 3", resp["scripts"]["total"])
	}
	if resp["scripts"]["enabled"] != 2 {
		t.Errorf("scripts.enabled = %d, want 2", resp["scripts"]["enabled"])
	}
	if resp["scripts"]["remote"] != 0 {
		t.Errorf("scripts.remote = %d, want 0", resp["scripts"]["remote"])
	}
	if resp["tokens"]["total"] != 3 {
		t.Errorf("tokens.total = %d, want 3", resp["tokens"]["total"])
	}
	if resp["tokens"]["revoked"] != 1 {
		t.Errorf("tokens.revoked = %d, want 1", resp["tokens"]["revoked"])
	}
	if resp["runs"]["active"] != 1 {
		t.Errorf("runs.active = %d, want 1", resp["runs"]["active"])
	}
}

func TestOverviewHandler_NonAdminDenied(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	handler := OverviewHandler(db)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, reqWithToken("/api/overview", "tok-viewer"))

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
}

func TestScriptsHandler(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	handler := ScriptsHandler(db)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, reqWithToken("/api/scripts", "tok-admin"))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var scripts []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &scripts); err != nil {
		t.Fatal(err)
	}

	if len(scripts) != 3 {
		t.Fatalf("expected 3 scripts, got %d", len(scripts))
	}

	// Scripts ordered by name: backup, deploy, disabled
	backup := scripts[0]
	if backup["name"] != "backup" {
		t.Errorf("first script name = %v, want backup", backup["name"])
	}
	if backup["type"] != "local" {
		t.Errorf("backup type = %v, want local", backup["type"])
	}
	if backup["watcher"] != nil {
		t.Errorf("backup watcher should be nil")
	}

	deploy := scripts[1]
	if deploy["name"] != "deploy" {
		t.Errorf("second script name = %v, want deploy", deploy["name"])
	}
	if deploy["last_run"] == nil {
		t.Error("deploy last_run should not be nil")
	} else {
		lr := deploy["last_run"].(map[string]any)
		if lr["status"] != "failed" {
			t.Errorf("deploy last_run status = %v, want failed", lr["status"])
		}
	}

	disabled := scripts[2]
	if disabled["enabled"] != false {
		t.Errorf("disabled script enabled = %v, want false", disabled["enabled"])
	}
}

func TestRunsHandler(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	handler := RunsHandler(db)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, reqWithToken("/api/runs", "tok-admin"))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var runs []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &runs); err != nil {
		t.Fatal(err)
	}

	if len(runs) != 3 {
		t.Fatalf("expected 3 runs, got %d", len(runs))
	}

	// Newest first: r3 (running/backup), r2 (failed/deploy), r1 (success/deploy)
	if runs[0]["status"] != "running" {
		t.Errorf("first run status = %v, want running", runs[0]["status"])
	}
	if runs[0]["script_name"] != "backup" {
		t.Errorf("first run script_name = %v, want backup", runs[0]["script_name"])
	}
}

func TestRunsHandler_Limit(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	handler := RunsHandler(db)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, reqWithToken("/api/runs?limit=1", "tok-admin"))

	var runs []map[string]any
	json.Unmarshal(w.Body.Bytes(), &runs)
	if len(runs) != 1 {
		t.Errorf("expected 1 run with limit=1, got %d", len(runs))
	}
}

func TestRunsHandler_MaxLimit(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	handler := RunsHandler(db)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, reqWithToken("/api/runs?limit=999", "tok-admin"))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestWatchersHandler_Empty(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	handler := WatchersHandler(db)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, reqWithToken("/api/watchers", "tok-admin"))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var watchers []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &watchers); err != nil {
		t.Fatal(err)
	}
	if len(watchers) != 0 {
		t.Errorf("expected 0 watchers, got %d", len(watchers))
	}
}

func TestWatchersHandler_WithHealthCheck(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Fake remote watcher that responds healthy
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer fake.Close()

	db.Exec(`INSERT INTO watchers (id, name, url, token) VALUES ('w1', 'prod', ?, 'tok123')`, fake.URL)
	db.Exec(`UPDATE scripts SET watcher_id = 'w1', remote_script_id = 'remote-s1' WHERE id = 's1'`)

	handler := WatchersHandler(db)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, reqWithToken("/api/watchers", "tok-admin"))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var watchers []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &watchers); err != nil {
		t.Fatal(err)
	}
	if len(watchers) != 1 {
		t.Fatalf("expected 1 watcher, got %d", len(watchers))
	}

	w1 := watchers[0]
	if w1["name"] != "prod" {
		t.Errorf("watcher name = %v, want prod", w1["name"])
	}
	if w1["healthy"] != true {
		t.Errorf("watcher healthy = %v, want true", w1["healthy"])
	}
	if int(w1["scripts"].(float64)) != 1 {
		t.Errorf("watcher scripts = %v, want 1", w1["scripts"])
	}
}

func TestWatchersHandler_UnhealthyWatcher(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Fake remote watcher that responds unhealthy
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer fake.Close()

	db.Exec(`INSERT INTO watchers (id, name, url, token) VALUES ('w1', 'prod', ?, 'tok123')`, fake.URL)

	handler := WatchersHandler(db)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, reqWithToken("/api/watchers", "tok-admin"))

	var watchers []map[string]any
	json.Unmarshal(w.Body.Bytes(), &watchers)

	if watchers[0]["healthy"] != false {
		t.Errorf("unhealthy watcher reported as healthy")
	}
}

func TestOverviewHandler_WithRemoteScripts(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer fake.Close()

	db.Exec(`INSERT INTO watchers (id, name, url, token) VALUES ('w1', 'prod', ?, 'tok123')`, fake.URL)
	db.Exec(`UPDATE scripts SET watcher_id = 'w1', remote_script_id = 'remote-s1' WHERE id = 's1'`)

	handler := OverviewHandler(db)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, reqWithToken("/api/overview", "tok-admin"))

	var resp map[string]map[string]int
	json.Unmarshal(w.Body.Bytes(), &resp)

	if resp["scripts"]["remote"] != 1 {
		t.Errorf("scripts.remote = %d, want 1", resp["scripts"]["remote"])
	}
	if resp["watchers"]["healthy"] != 1 {
		t.Errorf("watchers.healthy = %d, want 1", resp["watchers"]["healthy"])
	}
}

func TestWatcherRemoteScriptsHandler(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Fake remote that serves /api/scripts
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/scripts" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"id":"rs1","name":"remote-deploy","enabled":true,"type":"local","last_run":{"id":"rr1","status":"success"}}]`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer fake.Close()

	db.Exec(`INSERT INTO watchers (id, name, url, token) VALUES ('w1', 'prod', ?, 'tok123')`, fake.URL)

	handler := WatcherRemoteScriptsHandler(db)
	req := reqWithToken("/api/watchers/prod/scripts", "tok-admin")
	req.SetPathValue("name", "prod")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var scripts []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &scripts); err != nil {
		t.Fatal(err)
	}
	if len(scripts) != 1 {
		t.Fatalf("expected 1 script, got %d", len(scripts))
	}
	if scripts[0]["name"] != "remote-deploy" {
		t.Errorf("script name = %v, want remote-deploy", scripts[0]["name"])
	}
}

func TestWatcherRemoteScriptsHandler_NotFound(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	handler := WatcherRemoteScriptsHandler(db)
	req := reqWithToken("/api/watchers/nonexistent/scripts", "tok-admin")
	req.SetPathValue("name", "nonexistent")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestWatcherRemoteRunsHandler(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Fake remote that serves /api/runs
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/runs" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"id":"rr1","script_id":"rs1","script_name":"deploy","status":"success"}]`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer fake.Close()

	db.Exec(`INSERT INTO watchers (id, name, url, token) VALUES ('w1', 'prod', ?, 'tok123')`, fake.URL)

	handler := WatcherRemoteRunsHandler(db)
	req := reqWithToken("/api/watchers/prod/runs?limit=5", "tok-admin")
	req.SetPathValue("name", "prod")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var runs []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &runs); err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs))
	}
	if runs[0]["status"] != "success" {
		t.Errorf("run status = %v, want success", runs[0]["status"])
	}
}

func TestWatcherRemoteRunsHandler_LimitForwarded(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	var receivedPath string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.String()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	}))
	defer fake.Close()

	db.Exec(`INSERT INTO watchers (id, name, url, token) VALUES ('w1', 'prod', ?, 'tok123')`, fake.URL)

	handler := WatcherRemoteRunsHandler(db)
	req := reqWithToken("/api/watchers/prod/runs?limit=10", "tok-admin")
	req.SetPathValue("name", "prod")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if receivedPath != "/api/runs?limit=10" {
		t.Errorf("remote received path = %v, want /api/runs?limit=10", receivedPath)
	}
}

func TestAllEndpoints_NoToken(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	handlers := map[string]http.HandlerFunc{
		"/api/overview": OverviewHandler(db),
		"/api/scripts":  ScriptsHandler(db),
		"/api/runs":     RunsHandler(db),
		"/api/watchers": WatchersHandler(db),
	}

	for path, handler := range handlers {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, reqWithToken(path, ""))
		if w.Code != http.StatusForbidden {
			t.Errorf("%s with no token: expected 403, got %d", path, w.Code)
		}
	}
}
