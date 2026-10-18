package rime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gaboolic/moqi-ime/imecore"
)

func writeNetworkEgressConfig(t *testing.T, body string) {
	t.Helper()
	appData := os.Getenv("APPDATA")
	if appData == "" {
		t.Skip("APPDATA is not set")
	}
	root := filepath.Join(appData, APP)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	path := filepath.Join(root, networkEgressFileName)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write egress config: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Remove(path)
		resetNetworkEgressForTest()
	})
	resetNetworkEgressForTest()
}

func TestOutboundAllowedByDefault(t *testing.T) {
	resetNetworkEgressForTest()
	if !requireOutbound() {
		t.Fatal("expected outbound allowed when no config file exists")
	}
}

func TestOutboundBlockedWhenGateClosed(t *testing.T) {
	writeNetworkEgressConfig(t, `{"allow_outbound": false}`)
	if requireOutbound() {
		t.Fatal("expected outbound blocked by gate")
	}
}

func TestOutboundAllowedWhenGateOpen(t *testing.T) {
	writeNetworkEgressConfig(t, `{"allow_outbound": true}`)
	if !requireOutbound() {
		t.Fatal("expected outbound allowed by explicit gate")
	}
}

func TestAIGenerateRefusesWhenGateClosed(t *testing.T) {
	writeNetworkEgressConfig(t, `{"allow_outbound": false}`)
	client := &aiClient{baseURL: "https://example.invalid", apiKey: "k", model: "m"}
	_, err := client.GenerateReviewCandidates(aiGenerateRequest{Composition: "nihao"})
	if err == nil || !strings.Contains(err.Error(), "allow_outbound") {
		t.Fatalf("expected gate error, got %v", err)
	}
}

func TestSchemeSetDownloadRefusesWhenGateClosed(t *testing.T) {
	writeNetworkEgressConfig(t, `{"allow_outbound": false}`)
	ime := newIsolatedTestIME(t)
	resp := imecore.NewResponse(1, true)
	if ime.downloadSchemeSetAsync(nil, resp) {
		t.Fatal("expected download refused by gate")
	}
	if resp.TrayNotification == nil || !strings.Contains(resp.TrayNotification.Title+resp.TrayNotification.Message, "allow_outbound") {
		t.Fatalf("expected gate tray notification, got %#v", resp.TrayNotification)
	}
}

func TestUserDictSyncRefusesWhenGateClosed(t *testing.T) {
	writeNetworkEgressConfig(t, `{"allow_outbound": false}`)
	ime := newIsolatedTestIME(t)
	resp := imecore.NewResponse(1, true)
	if ime.syncUserDataCommand(resp) {
		t.Fatal("expected sync refused by gate")
	}
	if resp.TrayNotification == nil {
		t.Fatal("expected gate tray notification")
	}
}
