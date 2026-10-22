package rime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gaboolic/moqi-ime/imecore"
)

func TestParseAIConfigJSON(t *testing.T) {
	cfg, err := parseAIConfigJSON([]byte(`{
		"api": {
			"base_url": " https://example.test/v1/ ",
			"api_key": " secret ",
			"model": " demo-model "
		},
		"actions": [
			{
				"name": "写好评",
				"hotkey": "Ctrl+Alt+R",
				"prompt": "请围绕“{{composition}}”生成 2 条短评。"
			}
		]
	}`))
	if err != nil {
		t.Fatalf("expected config to parse, got %v", err)
	}
	if cfg.API.BaseURL != "https://example.test/v1" {
		t.Fatalf("unexpected base URL: %q", cfg.API.BaseURL)
	}
	if cfg.API.APIKey != "secret" {
		t.Fatalf("unexpected API key: %q", cfg.API.APIKey)
	}
	if cfg.API.Model != "demo-model" {
		t.Fatalf("unexpected model: %q", cfg.API.Model)
	}
	if len(cfg.Actions) != 1 {
		t.Fatalf("expected 1 action, got %#v", cfg.Actions)
	}
	if cfg.Actions[0].Hotkey != "Ctrl+Alt+R" {
		t.Fatalf("unexpected normalized hotkey: %q", cfg.Actions[0].Hotkey)
	}
	if cfg.Actions[0].KeyCode != int('R') || !cfg.Actions[0].Ctrl || !cfg.Actions[0].Alt || cfg.Actions[0].Shift {
		t.Fatalf("unexpected hotkey mapping: %#v", cfg.Actions[0])
	}
}

func TestParseAIConfigJSONFallsBackToEnvForEmptyAPIFields(t *testing.T) {
	t.Setenv("MOQI_AI_BASE_URL", "https://env.example.test/v1/")
	t.Setenv("MOQI_AI_API_KEY", "env-secret")
	t.Setenv("MOQI_AI_MODEL", "env-model")

	cfg, err := parseAIConfigJSON([]byte(`{
		"api": {
			"base_url": "",
			"api_key": "",
			"model": ""
		},
		"actions": [
			{
				"name": "写好评",
				"hotkey": "Ctrl+Shift+G",
				"prompt": "请围绕“{{composition}}”生成 2 条短评。"
			}
		]
	}`))
	if err != nil {
		t.Fatalf("expected empty API fields to fall back to env, got %v", err)
	}
	if cfg.API.BaseURL != "https://env.example.test/v1" {
		t.Fatalf("unexpected fallback base URL: %q", cfg.API.BaseURL)
	}
	if cfg.API.APIKey != "env-secret" {
		t.Fatalf("unexpected fallback API key: %q", cfg.API.APIKey)
	}
	if cfg.API.Model != "env-model" {
		t.Fatalf("unexpected fallback model: %q", cfg.API.Model)
	}
}

func TestAIActionMatchesConfiguredHotkey(t *testing.T) {
	action, err := parseAIHotkey("Ctrl+Alt+R")
	if err != nil {
		t.Fatalf("expected hotkey to parse, got %v", err)
	}
	if !action.matches(&imecore.Request{
		KeyCode:   int('R'),
		KeyStates: keyStatesWithDown(vkControl, vkMenu),
	}) {
		t.Fatalf("expected configured hotkey to match request")
	}
	if action.matches(&imecore.Request{
		KeyCode:   int('R'),
		KeyStates: keyStatesWithDown(vkControl, vkShift),
	}) {
		t.Fatalf("expected mismatched modifiers to fail")
	}
}

