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

func TestPairScoreSplitsAndBounds(t *testing.T) {
	pairs := map[string]map[string]int{
		"是": {"的": 29010, "时代": 0},
		"时": {"代": 12},
		"我": {"要": 26860, "也": 16978},
	}
	if got := pairScore(pairs, "是的", 4); got != 29010 {
		t.Fatalf("是的 = %d, want 29010", got)
	}
	if got := pairScore(pairs, "时代", 4); got != 12 {
		t.Fatalf("时代 = %d, want 12", got)
	}
	// 长候选超上限不打分
	if got := pairScore(pairs, "的时候啊呀", 4); got != 0 {
		t.Fatalf("超长候选 = %d, want 0", got)
	}
	// 单字不打分
	if got := pairScore(pairs, "的", 4); got != 0 {
		t.Fatalf("单字 = %d, want 0", got)
	}
}

func TestMixedPairRerankSwapsOnStrongPair(t *testing.T) {
	resetBigramTableForTest()
	sharedBigramTable.mu.Lock()
	sharedBigramTable.pairCounts = map[string]map[string]int{
		"是": {"的": 29010},
		"时": {"代": 12},
	}
	sharedBigramTable.loaded = true
	sharedBigramTable.mu.Unlock()

	ime := newTestIME()
	ime.contextualCfg = &contextualConfig{
		BigramRerank: bigramRerankConfig{
			MixedPair: true, PairRatio: 1.3, PairFloor: 50, PairMaxLen: 4,
		},
	}
	cands := []candidateItem{{Text: "时代"}, {Text: "是的"}, {Text: "使得"}}
	got, cursor, changed := ime.applyMixedPairRerank(cands, 0, "shid")
	if !changed || got[0].Text != "是的" {
		t.Fatalf("期望 是的 提前: changed=%t first=%q", changed, got[0].Text)
	}
	if cursor != 0 {
		t.Fatalf("cursor 应保持 0, got %d", cursor)
	}
	// 弱对（低于 floor）不换位
	cands2 := []candidateItem{{Text: "是的"}, {Text: "时代"}, {Text: "使得"}}
	if _, _, changed := ime.applyMixedPairRerank(cands2, 0, "shid"); changed {
		t.Fatal("弱对不应换位")
	}
	// 开关关闭不动作
	ime.contextualCfg.BigramRerank.MixedPair = false
	if _, _, changed := ime.applyMixedPairRerank(cands, 0, "shid"); changed {
		t.Fatal("关闭时不应换位")
	}
}
