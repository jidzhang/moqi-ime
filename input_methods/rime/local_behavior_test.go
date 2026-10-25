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

// disableMixedPairT 在隔离 APPDATA 下显式关闭 mixed 重排（默认已开），
// 供验证"空 patch 语义"的测试使用。
func disableMixedPairT(t *testing.T) {
	t.Helper()
	// 隔离机器日志档位：trace/debug 档会让 patch 带出 debug/show_candidate_marks
	// 行，测试随机器状态漂移（2026-10-25 踩过：用户开 trace 排障时 4 测试全挂）。
	SetDebugLoggingEnabled(false)
	t.Cleanup(func() { SetDebugLoggingEnabled(true) })
	root := t.TempDir()
	t.Setenv("APPDATA", root)
	t.Setenv("LOCALAPPDATA", t.TempDir())
	if err := os.MkdirAll(filepath.Join(root, "Moqi"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Moqi", "contextual_lift.json"),
		[]byte(`{ "bigram_rerank": { "mixed_pair": false } }`), 0o644); err != nil {
		t.Fatal(err)
	}
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
	disableMixedPairT(t)
	if got := buildFrostCustomPatch(localBehaviorConfig{}, ""); got != "" {
		t.Fatalf("expected empty patch with no switches, got:\n%s", got)
	}
}

func TestSyncFrostCustomPatchWritesAndRemovesManaged(t *testing.T) {
	disableMixedPairT(t)
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
	// 2026-10-23 方案 A：abbrev_sentences 已移除（真人回放 29.7%→64.8%），
	// 大 gram 在位也不得再开混合组句。
	if strings.Contains(patch, "abbrev_sentences") {
		t.Fatalf("大 gram 也不应启用 abbrev_sentences:\n%s", patch)
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

// 2026-10-21 安装竞态回归：TSF DLL 在安装尾声按需拉起后端时共享目录可能
// 尚未落齐（gram 扫描扑空）。初始化路径（ensureFrostCustomPatch）此时
// 必须不写也不删；删除语义只属于托盘「刷新配置」路径（syncFrostCustomPatch）。
func TestEnsureFrostCustomPatchNeverDeletesExistingConfig(t *testing.T) {
	disableMixedPairT(t)
	user := t.TempDir()
	shared := t.TempDir()
	existing := frostCustomManagedMark + "\npatch:\n  grammar/language: keep-me\n"
	if err := os.WriteFile(filepath.Join(user, frostCustomFileName), []byte(existing), 0o644); err != nil {
		t.Fatalf("write existing: %v", err)
	}

	if ensureFrostCustomPatch(user, shared) {
		t.Fatal("expected no write when scan finds no gram")
	}
	body, err := os.ReadFile(filepath.Join(user, frostCustomFileName))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(body) != existing {
		t.Fatalf("existing config must stay untouched, got:\n%s", body)
	}
}

func TestEnsureFrostCustomPatchWritesWhenGramPresent(t *testing.T) {
	user := t.TempDir()
	shared := t.TempDir()
	body := make([]byte, 100<<20)
	if err := os.WriteFile(filepath.Join(shared, "zh-hans-t-essay-bgw.gram"), body, 0o644); err != nil {
		t.Fatalf("write gram: %v", err)
	}

	if !ensureFrostCustomPatch(user, shared) {
		t.Fatal("expected patch written when gram present")
	}
	data, err := os.ReadFile(filepath.Join(user, frostCustomFileName))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(string(data), "grammar/language: zh-hans-t-essay-bgw") {
		t.Fatalf("expected grammar patch, got:\n%s", data)
	}
}

func TestSyncFrostCustomPatchDeletesManagedFileWhenEmpty(t *testing.T) {
	disableMixedPairT(t)
	user := t.TempDir()
	existing := frostCustomManagedMark + "\npatch:\n  grammar/language: old\n"
	if err := os.WriteFile(filepath.Join(user, frostCustomFileName), []byte(existing), 0o644); err != nil {
		t.Fatalf("write existing: %v", err)
	}

	if !syncFrostCustomPatch(user, t.TempDir()) {
		t.Fatal("expected managed file removed on empty patch")
	}
	if _, err := os.Stat(filepath.Join(user, frostCustomFileName)); !os.IsNotExist(err) {
		t.Fatal("expected managed file deleted")
	}
}

// mixed 重排开启时，托管 patch 应联动禁用混合输入的用户词典（治理
// 学习拖累：dedup −1.3 / 原口径 −1.8，2026-10-22）。
func TestBuildPatchMixedUserDictPattern(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir()) // contextual_lift.json 缺省 → rerankMixed=false
	patch := buildFrostCustomPatchEx(localBehaviorConfig{}, false)
	if strings.Contains(patch, "disable_user_dict_for_patterns") {
		t.Fatalf("rerank off 时不应禁用: %s", patch)
	}
	patchOn := buildFrostCustomPatchEx(localBehaviorConfig{}, true)
	if !strings.Contains(patchOn, `".*[bcdfhjklmpqstvwxyz]$"`) {
		t.Fatalf("rerank on 时应含禁用模式: %s", patchOn)
	}
}
