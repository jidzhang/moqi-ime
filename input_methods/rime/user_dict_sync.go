package rime

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gaboolic/moqi-ime/imecore"
	"github.com/gaboolic/moqi-ime/input_methods/rime/cloudclipboard"
)

type userDictSyncState struct {
	mu      sync.Mutex
	running bool
}

var sharedUserDictSyncState userDictSyncState

func resetUserDictSyncStateForTest() {
	sharedUserDictSyncState.mu.Lock()
	sharedUserDictSyncState.running = false
	sharedUserDictSyncState.mu.Unlock()
}

func (s *userDictSyncState) begin() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return false
	}
	s.running = true
	return true
}

func (s *userDictSyncState) end() {
	s.mu.Lock()
	s.running = false
	s.mu.Unlock()
}

func (ime *IME) syncUserDataCommand(resp *imecore.Response) bool {
	if !requireOutbound() {
		if resp != nil {
			resp.TrayNotification = trayNotification(outboundBlockedMessage, imecore.TrayNotificationIconError)
		}
		return false
	}
	if ime.backend == nil {
		if resp != nil {
			resp.TrayNotification = trayNotification("用户词库同步失败：Rime 后端不可用", imecore.TrayNotificationIconError)
		}
		return false
	}
	if !sharedUserDictSyncState.begin() {
		if resp != nil {
			resp.TrayNotification = trayNotification("用户词库同步已在进行中", imecore.TrayNotificationIconInfo)
			resp.ReturnValue = 1
		}
		return true
	}

	if ime.asyncResponseSender == nil {
		defer sharedUserDictSyncState.end()
		result, err := ime.syncUserDataWithWebDAV()
		if resp != nil {
			resp.TrayNotification = userDictSyncTrayNotification(result, err)
		}
		return err == nil
	}

	if resp != nil {
		resp.TrayNotification = trayNotification("开始同步用户词库...", imecore.TrayNotificationIconInfo)
		resp.ReturnValue = 1
	}
	go func() {
		defer sharedUserDictSyncState.end()
		result, err := ime.syncUserDataWithWebDAV()
		ime.sendAsyncTrayNotification(userDictSyncTrayNotification(result, err))
	}()
	return true
}

func (ime *IME) syncUserDataWithWebDAV() (cloudclipboard.UserDictSnapshotSyncResult, error) {
	var result cloudclipboard.UserDictSnapshotSyncResult
	userDir := ime.userDir()
	if userDir == "" {
		return result, fmt.Errorf("无法确定 Rime 用户目录")
	}
	schemeSet := currentSchemeSetName()
	if !cloudclipboard.IsSchemeSetDirName(schemeSet) {
		return result, fmt.Errorf("方案集名称无效")
	}
	localSyncRoot := filepath.Join(userDir, "sync")

	cfg := loadCloudClipboardConfig()
	webdavReady := cfg.IsComplete() && cloudclipboard.IsAllowedBaseURL(cfg.BaseURL)
	var webdavSync *cloudclipboard.Sync
	if webdavReady {
		cfg.Enabled = true
		webdavSync = cloudclipboard.NewSync(cfg)
		downloaded, err := webdavSync.DownloadUserDictSnapshots(localSyncRoot, schemeSet)
		if err != nil {
			return result, fmt.Errorf("下载远端快照失败: %w", err)
		}
		result.Downloaded = downloaded
	}

	if ime.backend == nil || !ime.backend.SyncUserData() {
		return result, fmt.Errorf("librime 合并用户词库失败")
	}

	if webdavReady {
		deviceID := readRimeInstallationID(userDir)
		if deviceID == "" {
			return result, fmt.Errorf("无法读取本机 Rime installation_id")
		}
		uploaded, err := webdavSync.UploadUserDictSnapshots(localSyncRoot, schemeSet, deviceID)
		if err != nil {
			return result, fmt.Errorf("上传本机快照失败: %w", err)
		}
		result.Uploaded = uploaded
	}
	return result, nil
}

func userDictSyncTrayNotification(result cloudclipboard.UserDictSnapshotSyncResult, err error) *imecore.TrayNotification {
	if err != nil {
		return trayNotification("用户词库同步失败: "+shortErrorMessage(err), imecore.TrayNotificationIconError)
	}
	if result.Downloaded == 0 && result.Uploaded == 0 {
		return trayNotification("用户资料同步完成", imecore.TrayNotificationIconInfo)
	}
	return trayNotification(
		fmt.Sprintf("用户词库同步完成：下载 %d，上传 %d", result.Downloaded, result.Uploaded),
		imecore.TrayNotificationIconInfo,
	)
}

func readRimeInstallationID(userDir string) string {
	file, err := os.Open(filepath.Join(userDir, "installation.yaml"))
	if err != nil {
		return ""
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "#") || !strings.HasPrefix(line, "installation_id:") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(line, "installation_id:"))
		value = strings.Trim(value, `"'`)
		if cloudclipboard.IsSyncDeviceDirName(value) {
			return value
		}
	}
	return ""
}
