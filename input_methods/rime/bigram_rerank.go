package rime

import (
	"bufio"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// bigram 重排（hit@3→hit@1 精修）：dll+语法模型已把正确候选排进前三，
// 本层用对话语料的词级 bigram 后继分布，在前三内部做保守换位——
// 只在后继证据显著更强（倍率阈值）时才把 2/3 名提到首位。
// 数据 <data>/zh-bigram.tsv（n\t词\t后词:计数 …，每头词留前 30 后继）。

const bigramTableFileName = "zh-bigram.tsv"

type bigramRerankConfig struct {
	Enabled    bool    `json:"bigram_rerank"`
	Ratio      float64 `json:"bigram_rerank_ratio"` // 2 名超 1 名的倍率阈值
	FloorCount int     `json:"bigram_rerank_floor"` // 后继质量下限，防低频抖动
	// 组字串内部配对重排（d3 混合连打，2026-10-21）：无上文时用候选自身的
	// 切分配对频次给前 3 打分——对的 gold 语料频次碾压书面词展开（是+的
	// 29010 vs 是+时代 0），换位规则与上文版相同（ratio 倍率 + floor 下限）。
	// 与 Enabled（上文版）独立开关，默认关。
	MixedPair  bool    `json:"bigram_rerank_mixed_pair"`
	PairRatio  float64 `json:"bigram_mixed_pair_ratio"`   // 候选配对频次超首名的倍率阈值
	PairFloor  int     `json:"bigram_mixed_pair_floor"`   // 配对频次下限
	PairMaxLen int     `json:"bigram_mixed_pair_max_len"` // 参与打分的候选最大字数
}

func defaultBigramRerankConfig() bigramRerankConfig {
	// 默认关闭：2026-10-17 全栈 A/B（2000 词对）与关闭档差 -0.05（噪声），
	// essay gram + 用户词典已覆盖该信号。配置 bigram_rerank.enabled 可开。
	return bigramRerankConfig{
		Enabled: false, Ratio: 1.6, FloorCount: 30,
		MixedPair: false, PairRatio: 1.3, PairFloor: 50, PairMaxLen: 4,
	}
}

type bigramTable struct {
	mu     sync.RWMutex
	loaded bool
	// head 词 → (后继首字 → 计数质量)
	successorMass map[string]map[string]int
	// head 词 → (完整后继词 → 计数)：组字串内部配对打分用
	pairCounts map[string]map[string]int
}

var sharedBigramTable bigramTable

// loadBigramTable 从 data 目录解析 TSV；文件缺失时返回空表（功能关闭）。
func loadBigramTable(dataDir string) map[string]map[string]int {
	table, _ := loadBigramTableFull(dataDir)
	return table
}

// loadBigramTableFull 返回 (首字摘要表, 全词对频次表)。
func loadBigramTableFull(dataDir string) (map[string]map[string]int, map[string]map[string]int) {
	table := make(map[string]map[string]int)
	pairs := make(map[string]map[string]int)
	if dataDir == "" {
		return table, pairs
	}
	f, err := os.Open(filepath.Join(dataDir, bigramTableFileName))
	if err != nil {
		return table, pairs
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "n\t") {
			continue
		}
		t1 := strings.IndexByte(line, '\t')
		t2 := strings.IndexByte(line[t1+1:], '\t')
		if t1 < 0 || t2 < 0 {
			continue
		}
		w1 := line[t1+1 : t1+1+t2]
		row := table[w1]
		if row == nil {
			row = make(map[string]int, 8)
			table[w1] = row
		}
		prow := pairs[w1]
		if prow == nil {
			prow = make(map[string]int, 8)
			pairs[w1] = prow
		}
		for _, tok := range strings.Fields(line[t1+1+t2+1:]) {
			i := strings.LastIndexByte(tok, ':')
			if i <= 0 {
				continue
			}
			c, err := strconv.Atoi(tok[i+1:])
			if err != nil || c <= 0 {
				continue
			}
			w2 := tok[:i]
			if w2 == "" {
				continue
			}
			first := string([]rune(w2)[0])
			row[first] += c
			prow[w2] += c
		}
	}
	return table, pairs
}

func (ime *IME) bigramTableSnapshot() map[string]map[string]int {
	t, _ := ime.bigramTableSnapshotFull()
	return t
}

func (ime *IME) bigramTableSnapshotFull() (map[string]map[string]int, map[string]map[string]int) {
	sharedBigramTable.mu.RLock()
	if sharedBigramTable.loaded {
		m := sharedBigramTable.successorMass
		p := sharedBigramTable.pairCounts
		sharedBigramTable.mu.RUnlock()
		return m, p
	}
	sharedBigramTable.mu.RUnlock()

	m, p := loadBigramTableFull(ime.sharedDir())
	sharedBigramTable.mu.Lock()
	sharedBigramTable.successorMass = m
	sharedBigramTable.pairCounts = p
	sharedBigramTable.loaded = true
	sharedBigramTable.mu.Unlock()
	return m, p
}

func resetBigramTableForTest() {
	sharedBigramTable.mu.Lock()
	sharedBigramTable.loaded = false
	sharedBigramTable.successorMass = nil
	sharedBigramTable.mu.Unlock()
}

// successorMass 返回 head 词各后继首字的质量。
func successorMass(table map[string]map[string]int, prevTail string) map[string]int {
	if len(table) == 0 || prevTail == "" {
		return nil
	}
	runes := []rune(prevTail)
	for l := 4; l >= 1; l-- {
		if l > len(runes) {
			continue
		}
		if row, ok := table[string(runes[len(runes)-l:])]; ok {
			return row
		}
	}
	return nil
}

