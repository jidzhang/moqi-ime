package rime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeLocalBehaviorConfig(t *testing.T, body string) {
	t.Helper()
	appData := os.Getenv("APPDATA")
	if appData == "" {
		t.Skip("APPDATA is not set")
	}
	root := filepath.Join(appData, APP)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	path := filepath.Join(root, localBehaviorFileName)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write behavior config: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
}

func TestBuildFrostCustomPatchSlashOff(t *testing.T) {
	off := true
	patch := buildFrostCustomPatch(localBehaviorConfig{DisableSlashPunct: &off}, "")
	if !strings.Contains(patch, "recognizer/patterns/punct: null") {
		t.Fatalf("expected punct null patch, got:\n%s", patch)
	}
}

func TestBuildFrostCustomPatchSlashVOnly(t *testing.T) {
	vonly := true
	patch := buildFrostCustomPatch(localBehaviorConfig{SlashPunctVOnly: &vonly}, "")
	if !strings.Contains(patch, `^v([0-9]|10|[A-Za-z]+)$`) {
		t.Fatalf("expected v-only punct patch, got:\n%s", patch)
	}
}

func TestBuildFrostCustomPatchAuxLookupOff(t *testing.T) {
	off := true
	patch := buildFrostCustomPatch(localBehaviorConfig{DisableAuxLookup: &off}, "")
	if !strings.Contains(patch, "engine/translators/-: table_translator@frost_aux") {
		t.Fatalf("expected aux translator removal, got:\n%s", patch)
	}
	if !strings.Contains(patch, "engine/filters/-: lua_filter@*aux_lookup_filter") {
		t.Fatalf("expected aux filter removal, got:\n%s", patch)
	}
}

func TestBuildFrostCustomPatchEmptyWhenNoSwitch(t *testing.T) {
	if got := buildFrostCustomPatch(localBehaviorConfig{}, ""); got != "" {
		t.Fatalf("expected empty patch with no switches, got:\n%s", got)
	}
}

func TestSyncFrostCustomPatchWritesAndRemovesManaged(t *testing.T) {
	userDir := t.TempDir()
	off := true
	writeLocalBehaviorConfig(t, `{"disable_slash_punct": true, "disable_aux_lookup": true}`)
	_ = off

	if !syncFrostCustomPatch(userDir, "") {
		t.Fatal("expected patch file to be written")
	}
	target := filepath.Join(userDir, frostCustomFileName)
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read patch file: %v", err)
	}
	if !strings.Contains(string(body), "recognizer/patterns/punct: null") {
		t.Fatalf("unexpected patch content:\n%s", body)
	}
	if !strings.Contains(string(body), frostCustomManagedMark) {
		t.Fatalf("expected managed mark in patch:\n%s", body)
	}

	// 开关全关后，之前生成的文件应被删掉以恢复上游行为。
	writeLocalBehaviorConfig(t, `{}`)
	if !syncFrostCustomPatch(userDir, "") {
		t.Fatal("expected managed patch file to be removed")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("expected patch file removed, stat err=%v", err)
	}
}

func TestSyncFrostCustomPatchKeepsHandEditedFile(t *testing.T) {
	userDir := t.TempDir()
	target := filepath.Join(userDir, frostCustomFileName)
	hand := "patch:\n  recognizer/patterns/punct: null\n"
	if err := os.WriteFile(target, []byte(hand), 0o644); err != nil {
		t.Fatalf("write hand file: %v", err)
	}

	writeLocalBehaviorConfig(t, `{"disable_aux_lookup": true}`)
	if syncFrostCustomPatch(userDir, "") {
		t.Fatal("expected hand-edited file to be left alone")
	}
	body, err := os.ReadFile(target)
	if err != nil || string(body) != hand {
		t.Fatalf("hand-edited file was modified: %q err=%v", body, err)
	}
}

func TestBuildFrostCustomPatchBigGrammarTuning(t *testing.T) {
	dir := t.TempDir()
	// 伪造一个 ≥64MB 的 gram 文件
	big := make([]byte, (64<<20)+1)
	if err := os.WriteFile(filepath.Join(dir, "zh-hans-t-essay-bgw.gram"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := localBehaviorConfig{}
	patch := buildFrostCustomPatch(cfg, dir)
	if !strings.Contains(patch, "grammar/language: zh-hans-t-essay-bgw") {
		t.Fatalf("应包含 grammar/language:\n%s", patch)
	}
	if !strings.Contains(patch, `"translator/abbrev_sentences": true`) {
		t.Fatalf("大 gram 应启用 abbrev_sentences:\n%s", patch)
	}
	if !strings.Contains(patch, `"melt_eng/initial_quality": 0.01`) {
		t.Fatalf("大 gram 应降 melt_eng 权重:\n%s", patch)
	}
}

func TestBuildFrostCustomPatchSmallGrammarNoTuning(t *testing.T) {
	dir := t.TempDir()
	// 只有小 gram（<64MB）：不启用 abbrev_sentences
	if err := os.WriteFile(filepath.Join(dir, "zh-moqi.gram"), make([]byte, 1024), 0o644); err != nil {
		t.Fatal(err)
	}
	s := "zh-moqi"
	cfg := localBehaviorConfig{GrammarModel: &s}
	patch := buildFrostCustomPatch(cfg, dir)
	if strings.Contains(patch, "abbrev_sentences") {
		t.Fatalf("小 gram 不应启用 abbrev_sentences:\n%s", patch)
	}
}
