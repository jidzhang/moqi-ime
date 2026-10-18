package rime

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
)

// 出站总闸（4.5）：一份本地开关统一管住四个出站口——
// 方案集下载、WebDAV 用户词库同步、云剪贴、AI 联想。
// 默认不建文件即"开"（保持上游行为）；用户写入
// {"allow_outbound": false} 后，所有主动出站统一拒绝并给托盘通知。
// 这是 fork 与上游的正当差异点：一键断网。

const networkEgressFileName = "network_egress.json"

type networkEgressConfig struct {
	AllowOutbound *bool `json:"allow_outbound,omitempty"`
}

var networkEgressState struct {
	mu      sync.RWMutex
	version uint64
	allowed bool
	loaded  bool
}

func networkEgressConfigPath() string {
	root := moqiAppDataDir()
	if root == "" {
		return ""
	}
	return filepath.Join(root, networkEgressFileName)
}

func loadNetworkEgressAllowed() bool {
	path := networkEgressConfigPath()
	if path == "" {
		return true
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return true
	}
	var cfg networkEgressConfig
	if err := json.Unmarshal(body, &cfg); err != nil {
		log.Printf("解析出站总闸配置失败，保持允许出站: %v", err)
		return true
	}
	if cfg.AllowOutbound == nil {
		return true
	}
	return *cfg.AllowOutbound
}

// outboundAllowed 读缓存的总闸状态； deploy/命令入口处可调 reloadNetworkEgress
// 刷新（文件很小，直接读盘也行，这里走缓存是因为命令路径高频）。
func outboundAllowed() bool {
	networkEgressState.mu.RLock()
	if networkEgressState.loaded {
		allowed := networkEgressState.allowed
		networkEgressState.mu.RUnlock()
		return allowed
	}
	networkEgressState.mu.RUnlock()

	allowed := loadNetworkEgressAllowed()
	networkEgressState.mu.Lock()
	networkEgressState.allowed = allowed
	networkEgressState.loaded = true
	networkEgressState.mu.Unlock()
	return allowed
}

func reloadNetworkEgress() {
	allowed := loadNetworkEgressAllowed()
	networkEgressState.mu.Lock()
	networkEgressState.allowed = allowed
	networkEgressState.loaded = true
	networkEgressState.version++
	networkEgressState.mu.Unlock()
}

func resetNetworkEgressForTest() {
	networkEgressState.mu.Lock()
	networkEgressState.loaded = false
	networkEgressState.allowed = true
	networkEgressState.mu.Unlock()
}

// requireOutbound 在四个出站入口调用：总闸关闭时返回 false，调用方应
// 弹通知并直接返回，不再碰网络。
func requireOutbound() bool {
	return outboundAllowed()
}

const outboundBlockedMessage = "已按出站总闸设置拒绝网络请求（network_egress.json allow_outbound=false）"
