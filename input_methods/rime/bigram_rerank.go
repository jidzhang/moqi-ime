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
			MixedPair  *bool    `json:"mixed_pair"`
			PairRatio  *float64 `json:"pair_ratio"`
			PairFloor  *int     `json:"pair_floor"`
			PairMaxLen *int     `json:"pair_max_len"`
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
	_, pairs := ime.bigramTableSnapshotFull()
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
	first := pairScore(pairs, candidates[0].Text, maxLen)
	bestIdx := -1
	bestScore := 0
	for i := 1; i < len(candidates); i++ {
		s := pairScore(pairs, candidates[i].Text, maxLen)
		if s >= cfg.PairFloor && (first == 0 || float64(s) >= cfg.PairRatio*float64(first)) && s > bestScore {
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
	debugLogf("mixed pair rerank: compo=%q %q(%d) 超过 %q(%d) 提前至#%d", composition, winner.Text, bestScore, candidates[0].Text, first, bestIdx+1)
	return reordered, cursor, true
}
