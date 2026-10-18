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
	Enabled     bool    `json:"bigram_rerank"`
	Ratio       float64 `json:"bigram_rerank_ratio"` // 2 名超 1 名的倍率阈值
	FloorCount  int     `json:"bigram_rerank_floor"` // 后继质量下限，防低频抖动
}

func defaultBigramRerankConfig() bigramRerankConfig {
	// 默认关闭：2026-10-17 全栈 A/B（2000 词对）与关闭档差 -0.05（噪声），
// essay gram + 用户词典已覆盖该信号。配置 bigram_rerank.enabled 可开。
return bigramRerankConfig{Enabled: false, Ratio: 1.6, FloorCount: 30}
}

type bigramTable struct {
	mu     sync.RWMutex
	loaded bool
	// head 词 → (后继首字 → 计数质量)
	successorMass map[string]map[string]int
}

var sharedBigramTable bigramTable

// loadBigramTable 从 data 目录解析 TSV；文件缺失时返回空表（功能关闭）。
func loadBigramTable(dataDir string) map[string]map[string]int {
	table := make(map[string]map[string]int)
	if dataDir == "" {
		return table
	}
	f, err := os.Open(filepath.Join(dataDir, bigramTableFileName))
	if err != nil {
		return table
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
		}
	}
	return table
}

func (ime *IME) bigramTableSnapshot() map[string]map[string]int {
	sharedBigramTable.mu.RLock()
	if sharedBigramTable.loaded {
		t := sharedBigramTable.successorMass
		sharedBigramTable.mu.RUnlock()
		return t
	}
	sharedBigramTable.mu.RUnlock()

	t := loadBigramTable(ime.sharedDir())
	sharedBigramTable.mu.Lock()
	sharedBigramTable.successorMass = t
	sharedBigramTable.loaded = true
	sharedBigramTable.mu.Unlock()
	return t
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
//（单独结构以便省略字段走默认）。
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