func TestCopyAIConfigIfMissingCopiesBundledConfig(t *testing.T) {
	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "bundle", aiConfigFileName)
	dstPath := filepath.Join(tmpDir, "user", aiConfigFileName)

	if err := os.MkdirAll(filepath.Dir(srcPath), 0o755); err != nil {
		t.Fatalf("mkdir src dir: %v", err)
	}
	want := []byte("{\"api\":{\"base_url\":\"https://example.test/v1\"}}")
	if err := os.WriteFile(srcPath, want, 0o644); err != nil {
		t.Fatalf("write src config: %v", err)
	}

	if err := copyAIConfigIfMissing(dstPath, srcPath); err != nil {
		t.Fatalf("expected config copy to succeed, got %v", err)
	}

	got, err := os.ReadFile(dstPath)
	if err != nil {
		t.Fatalf("read copied config: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("unexpected copied content: %q", got)
	}
}

func TestCopyAIConfigIfMissingDoesNotOverwriteExistingFile(t *testing.T) {
	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "bundle", aiConfigFileName)
	dstPath := filepath.Join(tmpDir, "user", aiConfigFileName)

	if err := os.MkdirAll(filepath.Dir(srcPath), 0o755); err != nil {
		t.Fatalf("mkdir src dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		t.Fatalf("mkdir dst dir: %v", err)
	}
	if err := os.WriteFile(srcPath, []byte("bundled"), 0o644); err != nil {
		t.Fatalf("write src config: %v", err)
	}
	if err := os.WriteFile(dstPath, []byte("user-customized"), 0o644); err != nil {
		t.Fatalf("write dst config: %v", err)
	}

	if err := copyAIConfigIfMissing(dstPath, srcPath); err != nil {
		t.Fatalf("expected existing user config to be preserved, got %v", err)
	}

	got, err := os.ReadFile(dstPath)
	if err != nil {
		t.Fatalf("read existing config: %v", err)
	}
	if string(got) != "user-customized" {
		t.Fatalf("expected existing config to stay unchanged, got %q", got)
	}
}

func TestSyncAIConfigFromBundledMergesMissingActionsAndKeepsUserKey(t *testing.T) {
	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "bundle", aiConfigFileName)
	dstPath := filepath.Join(tmpDir, "user", aiConfigFileName)
	if err := os.MkdirAll(filepath.Dir(srcPath), 0o755); err != nil {
		t.Fatalf("mkdir src dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		t.Fatalf("mkdir dst dir: %v", err)
	}
	bundled := `{"api":{"base_url":"https://bundled.test","api_key":"bundled-key","model":"bundled-model"},
"actions":[{"name":"写好评","hotkey":"Ctrl+Shift+G","prompt":"bundled prompt"},
{"name":"润色","hotkey":"Ctrl+Shift+R","prompt":"bundled polish"}]}`
	user := `{"api":{"base_url":"https://user.test","api_key":"user-key","model":""},
"actions":[{"name":"写好评","hotkey":"Ctrl+Shift+G","prompt":"user prompt"}]}`
	if err := os.WriteFile(srcPath, []byte(bundled), 0o644); err != nil {
		t.Fatalf("write src config: %v", err)
	}
	if err := os.WriteFile(dstPath, []byte(user), 0o644); err != nil {
		t.Fatalf("write user config: %v", err)
	}

	if err := syncAIConfigFromBundled(dstPath, srcPath); err != nil {
		t.Fatalf("expected merge to succeed, got %v", err)
	}

	data, err := os.ReadFile(dstPath)
	if err != nil {
		t.Fatalf("read merged config: %v", err)
	}
	var merged aiFileConfig
	if err := json.Unmarshal(data, &merged); err != nil {
		t.Fatalf("decode merged config: %v", err)
	}
	if merged.API.APIKey != "user-key" || merged.API.BaseURL != "https://user.test" {
		t.Fatalf("user api must win, got %#v", merged.API)
	}
	if merged.API.Model != "bundled-model" {
		t.Fatalf("expected empty model to be filled from bundle, got %q", merged.API.Model)
	}
	if len(merged.Actions) != 2 {
		t.Fatalf("expected missing action to be appended, got %#v", merged.Actions)
	}
	if merged.Actions[0].Prompt != "user prompt" {
		t.Fatalf("same-name action must not be overwritten, got %q", merged.Actions[0].Prompt)
	}
	if merged.Actions[1].Name != "润色" || merged.Actions[1].Prompt != "bundled polish" {
		t.Fatalf("expected bundled polish action appended, got %#v", merged.Actions[1])
	}
}

