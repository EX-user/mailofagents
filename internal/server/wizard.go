package server

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/agentmail/agentmail/internal/audit"
	"github.com/agentmail/agentmail/internal/config"
	"github.com/agentmail/agentmail/internal/store"
)

// WizardPort is the fixed port the setup wizard listens on. It is separate
// from the real server's listen address (which may not be known yet — the
// wizard collects it).
const WizardPort = "127.0.0.1:8848"

// WizardResult holds the outcome of a successful wizard run.
type WizardResult struct {
	Store    *store.Store
	Audit    *audit.Store
	Listen   string
	Domain   string
	DBPath   string
}

// RunWizard starts the setup wizard HTTP server on WizardPort, waits for the
// user to complete setup and click "launch", then returns. The returned store
// is already opened and bootstrapped (the wizard's /setup handler does
// store.Open + BootstrapSystem). It blocks until /launch is called or the
// HTTP server stops.
func RunWizard(cfg *config.Config) (*WizardResult, error) {
	w := &wizard{
		cfg:     cfg,
		doneCh:  make(chan struct{}),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", w.handleIndex)
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticSubFS))))
	mux.HandleFunc("/api/wizard-defaults", w.handleDefaults)
	mux.HandleFunc("/setup", w.handleSetup)
	mux.HandleFunc("/api/bootstrap-info", w.handleBootstrapInfo)
	mux.HandleFunc("/write-mcp-config", w.handleWriteMCPConfig)
	mux.HandleFunc("/open-config-folder", w.handleOpenConfigFolder)
	mux.HandleFunc("/api/mcp-config-status", w.handleMCPConfigStatus)
	mux.HandleFunc("/launch", w.handleLaunch)

	srv := &http.Server{Addr: WizardPort, Handler: mux, ReadHeaderTimeout: 10e9}
	go func() {
		<-w.doneCh
		srv.Close()
	}()

	log.Printf("agentmail wizard: open http://%s/ in your browser", WizardPort)
	go openBrowser("http://" + WizardPort + "/")

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return nil, fmt.Errorf("wizard server: %w", err)
	}

	if w.result == nil {
		return nil, fmt.Errorf("wizard ended without completion")
	}
	return w.result, nil
}

type wizard struct {
	cfg    *config.Config
	result *WizardResult
	doneCh chan struct{}
}

func (w *wizard) handleIndex(resp http.ResponseWriter, req *http.Request) {
	if req.URL.Path != "/" {
		http.NotFound(resp, req)
		return
	}
	data, err := fs.ReadFile(staticSubFS, "wizard.html")
	if err != nil {
		// Fall back to index.html if wizard.html not yet created.
		data, err = fs.ReadFile(staticSubFS, "index.html")
		if err != nil {
			http.Error(resp, "setup page not found", http.StatusInternalServerError)
			return
		}
	}
	resp.Header().Set("Content-Type", "text/html; charset=utf-8")
	resp.Write(data)
}

// handleDefaults returns the default values for the wizard form (from config).
func (w *wizard) handleDefaults(resp http.ResponseWriter, req *http.Request) {
	writeJSON(resp, http.StatusOK, map[string]any{
		"db_path": w.cfg.Storage.DBPath,
		"listen":  w.cfg.Server.Listen,
		"domain":  w.cfg.Server.Domain,
	})
}

