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
	writeGram("zh-moqi.gram", 7<<20)               // 内置小模型，64MB 以下不选
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

// 用户目录（%APPDATA% 下的 Moqi/Rime）也参与自动扫描：普通权限可放
// gram，覆盖安装不会删（2026-10-21 定案，见 PACKAGES.md）。
func TestDetectAutoGrammarScansUserDir(t *testing.T) {
	shared := t.TempDir()
	user := t.TempDir()
	body := make([]byte, 100<<20)
	if err := os.WriteFile(filepath.Join(user, "zh-hans-t-essay-bgw.gram"), body, 0o644); err != nil {
		t.Fatalf("write user gram: %v", err)
	}

	if got := detectAutoGrammarLanguage(shared, user); got != "zh-hans-t-essay-bgw" {
		t.Fatalf("expected user-dir gram picked, got %q", got)
	}
	patch := buildFrostCustomPatch(localBehaviorConfig{}, shared, user)
	if !strings.Contains(patch, "grammar/language: zh-hans-t-essay-bgw") {
		t.Fatalf("expected grammar patch from user dir, got %q", patch)
	}
	if !bigGrammarActive("zh-hans-t-essay-bgw", shared, user) {
		t.Fatal("expected bigGrammarActive true via user dir")
	}
}

func TestResolveGrammarLanguageModes(t *testing.T) {
	dir := t.TempDir()
	body := make([]byte, 100<<20)
	_ = os.WriteFile(filepath.Join(dir, "my-model.gram"), body, 0o644)

	if got := resolveGrammarLanguage(localBehaviorConfig{}, dir); got != "my-model" {
		t.Fatalf("auto should pick my-model, got %q", got)
	}
	off := "off"
	if got := resolveGrammarLanguage(localBehaviorConfig{GrammarModel: &off}, dir); got != "" {
		t.Fatalf("off should disable, got %q", got)
	}
	named := " zh-custom.gram "
	if got := resolveGrammarLanguage(localBehaviorConfig{GrammarModel: &named}); got != "zh-custom" {
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
