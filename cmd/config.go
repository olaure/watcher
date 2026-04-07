package cmd

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"watcher/config"
	"watcher/db"
)

func ConfigCmd(args []string, configPath string) {
	if len(args) == 0 {
		printConfigUsage()
		os.Exit(1)
	}

	switch args[0] {
	case "init":
		configInit(configPath)
	case "get":
		configGet(args[1:], configPath)
	case "set":
		configSet(args[1:], configPath)
	case "reset":
		configReset(args[1:], configPath)
	case "list":
		configList(configPath)
	default:
		printConfigUsage()
		os.Exit(1)
	}
}

func configInit(configPath string) {
	path := configPath
	if path == "" {
		path = config.DefaultConfigPath()
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating directory %s: %v\n", dir, err)
		os.Exit(1)
	}

	if _, err := os.Stat(path); err == nil {
		fmt.Fprintf(os.Stderr, "Config file already exists: %s\n", path)
		os.Exit(1)
	}

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

	// Create subdirectories
	cfg := config.Defaults
	cfg.ExpandPaths()
	for _, d := range []string{cfg.ScriptsDir, cfg.LogsDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not create %s: %v\n", d, err)
		}
	}

	fmt.Printf("Config created: %s\n", path)
	fmt.Printf("Scripts dir:    %s\n", cfg.ScriptsDir)
	fmt.Printf("Logs dir:       %s\n", cfg.LogsDir)
}

func configGet(args []string, configPath string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: watcher config get <key>")
		os.Exit(1)
	}
	key := args[0]

	fileCfg, dbOverrides := loadConfigLayers(configPath)

	rv, err := config.Get(key, fileCfg, dbOverrides)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("%s = %s  (source: %s)\n", key, rv.Value, rv.Source)
}

func configSet(args []string, configPath string) {
	fs := flag.NewFlagSet("config set", flag.ExitOnError)
	fs.Parse(args)
	if fs.NArg() < 2 {
		fmt.Fprintln(os.Stderr, "Usage: watcher config set <key> <value>")
		os.Exit(1)
	}
	key := fs.Arg(0)
	value := fs.Arg(1)

	fileCfg := loadFileCfg(configPath)
	resolved := config.Resolve(fileCfg, nil)
	resolved.ExpandPaths()

	database, err := db.Open(resolved.DBPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening database: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	if err := config.Set(database, key, value); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("%s = %s  (source: db)\n", key, value)
}

func configReset(args []string, configPath string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: watcher config reset <key>")
		os.Exit(1)
	}
	key := args[0]

	fileCfg := loadFileCfg(configPath)
	resolved := config.Resolve(fileCfg, nil)
	resolved.ExpandPaths()

	database, err := db.Open(resolved.DBPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening database: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	if err := config.Reset(database, key); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Reset %s to file/default value\n", key)
}

func configList(configPath string) {
	fileCfg, dbOverrides := loadConfigLayers(configPath)

	all := config.ResolveAll(fileCfg, dbOverrides)
	for _, key := range config.ValidKeys {
		rv := all[key]
		fmt.Printf("%-16s = %-40s (source: %s)\n", key, rv.Value, rv.Source)
	}
}

func loadFileCfg(configPath string) config.Config {
	path := configPath
	if path == "" {
		path = config.DefaultConfigPath()
	}
	fileCfg, err := config.LoadFromFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not read config file: %v\n", err)
	}
	return fileCfg
}

func loadConfigLayers(configPath string) (config.Config, map[string]string) {
	fileCfg := loadFileCfg(configPath)

	// Resolve just enough to find db_path
	resolved := config.Resolve(fileCfg, nil)
	resolved.ExpandPaths()

	database, err := db.Open(resolved.DBPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not open database: %v\n", err)
		return fileCfg, nil
	}
	defer database.Close()

	dbOverrides, err := config.LoadDBOverrides(database)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not read DB overrides: %v\n", err)
		return fileCfg, nil
	}

	return fileCfg, dbOverrides
}

func printConfigUsage() {
	fmt.Fprintln(os.Stderr, `Usage: watcher config <command>

Commands:
  init              Create default config file and directories
  get <key>         Show resolved value and source
  set <key> <value> Set a DB override
  reset <key>       Remove DB override (fall back to file/default)
  list              Show all config keys with resolved values

Valid keys: db_path, scripts_dir, logs_dir, listen_addr, prune_interval`)
}
