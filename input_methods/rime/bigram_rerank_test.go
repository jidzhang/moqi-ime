package rime

import (
	"os"
	"path/filepath"
	"testing"
)

func writeBigramFixture(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, bigramTableFileName), []byte(body), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}

func TestApplyBigramRerankPromotesStrongSuccessor(t *testing.T) {
	ime := newIsolatedTestIME(t)
	resetBigramTableForTest()
	ime.sharedDirOverride = t.TempDir()
	enableBigramRerankForTest(ime)
	writeBigramFixture(t, ime.sharedDirOverride, "n\t的\t是:900 时:20\n")
	ime.pushContextualCommit("的")

	candidates := []candidateItem{{Text: "时"}, {Text: "是"}}
	reordered, _, changed := ime.applyBigramRerank(candidates, 0)
	if !changed || reordered[0].Text != "是" {
		t.Fatalf("expected 是 promoted (900 vs 20), got %v changed=%v", reordered, changed)
	}
}

// enableBigramRerankForTest 显式开重排（默认关闭）。
func enableBigramRerankForTest(ime *IME) {
	cfg := defaultContextualConfig()
	cfg.BigramRerank = defaultBigramRerankConfig()
	cfg.BigramRerank.Enabled = true
	ime.contextualCfg = &cfg
}

func TestApplyBigramRerankConservativeBelowRatio(t *testing.T) {
	ime := newIsolatedTestIME(t)
	resetBigramTableForTest()
	enableBigramRerankForTest(ime)
	ime.sharedDirOverride = t.TempDir()
	writeBigramFixture(t, ime.sharedDirOverride, "n\t的\t是:100 时:80\n")
	ime.pushContextualCommit("的")

	candidates := []candidateItem{{Text: "时"}, {Text: "是"}}
	_, _, changed := ime.applyBigramRerank(candidates, 0)
	if changed {
		t.Fatal("80/100 below ratio 1.6, must not rerank")
	}
}

func TestApplyBigramRerankFloorGuardsNoise(t *testing.T) {
	ime := newIsolatedTestIME(t)
	resetBigramTableForTest()
	enableBigramRerankForTest(ime)
	ime.sharedDirOverride = t.TempDir()
	writeBigramFixture(t, ime.sharedDirOverride, "n\t的\t时:50 是:1\n")
	ime.pushContextualCommit("的")

	// 首选"时"mass=50；2 名"是"mass=1 低于 floor=30，不允许凭倍率上位。
	candidates := []candidateItem{{Text: "时"}, {Text: "是"}}
	_, _, changed := ime.applyBigramRerank(candidates, 0)
	if changed {
		t.Fatal("candidate mass below floor must not rerank")
	}
}

func TestApplyBigramRerankNoRecentCommit(t *testing.T) {
	ime := newIsolatedTestIME(t)
	resetBigramTableForTest()
	ime.sharedDirOverride = t.TempDir()
	writeBigramFixture(t, ime.sharedDirOverride, "n\t的\t是:900\n")

	_, _, changed := ime.applyBigramRerank([]candidateItem{{Text: "时"}, {Text: "是"}}, 0)
	if changed {
		t.Fatal("no recent commit must not rerank")
	}
}

func TestApplyBigramRerankMissingTable(t *testing.T) {
	ime := newIsolatedTestIME(t)
	resetBigramTableForTest()
	ime.sharedDirOverride = t.TempDir() // 无表文件
	ime.pushContextualCommit("的")

	_, _, changed := ime.applyBigramRerank([]candidateItem{{Text: "时"}, {Text: "是"}}, 0)
	if changed {
		t.Fatal("missing table must disable rerank")
	}
}
