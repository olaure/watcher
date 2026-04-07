package cmd

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"watcher/id"
)

// FederationCmd handles the `watcher federation` subcommand.
func FederationCmd(database *sql.DB, args []string) {
	if len(args) == 0 {
		printFederationUsage()
		os.Exit(1)
	}

	switch args[0] {
	case "invite":
		federationInvite(database, args[1:])
	case "join":
		federationJoin(database, args[1:])
	case "leave":
		federationLeave(database)
	case "push":
		federationPush(database)
	case "status":
		federationStatus(database)
	default:
		printFederationUsage()
		os.Exit(1)
	}
}

// --- Hub-side: invite ---

func federationInvite(database *sql.DB, args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: watcher federation invite <name>")
		os.Exit(1)
	}
	name := args[0]

	// Guard: a remote cannot act as a hub
	var federated int
	database.QueryRow("SELECT COUNT(*) FROM federation WHERE id = 1").Scan(&federated)
	if federated > 0 {
		fmt.Fprintln(os.Stderr, "Error: this instance is already federated as a remote")
		fmt.Fprintln(os.Stderr, "A watcher cannot be both a hub and a remote. Run 'watcher federation leave' first.")
		os.Exit(1)
	}

	// Create watcher record (url/token empty — will be filled by first sync)
	watcherID, _ := id.New()
	_, err := database.Exec(
		"INSERT INTO watchers (id, name, url, token, headers) VALUES (?, ?, '', '', '{}')",
		watcherID, name,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating watcher: %v\n", err)
		os.Exit(1)
	}

	// Create role: watcher:<name> with launch+poll on all (for proxy operations)
	roleName := "watcher:" + name
	roleID, _ := id.New()
	_, err = database.Exec("INSERT INTO roles (id, name) VALUES (?, ?)", roleID, roleName)
	if err != nil {
		database.Exec("DELETE FROM watchers WHERE id = ?", watcherID)
		fmt.Fprintf(os.Stderr, "Error creating role: %v\n", err)
		os.Exit(1)
	}
	// Grant all permissions — the watcher needs to call /federation/sync
	database.Exec(
		"INSERT INTO role_permissions (role_id, script_id, action) VALUES (?, '*', '*')",
		roleID,
	)

	// Create token
	tokenID, _ := id.New()
	tokenValue, _ := id.New()
	_, err = database.Exec(
		"INSERT INTO tokens (id, token, label, role_id) VALUES (?, ?, ?, ?)",
		tokenID, tokenValue, roleName, roleID,
	)
	if err != nil {
		database.Exec("DELETE FROM role_permissions WHERE role_id = ?", roleID)
		database.Exec("DELETE FROM roles WHERE id = ?", roleID)
		database.Exec("DELETE FROM watchers WHERE id = ?", watcherID)
		fmt.Fprintf(os.Stderr, "Error creating token: %v\n", err)
		os.Exit(1)
	}

	// Link watcher to its token
	database.Exec("UPDATE watchers SET token_id = ? WHERE id = ?", tokenID, watcherID)

	fmt.Printf("Invited watcher %q\n\n", name)
	fmt.Printf("  Hub token: %s\n\n", tokenValue)
	fmt.Printf("On the remote watcher, run:\n")
	fmt.Printf("  watcher federation join <hub-url> %s --url <remote-url>\n", tokenValue)
}

// --- Remote-side: join ---

