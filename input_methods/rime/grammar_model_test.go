package rime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectAutoGrammarPicksLargest(t *testing.T) {
	dir := t.TempDir()
	writeGram := func(name string, size int) {
		body := make([]byte, size)
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	writeGram("zh-moqi.gram", 7<<20)              // 内置小模型，64MB 以下不选
	writeGram("zh-hans-t-essay-bgw.gram", 206<<20) // 大模型
	writeGram("small2.gram", 1<<20)

	if got := detectAutoGrammarLanguage(dir); got != "zh-hans-t-essay-bgw" {
		t.Fatalf("expected zh-hans-t-essay-bgw, got %q", got)
	}
}

func TestDetectAutoGrammarEmptyDir(t *testing.T) {
	if got := detectAutoGrammarLanguage(t.TempDir()); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestResolveGrammarLanguageModes(t *testing.T) {
	dir := t.TempDir()
	body := make([]byte, 100<<20)
	_ = os.WriteFile(filepath.Join(dir, "my-model.gram"), body, 0o644)

	if got := resolveGrammarLanguage(dir, localBehaviorConfig{}); got != "my-model" {
		t.Fatalf("auto should pick my-model, got %q", got)
	}
	off := "off"
	if got := resolveGrammarLanguage(dir, localBehaviorConfig{GrammarModel: &off}); got != "" {
		t.Fatalf("off should disable, got %q", got)
	}
	named := " zh-custom.gram "
	if got := resolveGrammarLanguage("", localBehaviorConfig{GrammarModel: &named}); got != "zh-custom" {
		t.Fatalf("explicit name should win, got %q", got)
	}
}

func TestBuildPatchIncludesGrammarAuto(t *testing.T) {
	dir := t.TempDir()
	body := make([]byte, 100<<20)
	_ = os.WriteFile(filepath.Join(dir, "big.gram"), body, 0o644)

	patch := buildFrostCustomPatch(localBehaviorConfig{}, dir)
	if !strings.Contains(patch, "grammar/language: big") {
		t.Fatalf("expected grammar patch, got:\n%s", patch)
	}
	if !strings.Contains(patch, `"translator/contextual_suggestions": true`) {
		t.Fatalf("expected contextual_suggestions, got:\n%s", patch)
	}
}