func TestSyncAIConfigFromBundledLeavesCompleteConfigUntouched(t *testing.T) {
	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "bundle", aiConfigFileName)
	dstPath := filepath.Join(tmpDir, "user", aiConfigFileName)
	if err := os.MkdirAll(filepath.Dir(srcPath), 0o755); err != nil {
		t.Fatalf("mkdir src dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		t.Fatalf("mkdir dst dir: %v", err)
	}
	bundled := `{"api":{"base_url":"https://bundled.test","api_key":"bundled-key","model":"bundled-model"},
"actions":[{"name":"写好评","hotkey":"Ctrl+Shift+G","prompt":"bundled prompt"}]}`
	user := `{"api":{"base_url":"https://user.test","api_key":"user-key","model":"user-model"},
"actions":[{"name":"写好评","hotkey":"Ctrl+Shift+G","prompt":"user prompt"}]}`
	if err := os.WriteFile(srcPath, []byte(bundled), 0o644); err != nil {
		t.Fatalf("write src config: %v", err)
	}
	if err := os.WriteFile(dstPath, []byte(user), 0o644); err != nil {
		t.Fatalf("write user config: %v", err)
	}

	if err := syncAIConfigFromBundled(dstPath, srcPath); err != nil {
		t.Fatalf("expected merge to succeed, got %v", err)
	}

	got, err := os.ReadFile(dstPath)
	if err != nil {
		t.Fatalf("read user config: %v", err)
	}
	if string(got) != user {
		t.Fatalf("complete user config must stay byte-identical, got %q", got)
	}
}

func TestBundledAIConfigDefaultsToAIDisabled(t *testing.T) {
	data, err := os.ReadFile(aiConfigFileName)
	if err != nil {
		t.Fatalf("read bundled ai_config.json: %v", err)
	}
	_, err = parseAIConfigJSON(data)
	if err == nil {
		t.Fatal("bundled config must keep AI disabled until the user opts in")
	}
	if !strings.Contains(err.Error(), "删成「base_url」") {
		t.Fatalf("expected opt-in guidance in error, got %v", err)
	}
}

func TestSyncAIConfigFromBundledPreservesUnknownAPIKeys(t *testing.T) {
	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "bundle", aiConfigFileName)
	dstPath := filepath.Join(tmpDir, "user", aiConfigFileName)
	if err := os.MkdirAll(filepath.Dir(srcPath), 0o755); err != nil {
		t.Fatalf("mkdir src dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		t.Fatalf("mkdir dst dir: %v", err)
	}
	bundled := `{"api":{"base_url删掉我即可用AI功能":"https://bundled.test","api_key":"bundled-key","model":"bundled-model"},
"actions":[{"name":"写好评","hotkey":"Ctrl+Shift+G","prompt":"bundled prompt"}]}`
	user := `{"api":{"base_url删掉我即可用AI功能":"https://bundled.test","api_key":"bundled-key","model":"bundled-model"},
"actions":[{"name":"写好评","hotkey":"Ctrl+Shift+G","prompt":"user prompt"}]}`
	if err := os.WriteFile(srcPath, []byte(bundled), 0o644); err != nil {
		t.Fatalf("write src config: %v", err)
	}
	if err := os.WriteFile(dstPath, []byte(user), 0o644); err != nil {
		t.Fatalf("write user config: %v", err)
	}

	if err := syncAIConfigFromBundled(dstPath, srcPath); err != nil {
		t.Fatalf("expected merge to succeed, got %v", err)
	}

	got, err := os.ReadFile(dstPath)
	if err != nil {
		t.Fatalf("read user config: %v", err)
	}
	if string(got) != user {
		t.Fatalf("opt-in key must survive merge and untouched config must stay identical, got %q", got)
	}
}
