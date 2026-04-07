package db

const schema = `
CREATE TABLE IF NOT EXISTS config (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS tokens (
    id         TEXT PRIMARY KEY,
    token      TEXT NOT NULL UNIQUE,
    label      TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    revoked    INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS scripts (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL UNIQUE,
    path         TEXT NOT NULL,
    args         TEXT NOT NULL DEFAULT '[]',
    enabled      INTEGER NOT NULL DEFAULT 1,
    auto_cleanup INTEGER NOT NULL DEFAULT 0,
    log_ttl_sec  INTEGER NOT NULL DEFAULT 3600,
    created_at   TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS runs (
    id          TEXT PRIMARY KEY,
    script_id   TEXT NOT NULL REFERENCES scripts(id),
    pid         INTEGER,
    status      TEXT NOT NULL DEFAULT 'running',
    exit_code   INTEGER,
    log_file    TEXT NOT NULL,
    logs_pruned INTEGER NOT NULL DEFAULT 0,
    started_at  TEXT NOT NULL DEFAULT (datetime('now')),
    finished_at TEXT
);

CREATE TABLE IF NOT EXISTS poll_cursors (
    token_id    TEXT NOT NULL REFERENCES tokens(id),
    run_id      TEXT NOT NULL REFERENCES runs(id),
    byte_offset INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (token_id, run_id)
);

CREATE TABLE IF NOT EXISTS roles (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    parent_id  TEXT REFERENCES roles(id),
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS role_permissions (
    role_id   TEXT NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    script_id TEXT NOT NULL,
    action    TEXT NOT NULL CHECK(action IN ('launch', 'poll', '*')),
    PRIMARY KEY (role_id, script_id, action)
);

CREATE TABLE IF NOT EXISTS watchers (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    url        TEXT NOT NULL,
    token      TEXT NOT NULL,
    headers    TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS federation (
    id           INTEGER PRIMARY KEY CHECK (id = 1),
    hub_url      TEXT NOT NULL,
    hub_token    TEXT NOT NULL,
    hub_headers  TEXT NOT NULL DEFAULT '{}',
    remote_url   TEXT NOT NULL,
    remote_token TEXT NOT NULL
);
`