// handleSetup processes the wizard form: opens the db, bootstraps the system,
// and stores listen/domain. The store handle is kept for the caller.
func (w *wizard) handleSetup(resp http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(resp, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if w.result != nil {
		http.Error(resp, "already set up", http.StatusConflict)
		return
	}
	var body struct {
		DBPath   string `json:"db_path"`
		Listen   string `json:"listen"`
		Domain   string `json:"domain"`
		Password string `json:"admin_password"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		writeJSON(resp, http.StatusBadRequest, map[string]any{"error": "invalid body: " + err.Error()})
		return
	}
	body.DBPath = strings.TrimSpace(body.DBPath)
	body.Listen = strings.TrimSpace(body.Listen)
	body.Domain = strings.TrimSpace(body.Domain)
	if body.DBPath == "" || body.Listen == "" || body.Domain == "" || body.Password == "" {
		writeJSON(resp, http.StatusBadRequest, map[string]any{"error": "all fields are required"})
		return
	}
	if len(body.Password) < 8 {
		writeJSON(resp, http.StatusBadRequest, map[string]any{"error": "password must be at least 8 characters"})
		return
	}

	st, err := store.Open(body.DBPath)
	if err != nil {
		writeJSON(resp, http.StatusInternalServerError, map[string]any{"error": "open db: " + err.Error()})
		return
	}
	if st.IsInitialized() {
		st.Close()
		writeJSON(resp, http.StatusConflict, map[string]any{"error": "database is already initialized"})
		return
	}
	if err := st.BootstrapSystem("admin", body.Password, body.Domain); err != nil {
		st.Close()
		writeJSON(resp, http.StatusInternalServerError, map[string]any{"error": "bootstrap: " + err.Error()})
		return
	}
	if err := st.SetListen(body.Listen); err != nil {
		writeJSON(resp, http.StatusInternalServerError, map[string]any{"error": "set listen: " + err.Error()})
		return
	}
	auditStore, err := audit.New(st.DB())
	if err != nil {
		st.Close()
		writeJSON(resp, http.StatusInternalServerError, map[string]any{"error": "init audit: " + err.Error()})
		return
	}
	w.result = &WizardResult{
		Store:  st,
		Audit:  auditStore,
		Listen: body.Listen,
		Domain: body.Domain,
		DBPath: body.DBPath,
	}
	writeJSON(resp, http.StatusOK, map[string]any{
		"admin_address": "admin@" + body.Domain,
		"listen":        body.Listen,
		"domain":        body.Domain,
	})
}

// handleBootstrapInfo returns the collected config for MCP snippet generation.
func (w *wizard) handleBootstrapInfo(resp http.ResponseWriter, req *http.Request) {
	if w.result == nil {
		http.Error(resp, "not yet bootstrapped", http.StatusServiceUnavailable)
		return
	}
	gwPath := gatewayPath()
	writeJSON(resp, http.StatusOK, map[string]any{
		"listen":       w.result.Listen,
		"domain":       w.result.Domain,
		"gateway_path": gwPath,
		"server_url":   "http://" + w.result.Listen,
	})
}

// handleWriteMCPConfig creates a NEW config file only if it does not already
// exist. If the file exists, it returns 409 — we never overwrite an existing
// config (the user must merge manually to avoid losing other MCP servers).
//
//	POST /write-mcp-config {"client": "codex"|"zcode"|"opencode"}
func (w *wizard) handleWriteMCPConfig(resp http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(resp, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if w.result == nil {
		http.Error(resp, "not yet bootstrapped", http.StatusServiceUnavailable)
		return
	}
	var body struct{ Client string `json:"client"` }
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		writeJSON(resp, http.StatusBadRequest, map[string]any{"error": "invalid body"})
		return
	}
	info, err := mcpClientInfo(body.Client, gatewayPath(), w.result.Listen)
	if err != nil {
		writeJSON(resp, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	// Create parent directory if needed.
	if err := os.MkdirAll(filepath.Dir(info.path), 0o755); err != nil {
		writeJSON(resp, http.StatusInternalServerError, map[string]any{"error": "create dir: " + err.Error()})
		return
	}
	// Refuse to overwrite an existing file.
	if fileExists(info.path) {
		writeJSON(resp, http.StatusConflict, map[string]any{
			"error":  "file already exists — copy the snippet and merge manually to avoid losing existing config",
			"path":   info.path,
			"exists": true,
		})
		return
	}
	if err := os.WriteFile(info.path, []byte(info.content), 0o600); err != nil {
		writeJSON(resp, http.StatusInternalServerError, map[string]any{"error": "write: " + err.Error()})
		return
	}
	writeJSON(resp, http.StatusOK, map[string]any{
		"client":  body.Client,
		"path":    info.path,
		"written": true,
	})
}

// handleOpenConfigFolder opens the directory containing the config file in
// the system file manager. If the directory doesn't exist, returns 404.
//
//	POST /open-config-folder {"client": "codex"}
func (w *wizard) handleOpenConfigFolder(resp http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(resp, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct{ Client string `json:"client"` }
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		writeJSON(resp, http.StatusBadRequest, map[string]any{"error": "invalid body"})
		return
	}
	info, err := mcpClientInfo(body.Client, gatewayPath(), "")
	if err != nil {
		writeJSON(resp, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	dir := filepath.Dir(info.path)
	if !fileExists(dir) {
		writeJSON(resp, http.StatusNotFound, map[string]any{
			"error":   "directory not found — the client may not be installed",
			"path":    info.path,
			"dir":     dir,
			"missing": "dir",
		})
		return
	}
	openFolder(dir, info.path)
	writeJSON(resp, http.StatusOK, map[string]any{"opened": true, "path": info.path})
}

// handleMCPConfigStatus checks for all known clients whether their config
// file and/or directory exists. Used by the wizard to show honest status
// (installed vs not found) before the user clicks anything.
//
//	GET /api/mcp-config-status
func (w *wizard) handleMCPConfigStatus(resp http.ResponseWriter, req *http.Request) {
	gwPath := gatewayPath()
	clients := []string{"codex", "zcode", "opencode", "claude"}
	status := map[string]any{}
	for _, c := range clients {
		info, err := mcpClientInfo(c, gwPath, "")
		if err != nil {
			status[c] = map[string]any{"error": err.Error()}
			continue
		}
		status[c] = map[string]any{
			"path":        info.path,
			"file_exists": fileExists(info.path),
			"dir_exists":  fileExists(filepath.Dir(info.path)),
		}
	}
	writeJSON(resp, http.StatusOK, status)
}

// mcpClientInfo describes one agent client's config file location and the
// snippet to put in it.
type mcpClientInfoStruct struct {
	path    string
	content string
	kind    string // "file" (config file) or "command" (shell command, e.g. claude)
}

func mcpClientInfo(client, gwPath, serverURL string) (*mcpClientInfoStruct, error) {
	switch client {
	case "codex":
		home, _ := os.UserHomeDir()
		path := filepath.Join(home, ".codex", "config.toml")
		content := ""
		if serverURL != "" {
			content = fmt.Sprintf(`[mcp_servers.agentmail]
command = "%s"
args = ["--server-url", "%s"]
`, escapeBackslash(gwPath), serverURL)
		}
		return &mcpClientInfoStruct{path: path, content: content, kind: "file"}, nil
	case "zcode":
		home, _ := os.UserHomeDir()
		path := filepath.Join(home, ".zcode", "cli", "config.json")
		content := ""
		if serverURL != "" {
			content = fmt.Sprintf(`{
  "mcp": {
    "servers": {
      "agentmail": {
        "type": "stdio",
        "command": "%s",
        "args": ["--server-url", "%s"],
        "enabled": true
      }
    }
  }
}
`, escapeBackslash(gwPath), serverURL)
		}
		return &mcpClientInfoStruct{path: path, content: content, kind: "file"}, nil
	case "opencode":
		path := "opencode.json"
		content := ""
		if serverURL != "" {
			content = fmt.Sprintf(`{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "agentmail": {
      "type": "local",
      "command": ["%s", "--server-url", "%s"],
      "enabled": true
    }
  }
}
`, gwPath, serverURL)
		}
		return &mcpClientInfoStruct{path: path, content: content, kind: "file"}, nil
	case "claude":
		cmd := ""
		if serverURL != "" {
			cmd = fmt.Sprintf("claude mcp add agentmail -- %s --server-url %s", gwPath, serverURL)
		}
		return &mcpClientInfoStruct{path: "", content: cmd, kind: "command"}, nil
	default:
		return nil, fmt.Errorf("unknown client: %s", client)
	}
}

// fileExists reports whether the given path exists (file or directory).
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// openFolder opens the system file manager at dir, selecting targetFile if
// it exists.
func openFolder(dir, targetFile string) {
	switch runtime.GOOS {
	case "windows":
		// Use explorer.exe directly. Start() returns immediately; explorer
		// manages its own window. We pass the directory as a single argument.
		// (explorer /select,/path/to/file opens and selects, but doesn't
		// foreground reliably; opening the directory directly is more
		// predictable.)
		c := exec.Command("explorer.exe", dir)
		c.SysProcAttr = nil
		_ = c.Start()
	case "darwin":
		_ = exec.Command("open", dir).Start()
	default:
		_ = exec.Command("xdg-open", dir).Start()
	}
}

// handleLaunch signals the wizard to shut down so main() can start the real server.
func (w *wizard) handleLaunch(resp http.ResponseWriter, req *http.Request) {
	if w.result == nil {
		http.Error(resp, "not yet bootstrapped", http.StatusServiceUnavailable)
		return
	}
	writeJSON(resp, http.StatusOK, map[string]any{"status": "launching"})
	go func() {
		// Small delay so the response is sent before the server closes.
		close(w.doneCh)
	}()
}

// gatewayPath returns the path to the agentmail-gateway binary next to the
// server executable.
func gatewayPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "agentmail-gateway"
	}
	dir := filepath.Dir(exe)
	name := "agentmail-gateway"
	if runtime.GOOS == "windows" {
		name = "agentmail-gateway.exe"
	}
	return filepath.Join(dir, name)
}

func escapeBackslash(s string) string {
	return strings.ReplaceAll(s, `\`, `\\`)
}

// openBrowser tries to open the URL in the default browser. Best-effort.
func openBrowser(url string) {
	switch runtime.GOOS {
	case "windows":
		_ = exec.Command("cmd", "/c", "start", "", url).Start()
	case "darwin":
		_ = exec.Command("open", url).Start()
	default:
		_ = exec.Command("xdg-open", url).Start()
	}
}
