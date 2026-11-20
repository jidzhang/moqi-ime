package rime

import (
	"bufio"
	"math"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
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
	// 整句搭配证据通道 v3（2026-11-20）：判别量=PMI 强搭配对数
	// ln(count·N/(mass[a]·mass[b])) ≥ collocPmi 计一对凝固搭配。v1 有据
	// 对数/v2 等长闸被泛用字链击穿（不知道→我 PMI 仅 0.90）；v3 把泛用链
	// 全部分离，剩余错案均为双重凝固搭配真歧义（用户拍板可接受）。
	// 默认 2.0 开启；<0 关闭。
	CollocPmi float64 `json:"bigram_colloc_pmi"`
	// PairWordRatio：搭配证据必须 ≥ 首名"整词证据"×该倍率才换位。
	// 判别原理：可以=整词 84137（搭配 0）vs 抗疫=整词 0（搭配 823）——
	// 只看搭配会把整词误判为"无据"而挤掉（用户实报 k yi：按 2 上屏抗疫）。
	// 1.0 = 搭配证据须压过首名整词证据。
	PairWordRatio float64 `json:"bigram_mixed_pair_word_ratio"`
}

func defaultBigramRerankConfig() bigramRerankConfig {
	// MixedPair 默认开（2026-10-22 定档）：仅混合组字（全拼+首字母尾巴，
	// 音节切分门槛把关）、只替换"词表无据"的首名（PairRatio 999 语义）、
	// 频次 ≥300。d3 dedup 64.3→82.3（含学习治理），守卫全达标（见 report
	// 2026-10-22）。上方 Enabled 是"上一句上下文版"重排，维持默认关。
	return bigramRerankConfig{
		Enabled: false, Ratio: 1.6, FloorCount: 30,
		MixedPair: true, PairRatio: 999, PairFloor: 300, PairMaxLen: 4,
		PairWordRatio: 1.0,
		// 2.0 = 数据实测一刀切（正解搭配 3.66~5.75，病句 ≤1.59）。
		CollocPmi: 2.0,
	}
}

type bigramTable struct {
	mu     sync.RWMutex
	loaded bool
	// head 词 → (后继首字 → 计数质量)
	successorMass map[string]map[string]int
	// head 词 → (完整后继词 → 计数)：组字串内部配对打分用
	pairCounts map[string]map[string]int
	// 词 → 语料整词质量（作为 head 的行和 + 作为后继的出现数）。
	// 用于判别"真实整词"与"两字拼装"：可以=84137（整词），是的=0
	// （靠 是+的 搭配 29010 成立）。重排闸门据此避免误伤整词。
	wordMass map[string]int
	// 全表 token 总量（PMI 归一化 N）。
	totalTokens int64
}

var sharedBigramTable bigramTable

// loadBigramTableFull 顺手记下全表 token 量（colloc PMI 归一化用）。
var sharedBigramTableTotalTokens int64

// loadBigramTable 从 data 目录解析 TSV；文件缺失时返回空表（功能关闭）。
func loadBigramTable(dataDir string) map[string]map[string]int {
	table, _, _ := loadBigramTableFull(dataDir)
	return table
}

// loadBigramTableFull 返回 (首字摘要表, 全词对频次表, 整词质量表)。
func loadBigramTableFull(dataDir string) (map[string]map[string]int, map[string]map[string]int, map[string]int) {
	table := make(map[string]map[string]int)
	pairs := make(map[string]map[string]int)
	wordMass := make(map[string]int)
	var totalTokens int64
	if dataDir == "" {
		return table, pairs, wordMass
	}
	f, err := os.Open(filepath.Join(dataDir, bigramTableFileName))
	if err != nil {
		return table, pairs, wordMass
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
		rowTotal := 0
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
			wordMass[w2] += c
			rowTotal += c
		}
		wordMass[w1] += rowTotal
		totalTokens += int64(rowTotal)
	}
	sharedBigramTableTotalTokens = totalTokens
	return table, pairs, wordMass
}

