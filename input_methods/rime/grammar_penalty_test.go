package rime

import (
	"os"
	"path/filepath"
	"testing"
)

// 场景矩阵的确定性验证（装机场景受文件锁/进程复活竞态干扰，见
// 2026-10-25 场景实录；产品逻辑在此以单测钉死）。

func writeTempGram(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("fake-gram-"+name), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// 手填值最高优先：合法域内原样、越界钳到 -4，且不触发哈希/校准路径。
func TestGrammarPenaltyManualWins(t *testing.T) {
	dir := t.TempDir()
	writeTempGram(t, dir, "x.gram")

	manual := -6.0
	v, known := resolveGrammarPenaltyForDeploy(localBehaviorConfig{GrammarPenalty: &manual}, "x", dir)
	if !known || v != -6 {
		t.Fatalf("expected manual -6, got %v known=%v", v, known)
	}
	illegal := 99.0
	v, known = resolveGrammarPenaltyForDeploy(localBehaviorConfig{GrammarPenalty: &illegal}, "x", dir)
	if !known || v != -4 {
		t.Fatalf("expected clamp -4, got %v known=%v", v, known)
	}
}

// 改名防伪：预设按内容哈希命中，文件改名后仍取预设值（场景 E 的核心断言）。
func TestGrammarPenaltyFactoryPresetRenameProof(t *testing.T) {
	dir := t.TempDir()
	gram := writeTempGram(t, dir, "factory.gram")
	sum := gramFileSHA256(gram)
	if sum == "" {
		t.Fatal("hash failed")
	}
	factoryGramPenalty[sum] = -4
	defer delete(factoryGramPenalty, sum)

	v, known := resolveGrammarPenaltyForDeploy(localBehaviorConfig{}, "factory", dir)
	if !known || v != -4 {
		t.Fatalf("expected preset -4, got %v known=%v", v, known)
	}

	// 改名后（language 跟着新文件名走）：内容不变 → 哈希不变 → 仍命中预设。
	renamed := filepath.Join(dir, "my.gram")
	if err := os.Rename(gram, renamed); err != nil {
		t.Fatal(err)
	}
	v, known = resolveGrammarPenaltyForDeploy(localBehaviorConfig{}, "my", dir)
	if !known || v != -4 {
		t.Fatalf("expected preset -4 after rename, got %v known=%v", v, known)
	}
}

// 陌生模型：无手填/预设/缓存 → known=false（触发后台校准路径）。
func TestGrammarPenaltyUnknownNeedsCalibration(t *testing.T) {
	dir := t.TempDir()
	writeTempGram(t, dir, "stranger.gram")
	if _, known := resolveGrammarPenaltyForDeploy(localBehaviorConfig{}, "stranger", dir); known {
		t.Fatal("expected unknown for stranger gram")
	}
}

// 校准缓存命中：record 后同哈希直接取值。
func TestGrammarPenaltyCacheHit(t *testing.T) {
	dir := t.TempDir()
	gram := writeTempGram(t, dir, "third.gram")
	sum := gramFileSHA256(gram)
	recordCalibratedPenalty(sum, -8)
	defer func() {
		cache := loadGrammarPenaltyCache()
		grammarPenaltyCacheMu.Lock()
		delete(cache.Calibrated, sum)
		grammarPenaltyCacheMu.Unlock()
	}()
	v, known := resolveGrammarPenaltyForDeploy(localBehaviorConfig{}, "third", dir)
	if !known || v != -8 {
		t.Fatalf("expected cached -8, got %v known=%v", v, known)
	}
}

// ClampGrammarPenalty 边界。
func TestClampGrammarPenalty(t *testing.T) {
	cases := []struct {
		in, want float64
	}{
		{-4, -4}, {-20, -20}, {0, 0}, {-0.5, -0.5},
		{-21, -4}, {1, -4}, {99, -4},
	}
	for _, c := range cases {
		if got := ClampGrammarPenalty(c.in); got != c.want {
			t.Fatalf("clamp(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}
