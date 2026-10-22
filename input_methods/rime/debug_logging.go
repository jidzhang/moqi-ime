package rime

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type debugLogLevel int

const (
	debugLogLevelOff debugLogLevel = iota
	debugLogLevelDebug
	debugLogLevelTrace
)

var debugLogState struct {
	mu          sync.Mutex
	lastRefresh time.Time
	level       debugLogLevel
	override    *debugLogLevel
}

type launcherDebugConfig struct {
	LogLevel string `json:"logLevel"`
}

func debugConfigPath() string {
	localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
	if localAppData == "" {
		return ""
	}
	return filepath.Join(localAppData, "MoqiIM", "MoqiLauncher.json")
}

func parseDebugLogLevel(raw string) debugLogLevel {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "trace":
		return debugLogLevelTrace
	case "debug":
		return debugLogLevelDebug
	default:
		return debugLogLevelOff
	}
}

func currentDebugLogLevel() debugLogLevel {
	debugLogState.mu.Lock()
	defer debugLogState.mu.Unlock()

	if debugLogState.override != nil {
		return *debugLogState.override
	}

	now := time.Now()
	if !debugLogState.lastRefresh.IsZero() && now.Sub(debugLogState.lastRefresh) < time.Second {
		return debugLogState.level
	}

	debugLogState.level = debugLogLevelOff
	path := debugConfigPath()
	if path != "" {
		if data, err := os.ReadFile(path); err == nil {
			var cfg launcherDebugConfig
			if json.Unmarshal(data, &cfg) == nil {
				debugLogState.level = parseDebugLogLevel(cfg.LogLevel)
			}
		}
	}

	debugLogState.lastRefresh = now
	return debugLogState.level
}

func SetDebugLoggingEnabled(enabled bool) {
	debugLogState.mu.Lock()
	defer debugLogState.mu.Unlock()

	level := debugLogLevelOff
	if enabled {
		level = debugLogLevelDebug
	}
	debugLogState.override = &level
	debugLogState.level = level
	debugLogState.lastRefresh = time.Now()
}

func isDebugLoggingEnabled() bool {
	return currentDebugLogLevel() >= debugLogLevelDebug
}

func isTraceLoggingEnabled() bool {
	return currentDebugLogLevel() >= debugLogLevelTrace
}

func debugLogf(format string, args ...any) {
	if isDebugLoggingEnabled() {
		log.Printf(format, args...)
	}
}

func traceLogf(format string, args ...any) {
	if isTraceLoggingEnabled() {
		log.Printf(format, args...)
	}
}

// 产品默认的 rime.dll 日志级别：只记录 WARNING 及以上。
// 引擎 INFO 粒度太细（每个候选词的评分都可能是一条），默认放行会造成
// 日志目录成 GB 级增长（2026-10-18 实测事故：单次全量评估产生 ~16GB）。
const rimeDefaultMinLogLevel = 1 // 0=INFO 1=WARNING 2=ERROR

// rimeMinLogLevel 决定传给 librime traits 的 min_log_level。
// 调试档（MoqiLauncher.json logLevel=debug/trace）放行 INFO，否则只留 WARNING。
func rimeMinLogLevel() int {
	if currentDebugLogLevel() >= debugLogLevelDebug {
		return 0
	}
	return rimeDefaultMinLogLevel
}

// logCleanupThresholdBytes 日志目录体积上限（512MB）。
// 超限时按修改时间删除最旧的文件，兜住任何"日志点失控"的意外；
// 正常运行时 glog 自身已有 32MB 单文件上限 + 3 天保留期，不会触发。
var logCleanupThresholdBytes = int64(512 << 20)

// cleanupOversizedLogDir 扫描日志目录，超过阈值就按 mtime 从旧到新删除，
// 直到降到阈值以内。删除失败（文件被占用）即停止，不影响启动。
func cleanupOversizedLogDir(dir string) {
	if dir == "" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type fileInfo struct {
		path string
		size int64
		mod  time.Time
	}
	var files []fileInfo
	var total int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, fileInfo{filepath.Join(dir, e.Name()), info.Size(), info.ModTime()})
		total += info.Size()
	}
	if total <= logCleanupThresholdBytes {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	for _, f := range files {
		if total <= logCleanupThresholdBytes {
			break
		}
		if err := os.Remove(f.path); err != nil {
			continue
		}
		total -= f.size
	}
}
