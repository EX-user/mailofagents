package worker

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// setEnv returns the map with k=v set (nil-safe; the Config.Env map is
// shared across wakes, but every write is idempotent so that is fine).
func setEnv(m map[string]string, k, v string) map[string]string {
	if m == nil {
		m = map[string]string{}
	}
	m[k] = v
	return m
}

// mergeOpencodeProvider writes the Vendor block into the user's
// opencode.json (provider definition is file-level in opencode — there is
// no CLI flag for it). Existing content is preserved: we unmarshal into a
// generic map, set only provider.<name>, and write back indented.
//
// Model metadata note: opencode accepts a minimal {"name": ...} skeleton for
// custom models; providers serving unusual limits can be hand-tuned in the
// same file afterwards (our merge never removes hand-tuned fields).
func mergeOpencodeProvider(cfg *Config) error {
	v := cfg.Vendor
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := filepath.Join(home, ".config", "opencode", "opencode.json")

	root := map[string]any{
		"$schema": "https://opencode.ai/config.json",
	}
	if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
		if err := json.Unmarshal(b, &root); err != nil {
			return fmt.Errorf("parse existing %s: %w", path, err)
		}
	}

	providers, _ := root["provider"].(map[string]any)
	if providers == nil {
		providers = map[string]any{}
	}
	def, _ := providers[v.Name].(map[string]any)
	if def == nil {
		def = map[string]any{}
	}
	if v.NPM != "" {
		def["npm"] = v.NPM
	} else if def["npm"] == nil || def["npm"] == "" {
		def["npm"] = "@ai-sdk/anthropic" // anthropic-compatible endpoints are the common coding-plan shape
	}
	opts, _ := def["options"].(map[string]any)
	if opts == nil {
		opts = map[string]any{}
	}
	if v.BaseURL != "" {
		opts["baseURL"] = v.BaseURL
	}
	if v.APIKey != "" {
		opts["apiKey"] = v.APIKey
	}
	def["options"] = opts
	if v.Model != "" {
		models, _ := def["models"].(map[string]any)
		if models == nil {
			models = map[string]any{}
		}
		if _, ok := models[v.Model]; !ok {
			models[v.Model] = map[string]any{"name": v.Model}
		}
		def["models"] = models
	}
	providers[v.Name] = def
	root["provider"] = providers

	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".worker-tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