func (ime *IME) bigramTableSnapshot() map[string]map[string]int {
	t, _, _ := ime.bigramTableSnapshotFull()
	return t
}

func (ime *IME) bigramTableSnapshotFull() (map[string]map[string]int, map[string]map[string]int, map[string]int) {
	sharedBigramTable.mu.RLock()
	if sharedBigramTable.loaded {
		m := sharedBigramTable.successorMass
		p := sharedBigramTable.pairCounts
		w := sharedBigramTable.wordMass
		sharedBigramTable.mu.RUnlock()
		return m, p, w
	}
	sharedBigramTable.mu.RUnlock()

	m, p, w := loadBigramTableFull(ime.sharedDir())
	sharedBigramTable.mu.Lock()
	sharedBigramTable.successorMass = m
	sharedBigramTable.pairCounts = p
	sharedBigramTable.wordMass = w
	sharedBigramTable.loaded = true
	sharedBigramTable.mu.Unlock()
	return m, p, w
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
			Enabled           *bool    `json:"enabled"`
			Ratio             *float64 `json:"ratio"`
			FloorCount        *int     `json:"floor_count"`
			MixedPair         *bool    `json:"mixed_pair"`
			PairRatio         *float64 `json:"pair_ratio"`
			PairFloor         *int     `json:"pair_floor"`
			PairMaxLen        *int     `json:"pair_max_len"`
			PairWordRatio     *float64 `json:"pair_word_ratio"`
			CollocPmi         *float64 `json:"colloc_pmi"`
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
	if wrapper.BigramRerank.MixedPair != nil {
		cfg.MixedPair = *wrapper.BigramRerank.MixedPair
	}
	if wrapper.BigramRerank.PairRatio != nil && *wrapper.BigramRerank.PairRatio > 1 {
		cfg.PairRatio = *wrapper.BigramRerank.PairRatio
	}
	if wrapper.BigramRerank.PairFloor != nil && *wrapper.BigramRerank.PairFloor > 0 {
		cfg.PairFloor = *wrapper.BigramRerank.PairFloor
	}
	if wrapper.BigramRerank.PairMaxLen != nil && *wrapper.BigramRerank.PairMaxLen >= 2 {
		cfg.PairMaxLen = *wrapper.BigramRerank.PairMaxLen
	}
	if wrapper.BigramRerank.PairWordRatio != nil && *wrapper.BigramRerank.PairWordRatio >= 0 {
		cfg.PairWordRatio = *wrapper.BigramRerank.PairWordRatio
	}
	if wrapper.BigramRerank.CollocPmi != nil && *wrapper.BigramRerank.CollocPmi >= 0 {
		cfg.CollocPmi = *wrapper.BigramRerank.CollocPmi
	}
}

