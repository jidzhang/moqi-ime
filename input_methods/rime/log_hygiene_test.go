package rime

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 日志卫生回归（2026-10-18 事故：实验 dll 的 INFO 日志点把
// %LOCALAPPDATA%\MoqiIM\Log 撑到 60GB）。三条防线各配一个测试。

func TestRimeMinLogLevelDefaultsToWarning(t *testing.T) {
	SetDebugLoggingEnabled(false)
	if got := rimeMinLogLevel(); got != 1 {
		t.Fatalf("默认应为 WARNING(1)，实际 %d", got)
	}
}

func TestRimeMinLogLevelAllowsInfoWhenDebugEnabled(t *testing.T) {
	SetDebugLoggingEnabled(true)
	defer SetDebugLoggingEnabled(false)
	if got := rimeMinLogLevel(); got != 0 {
		t.Fatalf("调试档应放行 INFO(0)，实际 %d", got)
	}
}

func TestCleanupOversizedLogDirRemovesOldestFirst(t *testing.T) {
	dir := t.TempDir()
	mk := func(name string, size int64, age time.Duration) {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
		when := time.Now().Add(-age)
		if err := os.Chtimes(path, when, when); err != nil {
			t.Fatal(err)
		}
	}
	mk("old.log", 40<<20, 3*time.Hour)
	mk("mid.log", 40<<20, 2*time.Hour)
	mk("new.log", 40<<20, time.Hour)

	// 阈值临时调低到 60MB 验证策略：应删最旧的直到降到阈值内。
	restore := logCleanupThresholdBytes
	logCleanupThresholdBytes = 60 << 20
	defer func() { logCleanupThresholdBytes = restore }()

	cleanupOversizedLogDir(dir)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "new.log" {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("应只保留最新的 new.log，实际 %v", names)
	}
}

func TestCleanupOversizedLogDirLeavesSmallDirAlone(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "small.log"), make([]byte, 1024), 0o644); err != nil {
		t.Fatal(err)
	}
	cleanupOversizedLogDir(dir)
	if _, err := os.Stat(filepath.Join(dir, "small.log")); err != nil {
		t.Fatalf("小目录不该被清理: %v", err)
	}
}
