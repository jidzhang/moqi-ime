package rime

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func resetDebugLogState(t *testing.T) {
	t.Helper()
	debugLogState.mu.Lock()
	debugLogState.lastRefresh = time.Time{}
	debugLogState.level = debugLogLevelOff
	debugLogState.usageLog = false
	debugLogState.override = nil
	debugLogState.mu.Unlock()
}

func TestUsageLoggingRequiresExplicitSwitch(t *testing.T) {
	localAppData := t.TempDir()
	t.Setenv("LOCALAPPDATA", localAppData)
	configDir := filepath.Join(localAppData, "MoqiIM")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	configPath := filepath.Join(configDir, "MoqiLauncher.json")

	cases := []struct {
		name     string
		config   string
		wantLogs bool
	}{
		{"no config", "", false},
		{"debug only", `{"logLevel":"debug"}`, false},
		{"trace only", `{"logLevel":"trace"}`, false},
		{"trace with usageLog", `{"logLevel":"trace","usageLog":true}`, true},
		{"usageLog without debug", `{"logLevel":"error","usageLog":true}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.config == "" {
				_ = os.Remove(configPath)
			} else if err := os.WriteFile(configPath, []byte(tc.config), 0o644); err != nil {
				t.Fatalf("write config: %v", err)
			}
			resetDebugLogState(t)
			if got := isUsageLoggingEnabled(); got != tc.wantLogs {
				t.Fatalf("expected usage logging %v for config %q, got %v", tc.wantLogs, tc.config, got)
			}
		})
	}
}
