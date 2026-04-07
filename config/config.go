package config

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var Defaults = Config{
	DBPath:        "~/.watcher/watcher.db",
	ScriptsDir:    "~/.watcher/scripts",
	LogsDir:       "~/.watcher/logs",
	ListenAddr:    ":8082",
	PruneInterval: "60s",
	EnableAPI:     false,
}

var ValidKeys = []string{"db_path", "scripts_dir", "logs_dir", "listen_addr", "prune_interval", "enable_api"}

type Config struct {
	DBPath        string `json:"db_path"`
	ScriptsDir    string `json:"scripts_dir"`
	LogsDir       string `json:"logs_dir"`
	ListenAddr    string `json:"listen_addr"`
	PruneInterval string `json:"prune_interval"`
	EnableAPI     bool   `json:"enable_api"`
}

// Source indicates where a config value came from.
type Source string

const (
	SourceDefault Source = "default"
	SourceFile    Source = "file"
	SourceDB      Source = "db"
)

// ResolvedValue holds a config value and its origin.
type ResolvedValue struct {
	Value  string
	Source Source
}

// ExpandHome replaces a leading ~ with the user's home directory.
func ExpandHome(path string) string {
	if !strings.HasPrefix(path, "~") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[1:])
}

// DefaultConfigPath returns ~/.watcher/config.json.
func DefaultConfigPath() string {
	return ExpandHome("~/.watcher/config.json")
}

// LoadFromFile reads a JSON config file. Returns zero Config and no error if the file doesn't exist.
func LoadFromFile(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, nil
		}
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return Config{}, fmt.Errorf("parsing config file %s: %w", path, err)
	}
	return c, nil
}

// LoadDBOverrides reads key/value pairs from the config table.
func LoadDBOverrides(db *sql.DB) (map[string]string, error) {
	rows, err := db.Query("SELECT key, value FROM config")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	overrides := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		overrides[k] = v
	}
	return overrides, rows.Err()
}

// Resolve merges defaults, file values, and DB overrides (DB wins > file wins > defaults).
func Resolve(fileConfig Config, dbOverrides map[string]string) Config {
	c := Defaults

	// File layer
	if fileConfig.DBPath != "" {
		c.DBPath = fileConfig.DBPath
	}
	if fileConfig.ScriptsDir != "" {
		c.ScriptsDir = fileConfig.ScriptsDir
	}
	if fileConfig.LogsDir != "" {
		c.LogsDir = fileConfig.LogsDir
	}
	if fileConfig.ListenAddr != "" {
		c.ListenAddr = fileConfig.ListenAddr
	}
	if fileConfig.PruneInterval != "" {
		c.PruneInterval = fileConfig.PruneInterval
	}
	if fileConfig.EnableAPI {
		c.EnableAPI = true
	}

	// DB layer
	if v, ok := dbOverrides["db_path"]; ok {
		c.DBPath = v
	}
	if v, ok := dbOverrides["scripts_dir"]; ok {
		c.ScriptsDir = v
	}
	if v, ok := dbOverrides["logs_dir"]; ok {
		c.LogsDir = v
	}
	if v, ok := dbOverrides["listen_addr"]; ok {
		c.ListenAddr = v
	}
	if v, ok := dbOverrides["prune_interval"]; ok {
		c.PruneInterval = v
	}
	if v, ok := dbOverrides["enable_api"]; ok {
		c.EnableAPI = v == "true"
	}

	return c
}

// ResolveAll returns each config key with its resolved value and source.
func ResolveAll(fileConfig Config, dbOverrides map[string]string) map[string]ResolvedValue {
	result := make(map[string]ResolvedValue)

	for _, key := range ValidKeys {
		def := getField(Defaults, key)
		file := getField(fileConfig, key)
		dbVal, dbOk := dbOverrides[key]

		rv := ResolvedValue{Value: def, Source: SourceDefault}
		if file != "" {
			rv = ResolvedValue{Value: file, Source: SourceFile}
		}
		if dbOk {
			rv = ResolvedValue{Value: dbVal, Source: SourceDB}
		}
		result[key] = rv
	}
	return result
}

// Get returns a single resolved value.
func Get(key string, fileConfig Config, dbOverrides map[string]string) (ResolvedValue, error) {
	if !isValidKey(key) {
		return ResolvedValue{}, fmt.Errorf("unknown config key: %s", key)
	}
	all := ResolveAll(fileConfig, dbOverrides)
	return all[key], nil
}

// Set writes a DB override.
func Set(db *sql.DB, key, value string) error {
	if !isValidKey(key) {
		return fmt.Errorf("unknown config key: %s", key)
	}
	_, err := db.Exec(
		"INSERT INTO config (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
		key, value,
	)
	return err
}

// Reset removes a DB override.
func Reset(db *sql.DB, key string) error {
	if !isValidKey(key) {
		return fmt.Errorf("unknown config key: %s", key)
	}
	_, err := db.Exec("DELETE FROM config WHERE key = ?", key)
	return err
}

// ExpandPaths resolves ~ in all path fields.
func (c *Config) ExpandPaths() {
	c.DBPath = ExpandHome(c.DBPath)
	c.ScriptsDir = ExpandHome(c.ScriptsDir)
	c.LogsDir = ExpandHome(c.LogsDir)
}

// ParsePruneInterval parses the prune_interval string as a duration.
func (c *Config) ParsePruneInterval() (time.Duration, error) {
	return time.ParseDuration(c.PruneInterval)
}

func getField(c Config, key string) string {
	switch key {
	case "db_path":
		return c.DBPath
	case "scripts_dir":
		return c.ScriptsDir
	case "logs_dir":
		return c.LogsDir
	case "listen_addr":
		return c.ListenAddr
	case "prune_interval":
		return c.PruneInterval
	case "enable_api":
		if c.EnableAPI {
			return "true"
		}
		return "false"
	}
	return ""
}

func isValidKey(key string) bool {
	for _, k := range ValidKeys {
		if k == key {
			return true
		}
	}
	return false
}
