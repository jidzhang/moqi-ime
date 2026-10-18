package rime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func liftedTestIME(t *testing.T) *IME {
	t.Helper()
	ime := newIsolatedTestIME(t)
	ime.contextualCfg = nil // 触发默认配置懒加载
	return ime
}

func TestContextualLiftPromotesContinuation(t *testing.T) {
	ime := liftedTestIME(t)
	ime.pushContextualCommit("你好")

	candidates := []candidateItem{{Text: "妈"}, {Text: "吗"}, {Text: "马"}, {Text: "码"}}
	reordered, cursor, changed := ime.applyContextualLift(candidates, 0)
	if !changed {
		t.Fatal("expected contextual lift to reorder candidates")
	}
	if reordered[0].Text != "吗" {
		t.Fatalf("expected 吗 promoted to first, got %q", reordered[0].Text)
	}
	// 稳定重排：其余候选相对顺序不变。
	if reordered[1].Text != "妈" || reordered[2].Text != "马" || reordered[3].Text != "码" {
		t.Fatalf("unexpected order after lift: %v", reordered)
	}
	if cursor != 1 {
		t.Fatalf("expected cursor to follow 妈 to index 1, got %d", cursor)
	}
}

func TestContextualLiftRemapsCursor(t *testing.T) {
	ime := liftedTestIME(t)
	ime.pushContextualCommit("你好")

	candidates := []candidateItem{{Text: "妈"}, {Text: "吗"}}
	reordered, cursor, changed := ime.applyContextualLift(candidates, 1)
	if !changed {
		t.Fatal("expected contextual lift to reorder candidates")
	}
	if reordered[0].Text != "吗" || cursor != 0 {
		t.Fatalf("expected cursor remapped to 吗 at 0, got %q@%d", reordered[0].Text, cursor)
	}
}

func TestContextualLiftInactiveWithoutRecentCommit(t *testing.T) {
	ime := liftedTestIME(t)

	candidates := []candidateItem{{Text: "妈"}, {Text: "吗"}}
	_, _, changed := ime.applyContextualLift(candidates, 0)
	if changed {
		t.Fatal("expected no lift without a recent commit")
	}
}

func TestContextualLiftExpires(t *testing.T) {
	ime := liftedTestIME(t)
	ime.contextualRecentCommits = []contextualCommitRecord{
		{text: "你好", at: time.Now().Add(-time.Hour)},
	}

	candidates := []candidateItem{{Text: "妈"}, {Text: "吗"}}
	_, _, changed := ime.applyContextualLift(candidates, 0)
	if changed {
		t.Fatal("expected expired commit to be ignored")
	}
}

func TestContextualLiftLongestTailFirstThenFallback(t *testing.T) {
	ime := liftedTestIME(t)
	ime.pushContextualCommit("你好")
	// "你好"的接续（吗）不在候选里，应回退到短尾"好"的接续（坏）。
	cfg := defaultContextualConfig()
	cfg.Continuations["好"] = []string{"坏"}
	ime.contextualCfg = &cfg

	candidates := []candidateItem{{Text: "怀"}, {Text: "坏"}}
	reordered, _, changed := ime.applyContextualLift(candidates, 0)
	if !changed {
		t.Fatal("expected fallback tail lift")
	}
	if reordered[0].Text != "坏" {
		t.Fatalf("expected 坏 promoted via shorter tail, got %q", reordered[0].Text)
	}
}

func TestContextualLiftDisabledBySwitch(t *testing.T) {
	ime := liftedTestIME(t)
	ime.pushContextualCommit("你好")
	cfg := defaultContextualConfig()
	cfg.Enabled = false
	ime.contextualCfg = &cfg

	candidates := []candidateItem{{Text: "妈"}, {Text: "吗"}}
	_, _, changed := ime.applyContextualLift(candidates, 0)
	if changed {
		t.Fatal("expected disabled switch to skip lift")
	}
}

func TestContextualConfigFileMergesExtraContinuations(t *testing.T) {
	ime := liftedTestIME(t)

	appData := os.Getenv("APPDATA")
	if appData == "" {
		t.Skip("APPDATA is not set")
	}
	root := filepath.Join(appData, APP)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	configPath := filepath.Join(root, contextualConfigFileName)
	body, err := json.Marshal(map[string]any{
		"extra_continuations": map[string][]string{
			"墨奇": {"好"},
		},
	})
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(configPath, body, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(configPath) })

	ime.reloadContextualConfig()
	ime.pushContextualCommit("墨奇")

	candidates := []candidateItem{{Text: "号"}, {Text: "好"}}
	reordered, _, changed := ime.applyContextualLift(candidates, 0)
	if !changed || reordered[0].Text != "好" {
		t.Fatalf("expected user continuation 好 promoted, changed=%v got %v", changed, reordered)
	}
}

func TestPushContextualCommitKeepsRecentOnly(t *testing.T) {
	ime := liftedTestIME(t)

	ime.contextualRecentCommits = append(ime.contextualRecentCommits,
		contextualCommitRecord{text: "旧句一", at: time.Now().Add(-time.Hour)})
	ime.pushContextualCommit("旧句二")
	ime.pushContextualCommit("新句")

	if len(ime.contextualRecentCommits) != maxContextualRecentCommits {
		t.Fatalf("expected %d records, got %d", maxContextualRecentCommits, len(ime.contextualRecentCommits))
	}
	if ime.contextualRecentCommits[0].text != "旧句二" || ime.contextualRecentCommits[1].text != "新句" {
		t.Fatalf("unexpected records kept: %v", ime.contextualRecentCommits)
	}
}
