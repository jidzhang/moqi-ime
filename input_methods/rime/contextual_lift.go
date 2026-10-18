package rime

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 短窗连贯：上一句刚上屏后的短时间窗内，下一句首字的同音候选按接续关系
// 稳定重排（如"你好"+ma 首选"吗"而不是"妈"）。坏例集中在前一句刚结束时
// 首字纯同音翻转的场景，故只作用于小粒度提升，且默认权重保守。
// 数据为内置接续表（尾词 → 高频接续字），用户可用 JSON 覆盖扩充。
// 全程本地计算，无出站流量。

const contextualConfigFileName = "contextual_lift.json"

const (
	defaultContextualWindowSeconds = 8
	defaultContextualMaxTailRunes  = 4
	maxContextualRecentCommits     = 2
)

type contextualCommitRecord struct {
	text string
	at   time.Time
}

type contextualConfig struct {
	Enabled       bool
	WindowSeconds int
	MaxTailRunes  int
	Continuations map[string][]string
	BigramRerank  bigramRerankConfig `json:"bigram_rerank"`
}

type contextualFileConfig struct {
	Enabled            *bool               `json:"enabled,omitempty"`
	WindowSeconds      *int                `json:"window_seconds,omitempty"`
	MaxTailRunes       *int                `json:"max_tail_runes,omitempty"`
	ExtraContinuations map[string][]string `json:"extra_continuations,omitempty"`
}

// 内置接续表：只收录短窗内高置信度的接续，宁缺毋滥。
// 匹配时按最长尾词优先；列表顺序即优先级。
var defaultContextualContinuations = map[string][]string{
	"你好":   {"吗"},
	"谢谢":   {"你"},
	"辛苦":   {"了"},
	"请问":   {},
	"多":    {"谢"},
	"很":    {"好"},
	"不":    {"错"},
	"没":    {"事", "有"},
	"我":    {"们"},
	"你":    {"好"},
	"他":    {"们"},
	"她":    {"们"},
	"在":    {"哪", "这"},
	"哪":    {"里"},
	"是":    {"的", "吗"},
	"不是":   {"吗"},
	"对":    {"的", "吗"},
	"可以":   {"吗"},
	"好":    {"的", "吗"},
	"真":    {"的", "好"},
	"太":    {"好", "多"},
	"就":    {"是"},
	"都":    {"是"},
	"也":    {"是", "好"},
	"还":    {"好", "是"},
	"什么":   {"呢"},
	"为什么":  {"呢"},
	"怎么":   {"样", "办"},
	"吗":    {"呢"},
	"了":    {"吗", "吧"},
}

func defaultContextualConfig() contextualConfig {
	continuations := make(map[string][]string, len(defaultContextualContinuations))
	for tail, conts := range defaultContextualContinuations {
		continuations[tail] = append([]string(nil), conts...)
	}
	cfg := contextualConfig{
		Enabled:       true,
		WindowSeconds: defaultContextualWindowSeconds,
		MaxTailRunes:  defaultContextualMaxTailRunes,
		Continuations: continuations,
		BigramRerank:  defaultBigramRerankConfig(),
	}
	return cfg
}

func userContextualConfigPath() string {
	root := moqiAppDataDir()
	if root == "" {
		return ""
	}
	return filepath.Join(root, contextualConfigFileName)
}

func loadContextualConfig() contextualConfig {
	cfg := defaultContextualConfig()
	path := userContextualConfigPath()
	if path == "" {
		return cfg
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return cfg
	}
	var fileCfg contextualFileConfig
	if err := json.Unmarshal(body, &fileCfg); err != nil {
		log.Printf("解析短窗连贯配置失败，使用默认配置: %v", err)
		return cfg
	}
	if fileCfg.Enabled != nil {
		cfg.Enabled = *fileCfg.Enabled
	}
	if fileCfg.WindowSeconds != nil && *fileCfg.WindowSeconds >= 0 {
		cfg.WindowSeconds = *fileCfg.WindowSeconds
	}
	if fileCfg.MaxTailRunes != nil && *fileCfg.MaxTailRunes > 0 {
		cfg.MaxTailRunes = *fileCfg.MaxTailRunes
	}
	for tail, conts := range fileCfg.ExtraContinuations {
		tail = strings.TrimSpace(tail)
		if tail == "" || len(conts) == 0 {
			continue
		}
		cfg.Continuations[tail] = append([]string(nil), conts...)
	}
	applyBigramRerankFileConfig(&cfg.BigramRerank, body)
	return cfg
}

