package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// WatcherClient is an HTTP client for communicating with a remote watcher instance.
type WatcherClient struct {
	URL     string
	Token   string
	Headers map[string]string
	Client  *http.Client
}

// NewWatcherClient creates a WatcherClient from DB fields.
func NewWatcherClient(url, token, headersJSON string) *WatcherClient {
	wc := &WatcherClient{
		URL:   strings.TrimRight(url, "/"),
		Token: token,
		Client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
	if headersJSON != "" && headersJSON != "{}" {
		json.Unmarshal([]byte(headersJSON), &wc.Headers)
	}
	return wc
}

func (wc *WatcherClient) setHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+wc.Token)
	for k, v := range wc.Headers {
		req.Header.Set(k, v)
	}
}

// Launch proxies a launch request to the remote watcher.
// Returns the raw JSON response body and HTTP status code.
func (wc *WatcherClient) Launch(scriptID string, args []string) ([]byte, int, error) {
	body := map[string]any{"script_id": scriptID}
	if len(args) > 0 {
		body["args"] = args
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, 0, fmt.Errorf("encoding request: %w", err)
	}

	req, err := http.NewRequest("POST", wc.URL+"/launch", bytes.NewReader(data))
	if err != nil {
		return nil, 0, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	wc.setHeaders(req)

	resp, err := wc.Client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("reading response: %w", err)
	}
	return respBody, resp.StatusCode, nil
}

// Poll proxies a poll request to the remote watcher.
// Returns the raw JSON response body and HTTP status code.
func (wc *WatcherClient) Poll(scriptID, runID string) ([]byte, int, error) {
	url := fmt.Sprintf("%s/poll?script_id=%s&run_id=%s", wc.URL, scriptID, runID)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("creating request: %w", err)
	}
	wc.setHeaders(req)

	resp, err := wc.Client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("reading response: %w", err)
	}
	return respBody, resp.StatusCode, nil
}

// FetchAPI fetches a path from the remote watcher and returns the raw response body and status.
func (wc *WatcherClient) FetchAPI(path string) ([]byte, int, error) {
	req, err := http.NewRequest("GET", wc.URL+path, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("creating request: %w", err)
	}
	wc.setHeaders(req)

	resp, err := wc.Client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("reading response: %w", err)
	}
	return respBody, resp.StatusCode, nil
}

// Health checks connectivity to the remote watcher via GET /health.
func (wc *WatcherClient) Health() error {
	req, err := http.NewRequest("GET", wc.URL+"/health", nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	wc.setHeaders(req)

	resp, err := wc.Client.Do(req)
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
