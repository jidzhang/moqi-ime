package rime

import (
	"os"
	"testing"
	"time"

	"github.com/gaboolic/moqi-ime/imecore"
)

func resetTypoMemoryForTest(t *testing.T) {
	t.Helper()
	old := typoMemory
	typoMemory = &typoMemoryStore{entries: map[string]*typoMemoryEntry{}}
	t.Cleanup(func() { typoMemory = old })
}

func TestTypoMemoryRecordAndLookup(t *testing.T) {
	resetTypoMemoryForTest(t)
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)
	typoMemory.lastLoaded = time.Time{}

	typoMemory.record("wofe", "我的")
	if got := typoMemory.lookup("wofe"); got != "我的" {
		t.Fatalf("lookup wofe = %q, want 我的", got)
	}
	if got := typoMemory.lookup("nide"); got != "" {
		t.Fatalf("lookup nide = %q, want empty", got)
	}
	// 非纯字母 raw（含符号/汉字）不记
	typoMemory.record("wo.de", "我的")
	if got := typoMemory.lookup("wo.de"); got != "" {
		t.Fatalf("non-letter raw should not be recorded, got %q", got)
	}
	// 过短 raw 不记
	typoMemory.record("wo", "我")
	if got := typoMemory.lookup("wo"); got != "" {
		t.Fatalf("short raw should not be recorded, got %q", got)
	}
}

func TestTypoMemoryPersistAndReload(t *testing.T) {
	resetTypoMemoryForTest(t)
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)
	typoMemory.lastLoaded = time.Time{}

	for i := 0; i < typoMemorySaveEvery; i++ {
		typoMemory.record("wokde", "我的")
	}
	path := typoMemoryPath()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected persisted file %s: %v", path, err)
	}
	// 模拟重启：清内存后重新载入
	typoMemory = &typoMemoryStore{entries: map[string]*typoMemoryEntry{}}
	typoMemory.lastLoaded = time.Time{}
	if got := typoMemory.lookup("wokde"); got != "我的" {
		t.Fatalf("reloaded lookup = %q, want 我的", got)
	}
}

func TestTypoMemorySwitchChoice(t *testing.T) {
	resetTypoMemoryForTest(t)
	t.Setenv("APPDATA", t.TempDir())
	typoMemory.lastLoaded = time.Time{}

	typoMemory.record("niihao", "你好")
	typoMemory.record("niihao", "你好")
	typoMemory.record("niihao", "拟好") // 用户改选
	if got := typoMemory.lookup("niihao"); got != "拟好" {
		t.Fatalf("lookup after switch = %q, want 拟好", got)
	}
}

func TestHoistCandidate(t *testing.T) {
	list := []string{"我飞", "我的", "王菲"}
	got := hoistCandidate(list, "我的")
	want := []string{"我的", "我飞", "王菲"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("hoist[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
	// 已在第 1 位：原样返回（指针相等，无分配）
	if same := hoistCandidate(list, "我飞"); &same[0] != &list[0] {
		t.Fatalf("no-op hoist should return original slice")
	}
	// 不在列表：原样返回
	if same := hoistCandidate(list, "不存在"); len(same) != len(list) || same[0] != list[0] {
		t.Fatalf("missing text should return original")
	}
}

func TestHoistCandidateEntry(t *testing.T) {
	entries := []imecore.CandidateEntry{{Text: "我飞"}, {Text: "我的"}, {Text: "王菲"}}
	got := hoistCandidateEntry(entries, "我的")
	if got[0].Text != "我的" || got[1].Text != "我飞" || got[2].Text != "王菲" {
		t.Fatalf("entry hoist = %v", []string{got[0].Text, got[1].Text, got[2].Text})
	}
}

func TestTypoMemoryCap(t *testing.T) {
	resetTypoMemoryForTest(t)
	t.Setenv("APPDATA", t.TempDir())
	typoMemory.lastLoaded = time.Time{}
	// 直接塞超限条目，验证 save 后行数受限
	for i := 0; i < typoMemoryMaxEntries+50; i++ {
		typoMemory.entries["raw"+string(rune('a'+i%26))+string(rune('a'+i/26%26))+string(rune('a'+i/676))] =
			&typoMemoryEntry{count: i + 1, text: "词"}
	}
	typoMemory.save()
	path := typoMemoryPath()
	if path == "" {
		t.Skip("no writable config dir")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved: %v", err)
	}
	lines := 0
	for _, b := range data {
		if b == '\n' {
			lines++
		}
	}
	if lines > typoMemoryMaxEntries {
		t.Fatalf("saved %d lines, cap %d", lines, typoMemoryMaxEntries)
	}
}