func federationJoin(database *sql.DB, args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: watcher federation join <hub-url> <hub-token> --url <remote-url> [--token <remote-token>] [--header K:V]...")
		os.Exit(1)
	}

	// Guard: a hub cannot join another federation as a remote
	var hubWatchers int
	database.QueryRow("SELECT COUNT(*) FROM watchers WHERE token_id IS NOT NULL").Scan(&hubWatchers)
	if hubWatchers > 0 {
		fmt.Fprintln(os.Stderr, "Error: this instance is already acting as a hub with invited watchers")
		fmt.Fprintln(os.Stderr, "A watcher cannot be both a hub and a remote.")
		os.Exit(1)
	}

	hubURL := strings.TrimRight(args[0], "/")
	hubToken := args[1]

	var remoteURL, remoteToken string
	hubHeaders := make(map[string]string)
	for i := 2; i < len(args); i++ {
		switch args[i] {
		case "--url":
			if i+1 < len(args) {
				remoteURL = args[i+1]
				i++
			}
		case "--token":
			if i+1 < len(args) {
				remoteToken = args[i+1]
				i++
			}
		case "--header":
			if i+1 < len(args) {
				parts := strings.SplitN(args[i+1], ":", 2)
				if len(parts) == 2 {
					hubHeaders[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
				}
				i++
			}
		}
	}

	if remoteURL == "" {
		fmt.Fprintln(os.Stderr, "Error: --url is required")
		os.Exit(1)
	}

	// Check hub health
	hc := &HubClient{
		URL:     hubURL,
		Token:   hubToken,
		Headers: hubHeaders,
	}
	if err := hc.Health(); err != nil {
		fmt.Fprintf(os.Stderr, "Cannot reach hub: %v\n", err)
		os.Exit(1)
	}

	// Auto-generate a remote admin token if not provided
	if remoteToken == "" {
		// Look for existing admin token
		err := database.QueryRow(`
			SELECT t.token FROM tokens t
			JOIN roles r ON t.role_id = r.id
			JOIN role_permissions rp ON rp.role_id = r.id
			WHERE t.revoked = 0 AND rp.script_id = '*' AND rp.action = '*'
			LIMIT 1
		`).Scan(&remoteToken)
		if err != nil {
			// Create one
			var adminRoleID string
			err = database.QueryRow("SELECT id FROM roles WHERE name = 'admin'").Scan(&adminRoleID)
			if err != nil {
				fmt.Fprintln(os.Stderr, "Error: no admin role found and --token not provided")
				os.Exit(1)
			}
			tokenID, _ := id.New()
			remoteToken, _ = id.New()
			database.Exec(
				"INSERT INTO tokens (id, token, label, role_id) VALUES (?, ?, 'federation-proxy', ?)",
				tokenID, remoteToken, adminRoleID,
			)
			fmt.Printf("Auto-generated remote proxy token: %s\n", remoteToken)
		}
	}

	// Store federation config (single-row table)
	hubHeadersJSON, _ := json.Marshal(hubHeaders)
	_, err := database.Exec(`
		INSERT INTO federation (id, hub_url, hub_token, hub_headers, remote_url, remote_token)
		VALUES (1, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			hub_url = excluded.hub_url,
			hub_token = excluded.hub_token,
			hub_headers = excluded.hub_headers,
			remote_url = excluded.remote_url,
			remote_token = excluded.remote_token
	`, hubURL, hubToken, string(hubHeadersJSON), remoteURL, remoteToken)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error saving federation config: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Joined hub at %s\n", hubURL)

	// Initial push
	if err := pushToHub(database); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: initial push failed: %v\n", err)
	}
}

// --- Remote-side: leave ---

func federationLeave(database *sql.DB) {
	res, err := database.Exec("DELETE FROM federation WHERE id = 1")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		fmt.Fprintln(os.Stderr, "Not federated")
		os.Exit(1)
	}
	fmt.Println("Left federation. Scripts will no longer be pushed to the hub.")
}

// --- Remote-side: push ---

