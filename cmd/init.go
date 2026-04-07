package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"watcher/config"
	"watcher/db"
)

// InitCmd bootstraps everything needed on a fresh machine:
// config file, directories, and database.
func InitCmd(configPath string) {
	path := configPath
	if path == "" {
		path = config.DefaultConfigPath()
	}

	// 1. Create config file
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating directory %s: %v\n", dir, err)
		os.Exit(1)
	}

	if _, err := os.Stat(path); err == nil {
		fmt.Printf("Config already exists: %s\n", path)
	} else {
		data, err := json.MarshalIndent(config.Defaults, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error marshaling defaults: %v\n", err)
			os.Exit(1)
		}
		data = append(data, '\n')
		if err := os.WriteFile(path, data, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing config: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Config created: %s\n", path)
	}

	// 2. Load config to get resolved paths
	fileCfg, _ := config.LoadFromFile(path)
	cfg := config.Resolve(fileCfg, nil)
	cfg.ExpandPaths()

	// 3. Create directories
	for _, d := range []string{filepath.Dir(cfg.DBPath), cfg.ScriptsDir, cfg.LogsDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			fmt.Fprintf(os.Stderr, "Error creating %s: %v\n", d, err)
			os.Exit(1)
		}
	}
	fmt.Printf("Scripts dir:  %s\n", cfg.ScriptsDir)
	fmt.Printf("Logs dir:     %s\n", cfg.LogsDir)

	// 4. Create/migrate database
	database, err := db.Open(cfg.DBPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating database: %v\n", err)
		os.Exit(1)
	}
	database.Close()
	fmt.Printf("Database:     %s\n", cfg.DBPath)

	fmt.Println("Initialization complete.")
}