// pinyinSyllableSet 标准无调拼音音节表（含 lv/nv/lve/nve 变体）。
// 用于判定组字串是否"整串可切分为完整音节"——全拼组字（d1/d2 场景）
// 不做配对重排，只有带首字母尾巴的混合组字（d3 场景）才启用。
var pinyinSyllableSet = map[string]bool{
	"a":      true,
	"ai":     true,
	"an":     true,
	"ang":    true,
	"ao":     true,
	"e":      true,
	"ei":     true,
	"en":     true,
	"eng":    true,
	"er":     true,
	"o":      true,
	"ou":     true,
	"ba":     true,
	"bai":    true,
	"ban":    true,
	"bang":   true,
	"bao":    true,
	"bei":    true,
	"ben":    true,
	"beng":   true,
	"bi":     true,
	"bian":   true,
	"biao":   true,
	"bie":    true,
	"bin":    true,
	"bing":   true,
	"bo":     true,
	"bu":     true,
	"ca":     true,
	"cai":    true,
	"can":    true,
	"cang":   true,
	"cao":    true,
	"ce":     true,
	"cen":    true,
	"ceng":   true,
	"cha":    true,
	"chai":   true,
	"chan":   true,
	"chang":  true,
	"chao":   true,
	"che":    true,
	"chen":   true,
	"cheng":  true,
	"chi":    true,
	"chong":  true,
	"chou":   true,
	"chu":    true,
	"chua":   true,
	"chuai":  true,
	"chuan":  true,
	"chuang": true,
	"chui":   true,
	"chun":   true,
	"chuo":   true,
	"ci":     true,
	"cong":   true,
	"cou":    true,
	"cu":     true,
	"cuan":   true,
	"cui":    true,
	"cun":    true,
	"cuo":    true,
	"da":     true,
	"dai":    true,
	"dan":    true,
	"dang":   true,
	"dao":    true,
	"de":     true,
	"dei":    true,
	"den":    true,
	"deng":   true,
	"di":     true,
	"dia":    true,
	"dian":   true,
	"diao":   true,
	"die":    true,
	"ding":   true,
	"diu":    true,
	"dong":   true,
	"dou":    true,
	"du":     true,
	"duan":   true,
	"dui":    true,
	"dun":    true,
	"duo":    true,
	"fa":     true,
	"fan":    true,
	"fang":   true,
	"fei":    true,
	"fen":    true,
	"feng":   true,
	"fo":     true,
	"fou":    true,
	"fu":     true,
	"ga":     true,
	"gai":    true,
	"gan":    true,
	"gang":   true,
	"gao":    true,
	"ge":     true,
	"gei":    true,
	"gen":    true,
	"geng":   true,
	"gong":   true,
	"gou":    true,
	"gu":     true,
	"gua":    true,
	"guai":   true,
	"guan":   true,
	"guang":  true,
	"gui":    true,
	"gun":    true,
	"guo":    true,
	"ha":     true,
	"hai":    true,
	"han":    true,
	"hang":   true,
	"hao":    true,
	"he":     true,
	"hei":    true,
	"hen":    true,
	"heng":   true,
	"hong":   true,
	"hou":    true,
	"hu":     true,
	"hua":    true,
	"huai":   true,
	"huan":   true,
	"huang":  true,
	"hui":    true,
	"hun":    true,
	"huo":    true,
	"ji":     true,
	"jia":    true,
	"jian":   true,
	"jiang":  true,
	"jiao":   true,
	"jie":    true,
	"jin":    true,
	"jing":   true,
	"jiong":  true,
	"jiu":    true,
	"ju":     true,
	"juan":   true,
	"jue":    true,
	"jun":    true,
	"ka":     true,
	"kai":    true,
	"kan":    true,
	"kang":   true,
	"kao":    true,
	"ke":     true,
	"kei":    true,
	"ken":    true,
	"keng":   true,
	"kong":   true,
	"kou":    true,
	"ku":     true,
	"kua":    true,
	"kuai":   true,
	"kuan":   true,
	"kuang":  true,
	"kui":    true,
	"kun":    true,
	"kuo":    true,
	"la":     true,
	"lai":    true,
	"lan":    true,
	"lang":   true,
	"lao":    true,
	"le":     true,
	"lei":    true,
	"leng":   true,
	"li":     true,
	"lia":    true,
	"lian":   true,
	"liang":  true,
	"liao":   true,
	"lie":    true,
	"lin":    true,
	"ling":   true,
	"liu":    true,
	"lo":     true,
	"long":   true,
	"lou":    true,
	"lu":     true,
	"luan":   true,
	"lun":    true,
	"luo":    true,
	"lv":     true,
	"lve":    true,
	"ma":     true,
	"mai":    true,
	"man":    true,
	"mang":   true,
	"mao":    true,
	"me":     true,
	"mei":    true,
	"men":    true,
	"meng":   true,
	"mi":     true,
	"mian":   true,
	"miao":   true,
	"mie":    true,
	"min":    true,
	"ming":   true,
	"miu":    true,
	"mo":     true,
	"mou":    true,
	"mu":     true,
	"na":     true,
	"nai":    true,
	"nanang": true,
	"nao":    true,
	"ne":     true,
	"nei":    true,
	"nen":    true,
	"neng":   true,
	"ni":     true,
	"nian":   true,
	"niang":  true,
	"niao":   true,
	"nie":    true,
	"nin":    true,
	"ning":   true,
	"niu":    true,
	"nong":   true,
	"nou":    true,
	"nu":     true,
	"nuan":   true,
	"nuo":    true,
	"nv":     true,
	"nve":    true,
	"pa":     true,
	"pai":    true,
	"pan":    true,
	"pang":   true,
	"pao":    true,
	"pei":    true,
	"pen":    true,
	"peng":   true,
	"pi":     true,
	"pian":   true,
	"piao":   true,
	"pie":    true,
	"pin":    true,
	"ping":   true,
	"po":     true,
	"pou":    true,
	"pu":     true,
	"qi":     true,
	"qian":   true,
	"qiang":  true,
	"qiao":   true,
	"qie":    true,
	"qin":    true,
	"qing":   true,
	"qiong":  true,
	"qiu":    true,
	"qu":     true,
	"quan":   true,
	"que":    true,
	"qun":    true,
	"ran":    true,
	"rang":   true,
	"rao":    true,
	"re":     true,
	"ren":    true,
	"reng":   true,
	"ri":     true,
	"rong":   true,
	"rou":    true,
	"ru":     true,
	"rua":    true,
	"ruan":   true,
	"rui":    true,
	"run":    true,
	"ruo":    true,
	"sa":     true,
	"sai":    true,
	"san":    true,
	"sang":   true,
	"sao":    true,
	"se":     true,
	"sen":    true,
	"seng":   true,
	"sha":    true,
	"shai":   true,
	"shan":   true,
	"shang":  true,
	"shao":   true,
	"she":    true,
	"shei":   true,
	"shen":   true,
	"sheng":  true,
	"shi":    true,
	"shou":   true,
	"shu":    true,
	"shua":   true,
	"shuai":  true,
	"shuan":  true,
	"shuang": true,
	"shui":   true,
	"shun":   true,
	"shuo":   true,
	"si":     true,
	"song":   true,
	"sou":    true,
	"su":     true,
	"suan":   true,
	"sui":    true,
	"sun":    true,
	"suo":    true,
	"ta":     true,
	"tai":    true,
	"tan":    true,
	"tang":   true,
	"tao":    true,
	"te":     true,
	"teng":   true,
	"ti":     true,
	"tian":   true,
	"tiao":   true,
	"tie":    true,
	"ting":   true,
	"tong":   true,
	"tou":    true,
	"tu":     true,
	"tuan":   true,
	"tui":    true,
	"tun":    true,
	"tuo":    true,
	"wa":     true,
	"wai":    true,
	"wan":    true,
	"wang":   true,
	"wei":    true,
	"wen":    true,
	"weng":   true,
	"wo":     true,
	"wu":     true,
	"xi":     true,
	"xia":    true,
	"xian":   true,
	"xiang":  true,
	"xiao":   true,
	"xie":    true,
	"xin":    true,
	"xing":   true,
	"xiong":  true,
	"xiu":    true,
	"xu":     true,
	"xuan":   true,
	"xue":    true,
	"xun":    true,
	"ya":     true,
	"yan":    true,
	"yang":   true,
	"yao":    true,
	"ye":     true,
	"yi":     true,
	"yin":    true,
	"ying":   true,
	"yo":     true,
	"yong":   true,
	"you":    true,
	"yu":     true,
	"yuan":   true,
	"yue":    true,
	"yun":    true,
	"za":     true,
	"zai":    true,
	"zan":    true,
	"zang":   true,
	"zao":    true,
	"ze":     true,
	"zei":    true,
	"zen":    true,
	"zeng":   true,
	"zha":    true,
	"zhai":   true,
	"zhan":   true,
	"zhang":  true,
	"zhao":   true,
	"zhe":    true,
	"zhen":   true,
	"zheng":  true,
	"zhi":    true,
	"zhong":  true,
	"zhou":   true,
	"zhu":    true,
	"zhua":   true,
	"zhuai":  true,
	"zhuan":  true,
	"zhuang": true,
	"zhui":   true,
	"zhun":   true,
	"zhuo":   true,
	"zi":     true,
	"zong":   true,
	"zou":    true,
	"zu":     true,
	"zuan":   true,
	"zui":    true,
	"zun":    true,
	"zuo":    true,
}