func federationPush(database *sql.DB) {
	if err := pushToHub(database); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// --- Remote-side: status ---

func federationStatus(database *sql.DB) {
	var hubURL, hubToken, remoteURL string
	err := database.QueryRow(
		"SELECT hub_url, hub_token, remote_url FROM federation WHERE id = 1",
	).Scan(&hubURL, &hubToken, &remoteURL)
	if err != nil {
		fmt.Println("Not federated")
		return
	}

	fmt.Printf("Federation status:\n")
	fmt.Printf("  Hub URL:    %s\n", hubURL)
	fmt.Printf("  Hub token:  %s...%s\n", hubToken[:4], hubToken[len(hubToken)-4:])
	fmt.Printf("  Remote URL: %s\n", remoteURL)

	// Count local scripts that would be pushed
	var count int
	database.QueryRow("SELECT COUNT(*) FROM scripts WHERE watcher_id IS NULL").Scan(&count)
	fmt.Printf("  Local scripts: %d\n", count)
}

// pushToHub loads the federation config and pushes local scripts to the hub.
// Returns nil if not federated (no-op). Exported for use by other commands.
func pushToHub(database *sql.DB) error {
	var hubURL, hubToken, hubHeadersJSON, remoteURL, remoteToken string
	err := database.QueryRow(
		"SELECT hub_url, hub_token, hub_headers, remote_url, remote_token FROM federation WHERE id = 1",
	).Scan(&hubURL, &hubToken, &hubHeadersJSON, &remoteURL, &remoteToken)
	if err != nil {
		return nil // not federated
	}

	// Load local scripts (not proxy scripts)
	rows, err := database.Query(
		"SELECT id, name, enabled FROM scripts WHERE watcher_id IS NULL",
	)
	if err != nil {
		return fmt.Errorf("querying scripts: %w", err)
	}
	defer rows.Close()

	type scriptInfo struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	var scripts []scriptInfo
	for rows.Next() {
		var s scriptInfo
		var enabled int
		rows.Scan(&s.ID, &s.Name, &enabled)
		s.Enabled = enabled == 1
		scripts = append(scripts, s)
	}

	var hubHeaders map[string]string
	json.Unmarshal([]byte(hubHeadersJSON), &hubHeaders)

	hc := &HubClient{
		URL:     hubURL,
		Token:   hubToken,
		Headers: hubHeaders,
	}

	resp, err := hc.Sync(remoteURL, remoteToken, scripts)
	if err != nil {
		return err
	}
	fmt.Printf("Pushed to hub: created=%d updated=%d disabled=%d unchanged=%d\n",
		resp.Created, resp.Updated, resp.Disabled, resp.Unchanged)
	return nil
}

// PushToHub is the exported version for use by cmd/script.go and cmd/setup.go.
// It logs a warning on failure but never returns an error.
func PushToHub(database *sql.DB) {
	if err := pushToHub(database); err != nil {
		log.Printf("Warning: federation push failed: %v", err)
	}
}

// --- HubClient ---

// HubClient communicates with the hub from the remote side.
type HubClient struct {
	URL     string
	Token   string
	Headers map[string]string
}

func (hc *HubClient) setHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+hc.Token)
	for k, v := range hc.Headers {
		req.Header.Set(k, v)
	}
}

// Health checks connectivity to the hub via GET /health.
func (hc *HubClient) Health() error {
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("GET", hc.URL+"/health", nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	hc.setHeaders(req)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("health check returned HTTP %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// syncPayload is the request body for POST /federation/sync.
type syncPayload struct {
	URL     string            `json:"url"`
	Token   string            `json:"token"`
	Headers map[string]string `json:"headers,omitempty"`
	Scripts any               `json:"scripts"`
}

// syncResult is the response from POST /federation/sync.
type syncResult struct {
	Watcher   string `json:"watcher"`
	Created   int    `json:"created"`
	Updated   int    `json:"updated"`
	Disabled  int    `json:"disabled"`
	Unchanged int    `json:"unchanged"`
}

// Sync pushes the local script state to the hub.
func (hc *HubClient) Sync(remoteURL, remoteToken string, scripts any) (*syncResult, error) {
	payload := syncPayload{
		URL:     remoteURL,
		Token:   remoteToken,
		Scripts: scripts,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encoding request: %w", err)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest("POST", hc.URL+"/federation/sync", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	hc.setHeaders(req)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hub returned HTTP %d: %s", resp.StatusCode, string(body))
	}

	var result syncResult
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	return &result, nil
}

func printFederationUsage() {
	fmt.Fprintln(os.Stderr, `Usage: watcher federation <command>

Hub commands:
  invite <name>                                  Create watcher + token, print hub token

Remote commands:
  join <hub-url> <token> --url <url> [options]   Register with hub, push scripts
  leave                                          Stop pushing to hub
  push                                           Manual push (retry after downtime)
  status                                         Show federation info

Join options:
  --url <url>              Remote watcher URL (required)
  --token <token>          Remote admin token for hub to proxy through
  --header <Key:Value>     Extra header for hub requests (repeatable)`)
}
