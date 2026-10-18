package rime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gaboolic/moqi-ime/input_methods/rime/cloudclipboard"
)

const cloudClipboardConfigFileName = "cloud_clipboard.json"

const (
	ID_CLOUD_CLIPBOARD_SETTINGS = 5001
	ID_CLOUD_CLIPBOARD_ENABLED  = 5002
	ID_CLOUD_CLIPBOARD_TEST     = 5003
	ID_WEBDAV_SETTINGS          = 5004
)

type cloudClipboardFileConfig struct {
	Enabled        bool   `json:"enabled"`
	BaseURL        string `json:"base_url"`
	Username       string `json:"username"`
	SettingsRoot   string `json:"settings_root"`
	MinIntervalSec int    `json:"min_interval_sec"`
	ListHotkey     string `json:"list_hotkey"`
}

var (
	cloudClipboardServiceMu sync.Mutex
	cloudClipboardService   *cloudclipboard.Sync
)

func cloudClipboardConfigPath() string {
	root := moqiAppDataDir()
	if root == "" {
		return ""
	}
	return filepath.Join(root, cloudClipboardConfigFileName)
}

func loadCloudClipboardConfig() cloudclipboard.Config {
	cfg := cloudclipboard.Config{
		SettingsRoot:   cloudclipboard.DefaultSettingsRoot,
		MinIntervalSec: 10,
		ListHotkey:     cloudClipboardDefaultListHotkey,
	}
	path := cloudClipboardConfigPath()
	if path == "" {
		return cfg
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg
	}
	var raw cloudClipboardFileConfig
	if err := json.Unmarshal(data, &raw); err != nil {
		return cfg
	}
	cfg.Enabled = raw.Enabled
	cfg.BaseURL = cloudclipboard.NormalizeBaseURL(raw.BaseURL)
	cfg.Username = strings.TrimSpace(raw.Username)
	cfg.Password = readCloudClipboardPassword()
	cfg.SettingsRoot = cloudclipboard.NormalizeDir(raw.SettingsRoot)
	if raw.MinIntervalSec > 0 {
		cfg.MinIntervalSec = raw.MinIntervalSec
	}
	if hk := strings.TrimSpace(raw.ListHotkey); isOldCloudClipboardDefaultHotkey(hk) {
		cfg.ListHotkey = cloudClipboardDefaultListHotkey
	} else if hk != "" {
		cfg.ListHotkey = hk
	}
	return cfg
}

func isOldCloudClipboardDefaultHotkey(hotkey string) bool {
	return strings.EqualFold(hotkey, cloudClipboardOldShiftVHotkey) ||
		strings.EqualFold(hotkey, cloudClipboardOldShiftZeroHotkey)
}

func saveCloudClipboardConfig(cfg cloudclipboard.Config, newPassword string, keepPassword bool) error {
	path := cloudClipboardConfigPath()
	if path == "" {
		return fmt.Errorf("无法确定配置目录")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw := cloudClipboardFileConfig{
		Enabled:        cfg.Enabled,
		BaseURL:        cloudclipboard.NormalizeBaseURL(cfg.BaseURL),
		Username:       strings.TrimSpace(cfg.Username),
		SettingsRoot:   cloudclipboard.NormalizeDir(cfg.SettingsRoot),
		MinIntervalSec: cfg.MinIntervalSec,
		ListHotkey:     cfg.ListHotkey,
	}
	if raw.MinIntervalSec <= 0 {
		raw.MinIntervalSec = 10
	}
	if raw.ListHotkey == "" {
		raw.ListHotkey = cloudClipboardDefaultListHotkey
	}
	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	if newPassword != "" {
		if err := saveCloudClipboardPassword(newPassword); err != nil {
			return err
		}
		cfg.Password = newPassword
	} else if !keepPassword {
		if err := saveCloudClipboardPassword(""); err != nil {
			return err
		}
		cfg.Password = ""
	} else {
		cfg.Password = readCloudClipboardPassword()
	}
	reloadCloudClipboardService(cfg)
	return nil
}

func globalCloudClipboardService() *cloudclipboard.Sync {
	cloudClipboardServiceMu.Lock()
	defer cloudClipboardServiceMu.Unlock()
	if cloudClipboardService == nil {
		cloudClipboardService = cloudclipboard.NewSync(loadCloudClipboardConfig())
	}
	return cloudClipboardService
}

func reloadCloudClipboardService(cfg cloudclipboard.Config) {
	cloudClipboardServiceMu.Lock()
	defer cloudClipboardServiceMu.Unlock()
	if cloudClipboardService == nil {
		cloudClipboardService = cloudclipboard.NewSync(cfg)
		return
	}
	cloudClipboardService.ReloadConfig(cfg)
}

func reloadCloudClipboardServiceFromDisk() {
	reloadCloudClipboardService(loadCloudClipboardConfig())
}

// UploadCloudClipboardFromLauncher handles clipboard uploads from MoqiLauncher.
func UploadCloudClipboardFromLauncher(text string) {
	if !requireOutbound() {
		return
	}
	globalCloudClipboardService().UploadFromRequest(text)
}