func (ime *IME) contextualConfigSnapshot() contextualConfig {
	if ime.contextualCfg == nil {
		cfg := loadContextualConfig()
		ime.contextualCfg = &cfg
	}
	return *ime.contextualCfg
}

func (ime *IME) reloadContextualConfig() {
	cfg := loadContextualConfig()
	ime.contextualCfg = &cfg
}

// pushContextualCommit 在真实上屏时记录句子，供下一句首字查询；
// 只保留最近一两句，且在此顺带清掉过期记录。
func (ime *IME) pushContextualCommit(commit string) {
	commit = strings.TrimSpace(commit)
	if commit == "" {
		return
	}
	cfg := ime.contextualConfigSnapshot()
	now := time.Now()
	records := ime.contextualRecentCommits[:0]
	for _, rec := range ime.contextualRecentCommits {
		if now.Sub(rec.at) < time.Duration(cfg.WindowSeconds)*time.Second {
			records = append(records, rec)
		}
	}
	records = append(records, contextualCommitRecord{text: commit, at: now})
	if len(records) > maxContextualRecentCommits {
		records = records[len(records)-maxContextualRecentCommits:]
	}
	ime.contextualRecentCommits = records
}

// activeContextualTail 返回未过期最近一句的 ≤maxTailRunes 字后缀（最长优先的
// 长度由调用方遍历），无可用记录时返回 false。
func (ime *IME) activeContextualTail() (string, bool) {
	if len(ime.contextualRecentCommits) == 0 {
		return "", false
	}
	cfg := ime.contextualConfigSnapshot()
	rec := ime.contextualRecentCommits[len(ime.contextualRecentCommits)-1]
	if time.Since(rec.at) >= time.Duration(cfg.WindowSeconds)*time.Second {
		return "", false
	}
	return rec.text, true
}

// applyContextualLift 对当前候选做接续提升：取上一句尾词命中的接续字，
// 把首字命中的候选稳定前移（相对顺序不变），并同步重映射候选光标。
// 返回重排后的列表与是否发生了调整。
func (ime *IME) applyContextualLift(candidates []candidateItem, cursor int) ([]candidateItem, int, bool) {
	if !ime.contextualConfigSnapshot().Enabled || len(candidates) == 0 {
		return candidates, cursor, false
	}
	commitText, ok := ime.activeContextualTail()
	if !ok {
		return candidates, cursor, false
	}

	cfg := ime.contextualConfigSnapshot()
	runes := []rune(commitText)
	if cfg.MaxTailRunes > len(runes) {
		cfg.MaxTailRunes = len(runes)
	}
	for l := cfg.MaxTailRunes; l >= 1; l-- {
		tail := string(runes[len(runes)-l:])
		conts, ok := cfg.Continuations[tail]
		if !ok {
			continue
		}
		for _, cont := range conts {
			want := []rune(cont)
			if len(want) == 0 {
				continue
			}
			matchedCount := 0
			for _, item := range candidates {
				itemRunes := []rune(item.Text)
				if len(itemRunes) > 0 && itemRunes[0] == want[0] {
					matchedCount++
				}
			}
			if matchedCount == 0 || matchedCount == len(candidates) {
				// 无命中，或命中的候选本来就全在最前，换下一个接续字。
				continue
			}
			reordered := make([]candidateItem, 0, len(candidates))
			matched := make([]candidateItem, 0, matchedCount)
			for _, item := range candidates {
				itemRunes := []rune(item.Text)
				if len(itemRunes) > 0 && itemRunes[0] == want[0] {
					matched = append(matched, item)
				} else {
					reordered = append(reordered, item)
				}
			}
			// 接续候选提升到最前，其余保持原相对顺序。
			reordered = append(matched, reordered...)
			// 光标跟随原指向的候选文本。
			if cursor >= 0 && cursor < len(candidates) {
				oldText := candidates[cursor].Text
				for i, item := range reordered {
					if item.Text == oldText {
						return reordered, i, true
					}
				}
			}
			return reordered, cursor, true
		}
	}
	return candidates, cursor, false
}
