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

func TestDefaultMixedPairRerankOn(t *testing.T) {
	cfg := defaultBigramRerankConfig()
	if !cfg.MixedPair || cfg.PairRatio != 999 || cfg.PairFloor != 300 {
		t.Fatalf("mixed pair rerank 默认应开（999/300），got %+v", cfg)
	}
	if cfg.Enabled {
		t.Fatal("上文上下文版重排默认应保持关闭")
	}
}

func TestBuildDisplayPerm(t *testing.T) {
	mk := func(xs ...string) []candidateItem {
		out := make([]candidateItem, len(xs))
		for i, s := range xs {
			out[i] = candidateItem{Text: s}
		}
		return out
	}
	// 同序 → nil（零开销）
	if got := buildDisplayPerm(mk("可以", "抗疫"), mk("可以", "抗疫")); got != nil {
		t.Fatalf("同序应返回 nil, got %v", got)
	}
	// 交换 → [1,0]
	got := buildDisplayPerm(mk("可以", "抗疫", "刻意"), mk("抗疫", "可以", "刻意"))
	if len(got) != 3 || got[0] != 1 || got[1] != 0 || got[2] != 2 {
		t.Fatalf("交换映射错误: %v", got)
	}
	// 重复文本（同词多码）：双射不错位
	got = buildDisplayPerm(mk("的", "的", "地"), mk("地", "的", "的"))
	if len(got) != 3 || got[0] != 2 || got[1] != 0 || got[2] != 1 {
		t.Fatalf("重复文本映射错误: %v", got)
	}
	// 长度不一致 → nil
	if got := buildDisplayPerm(mk("a", "b"), mk("b")); got != nil {
		t.Fatalf("长度不一致应返回 nil, got %v", got)
	}
}

// 用户实报 bug 的回归测试：整词（可以 整词证据高）不能被垃圾搭配（抗疫）
// 挤掉——即使首名是组句垃圾（整词 0）。and shid 这类真赢面仍要提前。
func TestMixedPairRerankRespectsWordEvidence(t *testing.T) {
	resetBigramTableForTest()
	sharedBigramTable.mu.Lock()
	sharedBigramTable.pairCounts = map[string]map[string]int{
		"抗": {"疫": 823},   // 抗疫：搭配有据，整词无据
		"是": {"的": 29010}, // 是的：搭配强
	}
	sharedBigramTable.wordMass = map[string]int{
		"可以": 19516, // 真实高频整词
		"时代": 7513,
	}
	sharedBigramTable.loaded = true
	sharedBigramTable.mu.Unlock()

	ime := newTestIME()
	ime.contextualCfg = &contextualConfig{BigramRerank: bigramRerankConfig{
		MixedPair: true, PairRatio: 999, PairFloor: 300, PairMaxLen: 4, PairWordRatio: 1.0,
	}}

	// k yi 全新词典实况（2026-10-23 用户在新电脑实报首选"看一"）：
	// 搭配通道救不了无搭配的真词（可以 搭配 0），这里固定当前行为——
	// 首名组句垃圾 + 可以 无搭配 → 不换位（整词通道方案已被 d3 基准否决：
	// 无约束 65.9→45.3、i≤2 约束 →56.7）。
	cands := []candidateItem{{Text: "看一 ∞"}, {Text: "可以"}, {Text: "抗疫"}}
	got, _, changed := ime.applyMixedPairRerank(cands, 0, "k yi")
	if changed || got[0].Text != "看一 ∞" {
		t.Fatalf("搭配通道下不应换位（记录现状，见 report 10-23）: changed=%t first=%q", changed, got[0].Text)
	}
	// 2026-10-23 生产实况：用户词典调频后引擎序就是 [可以#1, 抗疫#2]——
	// 抗疫越位时上方恰好只有首名，闸门必须看到首名的整词证据。
	cands3 := []candidateItem{{Text: "可以"}, {Text: "抗疫"}, {Text: "刻意"}}
	got3, _, changed3 := ime.applyMixedPairRerank(cands3, 0, "k yi")
	if changed3 || got3[0].Text != "可以" {
		t.Fatalf("抗疫 越过首名 可以 应被闸门拦截: changed=%t first=%q", changed3, got3[0].Text)
	}
	// shi d：时代（整词 7513）在前，是的 搭配 29010 → 应提前
	cands2 := []candidateItem{{Text: "时代"}, {Text: "是的"}, {Text: "使得"}}
	got2, _, changed2 := ime.applyMixedPairRerank(cands2, 0, "shi d")
	if !changed2 || got2[0].Text != "是的" {
		t.Fatalf("是的 应提前: changed=%t first=%q", changed2, got2[0].Text)
	}
}
