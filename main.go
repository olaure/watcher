package main

import (
	"fmt"
	"os"
	"path/filepath"

	"watcher/cmd"
	"watcher/config"
	"watcher/db"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	// Global flag: --config
	configPath := ""
	for i, arg := range os.Args {
		if arg == "--config" && i+1 < len(os.Args) {
			configPath = os.Args[i+1]
			// Remove --config and its value from args
			os.Args = append(os.Args[:i], os.Args[i+2:]...)
			break
		}
	}

	subcmd := os.Args[1]
	subArgs := os.Args[2:]

	// "config" subcommand handles its own DB access (bootstrap issue)
	if subcmd == "config" {
		cmd.ConfigCmd(subArgs, configPath)
		return
	}

	// "init" is handled separately — it bootstraps everything
	if subcmd == "init" {
		cmd.InitCmd(configPath)
		return
	}

	// For all other subcommands, load config and open DB
	cfg := loadConfig(configPath)

	// Ensure DB directory exists
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating database directory: %v\n", err)
		os.Exit(1)
	}

	database, err := db.Open(cfg.DBPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening database: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	switch subcmd {
	case "serve":
		cmd.ServeCmd(database, cfg, subArgs)
	case "token":
		cmd.TokenCmd(database, subArgs)
	case "script":
		cmd.ScriptCmd(database, cfg.ScriptsDir, subArgs)
	case "role":
		cmd.RoleCmd(database, subArgs)
	case "watcher":
		cmd.WatcherCmd(database, subArgs)
	case "setup":
		cmd.SetupCmd(database, cfg.ScriptsDir, subArgs)
	case "federation":
		cmd.FederationCmd(database, subArgs)
	default:
		printUsage()
		os.Exit(1)
	}
}

func loadConfig(configPath string) config.Config {
	path := configPath
	if path == "" {
		path = config.DefaultConfigPath()
	}

	fileCfg, err := config.LoadFromFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
	}

	// First resolve to get db_path
	cfg := config.Resolve(fileCfg, nil)
	cfg.ExpandPaths()

	// Try to load DB overrides
	database, err := db.Open(cfg.DBPath)
	if err != nil {
		// If DB doesn't exist yet, just use file + defaults
		return cfg
	}
	defer database.Close()

	dbOverrides, err := config.LoadDBOverrides(database)
	if err != nil {
		return cfg
	}

	cfg = config.Resolve(fileCfg, dbOverrides)
	cfg.ExpandPaths()
	return cfg
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `Usage: watcher [--config <path>] <command> [args...]

Commands:
  init                      Initialize config, directories, and database
  serve                     Start the HTTP server
  token                     Manage auth tokens
  script                    Manage scripts
  role                      Manage roles and permissions
  watcher                   Manage remote watchers (federation)
  setup                     Create script + role + token in one step
  federation                Manage push-based federation
  config                    Manage configuration

Global flags:
  --config <path>           Path to config file (default: ~/.watcher/config.json)

Run 'watcher <command>' for subcommand help.`)
}
