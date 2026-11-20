package rime

import "testing"

// 搭配层两案 v3（PMI 判别）：正解搭配 PMI 3.66~5.75，泛用字链 ≤1.59 或
// 频次 <3 不进判决——数据实测阈值 2.0 一刀分开（见卷宗 2026-11-20）。
// 表按真实 zh-bigram 的量级手工缩放：N=100000，mass 模拟高频/低频词。
func collocTestTable() (map[string]map[string]int, map[string]int) {
	pairs := map[string]map[string]int{
		"这个":   {"是": 990},
		"务必":   {"解决": 3, "要": 20},
		"无比":   {"的": 140},
		"随着":   {"时间": 634},
		"时间":   {"推移": 96},
		"推移":   {"的": 12},
		"太原":   {"的": 8},
		"解决":   {"了": 50},
		"今天":   {"是": 800},
		"不知道":  {"我": 265},
		"我":     {"的": 4000},
	}
	mass := map[string]int{}
	for h, row := range pairs {
		tot := 0
		for _, c := range row {
			tot += c
		}
		mass[h] += tot
		for w, c := range row {
			mass[w] += c
		}
	}
	// 高频词放大（模拟语料真实量级差）
	mass["我"] += 100000
	mass["的"] += 100000
	mass["是"] += 50000
	return pairs, mass
}

func TestCollocScorePmi(t *testing.T) {
	pairs, mass := collocTestTable()
	const N = 1000000
	const pmiMin = 2.0
	// 务必→解决:3 是低频凝固搭配（mass 小 → PMI 高），必须计入。
	if cnt, _ := collocScore(pairs, mass, N, pmiMin, "这个务必解决"); cnt != 1 {
		t.Fatalf("这个务必解决 强搭配对数 = %d, want 1", cnt)
	}
	// 无比→解决 0 对。
	if cnt, _ := collocScore(pairs, mass, N, pmiMin, "这个无比解决"); cnt != 0 {
		t.Fatalf("这个无比解决 = %d, want 0", cnt)
	}
	// 随着时间推移 = 2 对（634/96 都过线）。
	if cnt, _ := collocScore(pairs, mass, N, pmiMin, "随着时间推移"); cnt != 2 {
		t.Fatalf("随着时间推移 = %d, want 2", cnt)
	}
	// 泛用链：不知道→我:265 有据但 PMI 低（mass[我] 大）→ 不计。
	if cnt, _ := collocScore(pairs, mass, N, pmiMin, "不知道我的"); cnt != 0 {
		t.Fatalf("泛用链 不知道我的 = %d, want 0", cnt)
	}
}

func TestCollocRerankSwapsTwoCases(t *testing.T) {
	resetBigramTableForTest()
	pairs, mass := collocTestTable()
	sharedBigramTable.mu.Lock()
	sharedBigramTable.pairCounts = pairs
	sharedBigramTable.wordMass = mass
	sharedBigramTable.loaded = true
	sharedBigramTable.mu.Unlock()
	sharedBigramTableTotalTokens = 1000000

	ime := newTestIME()
	ime.contextualCfg = &contextualConfig{
		BigramRerank: bigramRerankConfig{
			MixedPair: true, PairRatio: 999, PairFloor: 300, PairMaxLen: 4,
			PairWordRatio: 1.0, CollocPmi: 2.0,
		},
	}
	// 案件一：全拼组字。
	cands := []candidateItem{
		{Text: "这个无比解决"}, {Text: "这个务必解决"},
	}
	got, _, changed := ime.applyCollocationRerank(cands, 0)
	if !changed || got[0].Text != "这个务必解决" {
		t.Fatalf("案件一未翻正: changed=%t first=%q", changed, got[0].Text)
	}
	// 案件二：混拼组字。
	cands2 := []candidateItem{
		{Text: "随着时间太原"}, {Text: "随着时间推移"},
	}
	got2, _, changed2 := ime.applyCollocationRerank(cands2, 0)
	if !changed2 || got2[0].Text != "随着时间推移" {
		t.Fatalf("案件二未翻正: changed=%t first=%q", changed2, got2[0].Text)
	}
	// 反向保护：首名本身强搭配更多时不被动。
	cands3 := []candidateItem{
		{Text: "随着时间推移"}, {Text: "随着时间太原"},
	}
	got3, _, changed3 := ime.applyCollocationRerank(cands3, 0)
	if changed3 || got3[0].Text != "随着时间推移" {
		t.Fatalf("反向误换: changed=%t first=%q", changed3, got3[0].Text)
	}
	// 泛用链不换位（v1 击穿形态：不知道我的 #2 不许上）。
	cands4 := []candidateItem{
		{Text: "不知道是不是的"}, {Text: "不知道我的谁"},
	}
	_, _, changed4 := ime.applyCollocationRerank(cands4, 0)
	if changed4 {
		t.Fatalf("泛用链被换位")
	}
	// 短候选不参与（v1 守卫实炸形态）。
	cands5 := []candidateItem{
		{Text: "时代"}, {Text: "是的"},
	}
	if _, _, c5 := ime.applyCollocationRerank(cands5, 0); c5 {
		t.Fatalf("短候选被换位")
	}
}