// fullyPinyinSegmentable 报告 s 是否为全拼组字：按空白分段（d1/d2 的
// 输入用空格分隔音节，如 "wo de ren"），每段都必须能完全切分为拼音
// 音节（DP，音节最长 6）。任一段切不开（如 d3 的首字母尾巴 "shid"）
// 即混合组字。
func fullyPinyinSegmentable(s string) bool {
	toks := strings.Fields(s)
	if len(toks) == 0 {
		return false
	}
	for _, tok := range toks {
		if !segmentable(tok) {
			return false
		}
	}
	return true
}

func segmentable(tok string) bool {
	n := len(tok)
	if n == 0 {
		return false
	}
	dp := make([]bool, n+1)
	dp[0] = true
	for j := 1; j <= n; j++ {
		for i := j - 1; i >= 0 && j-i <= 6; i-- {
			if dp[i] && pinyinSyllableSet[tok[i:j]] {
				dp[j] = true
				break
			}
		}
	}
	return dp[n]
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
func (ime *IME) applyMixedPairRerank(candidates []candidateItem, cursor int, composition string) ([]candidateItem, int, bool) {
	cfg := ime.contextualConfigSnapshot().BigramRerank
	// 只作用于混合组字（全拼前缀+首字母尾巴）：整串可完整切分为音节的
	// 全拼组字（两词/三词连打）引擎排序已最优，词对先验越权会砸长句
	// 候选（d2 实测 80.3→30.7 的教训）。
	if !cfg.MixedPair || len(candidates) < 2 || fullyPinyinSegmentable(composition) {
		return candidates, cursor, false
	}
	_, pairs, wordMass := ime.bigramTableSnapshotFull()
	if len(pairs) == 0 {
		return candidates, cursor, false
	}
	maxLen := cfg.PairMaxLen
	if maxLen < 2 {
		maxLen = 2
	}
	// v2：全列表打分（不只前 3）——d3 的 gold 常在第 4~9 位（显微镜：
	// shid 的「是的」#9），top-3 窗口捞不到。换位仍保守：频次 ≥floor 且
	// ≥ratio×首名分才提到首位，其余顺位下移。
	// 首名的"整词证据"：作为完整词在语料中的质量。高说明它是真实高频词
	//（可以 84137），即便两字搭配为 0 也不该被挤掉；低说明它是拼装词
	//（是的 整词 0、靠 是+的 29010 成立），此时搭配证据才是有效信号。
	stripMarks := func(s string) string {
		if i := strings.IndexByte(s, ' '); i > 0 {
			s = s[:i]
		}
		return strings.TrimSpace(s)
	}
	first := pairScore(pairs, candidates[0].Text, maxLen)
	// 覆盖闸门（2026-11-18 混拼首候选事故）：配对证据只允许把「不比首名
	// 短」的候选提到第一。首名是引擎排的完整覆盖候选（如 zaiwokl 的
	// 在我看来），更短的候选（在我）是丢弃简拼尾巴的前缀候选——字级
	// 高频对（在|我 9708）轻易过 floor，把它顶上去就是把残缺候选当首选。
	// 原设计场景（shid：时代/是的/使得 同长度）不受影响。
	firstRunes := utf8.RuneCountInString(candidates[0].Text)
	// 每位的"整词证据"前缀最大值：候选 i 想被提前，它的搭配证据必须压过
	// 排在它上面的所有候选里最强的整词证据。
	// 教训（2026-10-22 用户实报 k yi）：只跟首名比不够——首名可能是组句
	// 垃圾「看一∞」（整词 0），而真正的整词「可以」（整词 19516）排在第 3，
	// 结果垃圾搭配「抗疫」被提上来、按 2 还选错。
	wordMassAbove := 0
	bestIdx := -1
	bestScore := 0
	for i := 1; i < len(candidates); i++ {
		if utf8.RuneCountInString(candidates[i].Text) < firstRunes {
			continue
		}
		// 候选 i 上方是 candidates[0..i-1]，含首名——首名的整词证据必须参与
		// 比较（2026-10-23 用户实报 k yi：引擎序 [可以#1, 抗疫#2]，抗疫越位时
		// 旧代码 if i>1 跳过首名，wordMassAbove=0，闸门失明 → 抗疫提到首位）。
		if m := wordMass[stripMarks(candidates[i-1].Text)]; m > wordMassAbove {
			wordMassAbove = m
		}
		s := pairScore(pairs, candidates[i].Text, maxLen)
		if s < cfg.PairFloor {
			continue
		}
		// 通道闸门：搭配证据须过 floor，且明显强于首名的搭配。
		if first != 0 && float64(s) < cfg.PairRatio*float64(first) {
			continue
		}
		// 闸门：搭配证据必须压过上方所有候选的整词证据（含首名，见上）。
		if float64(s) < cfg.PairWordRatio*float64(wordMassAbove) {
			continue
		}
		if s > bestScore {
			bestScore = s
			bestIdx = i
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
	// 记录显示首选的引擎位次，供空格提交对齐（见 processKey）。
	ime.mixedPairSwapIndex = bestIdx
	debugLogf("mixed pair rerank: compo=%q %q(%d) 越过 %q(%d) 提前至#1（原#%d）", composition, winner.Text, bestScore, candidates[0].Text, first, bestIdx+1)
	return reordered, cursor, true
}


// collocScore 整句候选的搭配证据 v3：按 bigram 词表穷举切分，统计相邻词对
// 里 PMI = ln(count·N/(mass[a]·mass[b])) ≥ pmiMin 且 count ≥ 3 的"凝固
// 搭配"对数与其中最小 PMI。切不开或只有一段 → (0, 0)。与 C++ 镜像。
func collocScore(pairs map[string]map[string]int, wordMass map[string]int,
	totalTokens int64, pmiMin float64, text string) (cnt int, minPmi float64) {
	runes := []rune(text)
	n := len(runes)
	if n < 2 || totalTokens <= 0 {
		return 0, 0
	}
	const maxWord = 8
	const inf = 1e18
	pmiOf := func(a, b string) float64 {
		row, ok := pairs[a]
		if !ok {
			return -inf
		}
		c := row[b]
		// 频次地板 3：PMI 再高、只出现 1~2 次的偶发组合不进判决
		//（两案最小真实频次正是 3：务必→解决）。
		if c < 3 {
			return -inf
		}
		ma, mb := 1.0, 1.0
		if m, ok := wordMass[a]; ok {
			ma = float64(m)
		}
		if m, ok := wordMass[b]; ok {
			mb = float64(m)
		}
		return math.Log(float64(c) * float64(totalTokens) / (ma * mb))
	}
	type node struct {
		pos      int
		lastWord string
		cnt      int
		mn       float64
	}
	queue := []node{{0, "", 0, inf}}
	for len(queue) > 0 {
		nd := queue[0]
		queue = queue[1:]
		if nd.pos == n {
			if nd.lastWord != "" &&
				(nd.cnt > cnt || (nd.cnt == cnt && nd.mn > minPmi)) {
				cnt, minPmi = nd.cnt, nd.mn
			}
			continue
		}
		for l := 1; l <= maxWord && nd.pos+l <= n; l++ {
			w := string(runes[nd.pos : nd.pos+l])
			if _, ok := pairs[w]; !ok {
				continue
			}
			c2, m2 := nd.cnt, nd.mn
			if nd.lastWord != "" {
				if p := pmiOf(nd.lastWord, w); p >= pmiMin {
					c2++
					if p < m2 {
						m2 = p
					}
				}
			}
			queue = append(queue, node{nd.pos + l, w, c2, m2})
		}
	}
	if cnt == 0 {
		minPmi = 0
	}
	return cnt, minPmi
}

// applyCollocationRerank 整句搭配证据换位 v3（PMI 判别，与 C++ 镜像）。
// 闸门：≥6 字长候选 / 严格等长 / 强搭配对数严格占优 / 只提到首位。
func (ime *IME) applyCollocationRerank(candidates []candidateItem, cursor int) ([]candidateItem, int, bool) {
	cfg := ime.contextualConfigSnapshot().BigramRerank
	if cfg.CollocPmi < 0 || len(candidates) < 2 {
		return candidates, cursor, false
	}
	// 只对 ≥3 词（≥6 字）的长候选生效（v1 守卫实炸：短候选越权换位）。
	if utf8.RuneCountInString(candidates[0].Text) < 6 {
		return candidates, cursor, false
	}
	_, pairs, wordMass := ime.bigramTableSnapshotFull()
	if len(pairs) == 0 || sharedBigramTableTotalTokens <= 0 {
		return candidates, cursor, false
	}
	stripMarks := func(s string) string {
		if i := strings.IndexByte(s, ' '); i > 0 {
			s = s[:i]
		}
		return strings.TrimSpace(s)
	}
	firstCnt, _ := collocScore(pairs, wordMass, sharedBigramTableTotalTokens,
		cfg.CollocPmi, stripMarks(candidates[0].Text))
	firstRunes := utf8.RuneCountInString(candidates[0].Text)
	bestIdx, bestCnt := -1, 0
	bestMn := 0.0
	for i := 1; i < len(candidates); i++ {
		// 严格等长（v2 实炸：对数优势偏向更长续词句"最重要的一点在于"）。
		if utf8.RuneCountInString(candidates[i].Text) != firstRunes {
			continue
		}
		cnt, mn := collocScore(pairs, wordMass, sharedBigramTableTotalTokens,
			cfg.CollocPmi, stripMarks(candidates[i].Text))
		if cnt <= firstCnt {
			continue
		}
		if cnt > bestCnt || (cnt == bestCnt && mn > bestMn) {
			bestIdx, bestCnt, bestMn = i, cnt, mn
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
	ime.mixedPairSwapIndex = bestIdx
	debugLogf("collocation rerank: %q(%d对/%.2f) 越过 %q(%d对) 提前至#1（原#%d）",
		winner.Text, bestCnt, bestMn, candidates[0].Text, firstCnt, bestIdx+1)
	return reordered, cursor, true
}

// buildDisplayPerm 由"引擎原始顺序 + 重排后顺序"构建
// 显示位次 → 引擎位次 映射；顺序完全一致时返回 nil（零开销快路径）。
// 候选文本可能重复（同词多码），用逐位贪心匹配保证双射。
func buildDisplayPerm(engineOrder, display []candidateItem) []int {
	if len(engineOrder) == 0 || len(display) == 0 || len(engineOrder) != len(display) {
		return nil
	}
	perm := make([]int, len(display))
	used := make([]bool, len(engineOrder))
	sameOrder := true
	for di := range display {
		found := -1
		for ei := range engineOrder {
			if !used[ei] && engineOrder[ei].Text == display[di].Text {
				found = ei
				break
			}
		}
		if found < 0 {
			// 文本不完全对应（重排器可能替换了文本），退化为同序。
			return nil
		}
		used[found] = true
		perm[di] = found
		if found != di {
			sameOrder = false
		}
	}
	if sameOrder {
		return nil
	}
	return perm
}

// hoistPerm 把"第 j 位提到第 1 位"同应用到显示位次→引擎位次映射上：
// perm[0]=旧 perm[j]，其余顺延。nil/越界原样返回（同序无映射时无需动）。
// 与 typo_memory 提顶/hoistCandidate 配套——提顶不改映射会导致空格/数字
// 按提顶前顺序选错词（2026-11-02 用户实报 shima）。
func hoistPerm(perm []int, j int) []int {
	if len(perm) == 0 || j <= 0 || j >= len(perm) {
		return perm
	}
	out := make([]int, 0, len(perm))
	out = append(out, perm[j])
	out = append(out, perm[:j]...)
	out = append(out, perm[j+1:]...)
	return out
}