// applyBigramRerank 在前三候选内部做保守换位；返回是否调整。
// 只在：上一句 8 秒窗口内、前三齐全、2 名后继质量 > ratio×1 名
// 且自身 > floor 时，把 2 名提到首位。3 名同理但不越过 2 名。
func (ime *IME) applyBigramRerank(candidates []candidateItem, cursor int) ([]candidateItem, int, bool) {
	cfg := ime.contextualConfigSnapshot().BigramRerank
	if !cfg.Enabled || len(candidates) < 2 {
		return candidates, cursor, false
	}
	rec, ok := ime.activeContextualTail()
	if !ok {
		return candidates, cursor, false
	}
	mass := successorMass(ime.bigramTableSnapshot(), rec)
	if mass == nil {
		return candidates, cursor, false
	}

	score := func(item candidateItem) int {
		runes := []rune(item.Text)
		if len(runes) == 0 {
			return 0
		}
		return mass[string(runes[0])]
	}
	firstScore := score(candidates[0])
	// 找前三个里后继质量最高且显著超首名的。
	bestIdx := -1
	for i := 1; i < len(candidates) && i < 3; i++ {
		s := score(candidates[i])
		if s >= cfg.FloorCount && (firstScore == 0 || float64(s) > cfg.Ratio*float64(firstScore)) {
			if bestIdx < 0 || s > score(candidates[bestIdx]) {
				bestIdx = i
			}
		}
	}
	if bestIdx < 0 {
		return candidates, cursor, false
	}
	reordered := append([]candidateItem(nil), candidates...)
	winner := reordered[bestIdx]
	copy(reordered[1:bestIdx+1], reordered[0:bestIdx])
	reordered[0] = winner
	if cursor == bestIdx {
		cursor = 0
	} else if cursor == 0 {
		cursor = bestIdx
	}
	log.Printf("bigram rerank: %q[%d] 超过 %q[%d]，提前 (mass %d vs %d)",
		winner.Text, bestIdx, candidates[0].Text, 0, score(winner), firstScore)
	return reordered, cursor, true
}

// applyBigramRerankFileConfig 从 contextual_lift.json 原文解析重排子配置
// （单独结构以便省略字段走默认）。
func applyBigramRerankFileConfig(cfg *bigramRerankConfig, body []byte) {
	if len(body) == 0 {
		return
	}
	var wrapper struct {
		BigramRerank *struct {
			Enabled    *bool    `json:"enabled"`
			Ratio      *float64 `json:"ratio"`
			FloorCount *int     `json:"floor_count"`
		} `json:"bigram_rerank"`
	}
	if err := json.Unmarshal(body, &wrapper); err != nil || wrapper.BigramRerank == nil {
		return
	}
	if wrapper.BigramRerank.Enabled != nil {
		cfg.Enabled = *wrapper.BigramRerank.Enabled
	}
	if wrapper.BigramRerank.Ratio != nil && *wrapper.BigramRerank.Ratio > 1 {
		cfg.Ratio = *wrapper.BigramRerank.Ratio
	}
	if wrapper.BigramRerank.FloorCount != nil && *wrapper.BigramRerank.FloorCount > 0 {
		cfg.FloorCount = *wrapper.BigramRerank.FloorCount
	}
}

// pairScore 用完整词对频次给候选打分：对所有切分点取
// max pairCounts[前段][后段]。候选 2..PairMaxLen 字才参与（词对候选；
// 长句组词切分噪声大，不碰）。
func pairScore(pairs map[string]map[string]int, text string, maxLen int) int {
	runes := []rune(text)
	if len(runes) < 2 || len(runes) > maxLen {
		return 0
	}
	best := 0
	for l := 1; l < len(runes); l++ {
		head := string(runes[:l])
		row, ok := pairs[head]
		if !ok {
			continue
		}
		if c := row[string(runes[l:])]; c > best {
			best = c
		}
	}
	return best
}

// applyMixedPairRerank 组字串内部配对重排（无上文版）：前 3 候选按词对
// 频次打分，显著强者（≥PairRatio×首名 且 ≥PairFloor）提到首位。
// 只在前 3 都不超 PairMaxLen 字时参与打分竞争，换位规则与上文版一致。
func (ime *IME) applyMixedPairRerank(candidates []candidateItem, cursor int) ([]candidateItem, int, bool) {
	cfg := ime.contextualConfigSnapshot().BigramRerank
	if !cfg.MixedPair || len(candidates) < 2 {
		return candidates, cursor, false
	}
	_, pairs := ime.bigramTableSnapshotFull()
	if len(pairs) == 0 {
		return candidates, cursor, false
	}
	maxLen := cfg.PairMaxLen
	if maxLen < 2 {
		maxLen = 2
	}
	scores := make([]int, 0, 3)
	for i := 0; i < len(candidates) && i < 3; i++ {
		scores = append(scores, pairScore(pairs, candidates[i].Text, maxLen))
	}
	first := scores[0]
	bestIdx := -1
	for i := 1; i < len(scores); i++ {
		s := scores[i]
		if s >= cfg.PairFloor && (first == 0 || float64(s) >= cfg.PairRatio*float64(first)) {
			if bestIdx < 0 || s > scores[bestIdx] {
				bestIdx = i
			}
		}
	}
	if bestIdx < 0 {
		return candidates, cursor, false
	}
	reordered := append([]candidateItem(nil), candidates...)
	winner := reordered[bestIdx]
	copy(reordered[1:bestIdx+1], reordered[0:bestIdx])
	reordered[0] = winner
	if cursor == bestIdx {
		cursor = 0
	}
	debugLogf("mixed pair rerank: %q(%d) 超过 %q(%d) 提前", winner.Text, scores[bestIdx], candidates[0].Text, first)
	return reordered, cursor, true
}
