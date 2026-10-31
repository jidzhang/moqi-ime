package rime

// 组词罚分（grammar/non_collocation_penalty）自动适配：
//
// 不同 gram 模型的分数分布不同，各自有不同的最优罚分（2026-10-25 全量
// 实测：essay-bgw -4、万象 0、内置小模型 -6，互套都减分）。产品规则
// （用户 2026-10-25 定）：
//   ① local_behavior.json 的 grammar_penalty 手填值最高优先——合法域
//      [-20, 0] 内照单执行，越界/非法钳到 -4，完全不探测；
//   ② 出厂模型按文件内容 SHA256 走预设表（改名不影响），零探测零延迟；
//   ③ 用户自装的陌生模型：引擎就绪后在后台校准（约 5 秒，读候选不提交
//      不写用户词典），结果按哈希落盘缓存；此后（含重启/切换应用）读
//      缓存，零成本。
//
// 校准原理：6 档 {-12,-8,-6,-4,-2,0} × 483 句自有语料（版权安全，随包
// 分发），运行时改罚分（librime RimeSetGrammarNonCollocationPenalty），
// 取命中峰值；平手取更负（2026-10-25 三模型验收：essay/-8（真值-4，实
// 际差 0.12 点）、万象/-2（真值 0，差 0.20）、小模型/-8（真值-6，差
// 0.12），全部 ≤0.2 点）。

import (
	"crypto/sha256"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// calibrationFS 内置校准句对（句子<TAB>全拼按键串，随包分发，版权安全）。
// 句集变更须重新生成拼音列（eval 的 GoldToPinyin 同源推导）。
//
//go:embed calibration_pairs.txt
var calibrationFS embed.FS

// grammarPenaltyDefault 手填非法/兜底时的默认罚分（essay 调优值）。
const grammarPenaltyDefault = -4.0

// grammarPenaltyMin/Max 手填合法域。
const (
	grammarPenaltyMin = -20.0
	grammarPenaltyMax = 0.0
)

// factoryGramPenalty 出厂模型预设（SHA256 全量）。改词库模型文件时须同步。
// 2026-10-29 gram 档位定版（用户拍板）：std 9MB / wiki 30.6MB / lite 三档，
// 罚分 -4（essay 系分布扫参峰）。lite 档 2026-10-31 从 1KB 空占位换装
// 1.1MB essay 裁剪（top-10 万高频词对，延续 hit@1 +5.2，两词简拼 -3.6）。
var factoryGramPenalty = map[string]float64{
	// zh-moqi.gram（标准包内置 9MB）→ -4（2026-10-29）
	"098bb5dac68d30dd01da194c31a92e4769fd845258cb1ad22689e68ad14a3223": -4,
	// zh-moqi-wiki.gram（wiki 包内置 30.6MB，；档名与
	// 基底解耦，后续可能换基底，用户 2026-10-29 定名不变）→ -4
	"02337eeb5a2812fa68f242bcd6689e8922bf76278e242f9a8e645309dd1cdb59": -4,
	// zh-moqi-lite.gram（lite 包内置 1.1MB，essay top-10 万裁剪，2026-10-31
	// 换下 1KB dummy 空占位）→ -4（essay 系分布）
	"7b441e6db5c3b8ec977d87d48656407d7694691142a55deda529edf4fe86f858": -4,
	// zh-hans-t-essay-bgw.gram（essay 包内置 206MB）→ -4（2026-10-24 扫参峰）
	"b91e4be2d9fee62bfda0aa0d87df94ef95c8d7ffd7302365b90a3e888dc60773": -4,
	// wanxiang-lts-zh-hans.gram（本地 wanxiang 变体包内置 380MB，仅本地出包
	// 不走 CI）→ 0（2026-10-25 全语料扫参峰，真机 63.33 复现）
	"1849e5e4acbdbfaf41fceeb86b739d5d0c85d574bd8a688562cba1e6beb78488": 0,
}

const grammarPenaltyCacheName = "grammar_penalty_cache.json"

type grammarPenaltyCache struct {
	// Calibrated 按内容哈希记忆的校准结果（改名/重启不受影响）。
	Calibrated map[string]float64 `json:"calibrated"`
	// ManualPerGram 用户对特定模型手填的记忆（哈希 → 值）。当前版本手填
	// 为全局单值；此结构预留（设置体系立项后按模型记忆）。
	Files map[string]gramFileFastKey `json:"files"`
}

// gramFileFastKey (path,size,mtime) → sha256 的快速索引，避免每次启动
// 重哈希 200-400MB 文件。
type gramFileFastKey struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	ModSec int64  `json:"mtime_sec"`
	SHA256 string `json:"sha256"`
}

var (
	grammarPenaltyCacheMu sync.Mutex
	grammarPenaltyCache_  *grammarPenaltyCache
)

func loadGrammarPenaltyCache() *grammarPenaltyCache {
	grammarPenaltyCacheMu.Lock()
	defer grammarPenaltyCacheMu.Unlock()
	if grammarPenaltyCache_ != nil {
		return grammarPenaltyCache_
	}
	c := &grammarPenaltyCache{Calibrated: map[string]float64{}}
	root := moqiAppDataDir()
	if root != "" {
		if body, err := os.ReadFile(filepath.Join(root, grammarPenaltyCacheName)); err == nil {
			_ = json.Unmarshal(body, c)
		}
	}
	if c.Calibrated == nil {
		c.Calibrated = map[string]float64{}
	}
	grammarPenaltyCache_ = c
	return c
}

func saveGrammarPenaltyCache(c *grammarPenaltyCache) {
	root := moqiAppDataDir()
	if root == "" {
		return
	}
	body, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(root, grammarPenaltyCacheName), body, 0o644)
}

// gramFilePathForLanguage 在数据目录里找 <language>.gram 的完整路径。
func gramFilePathForLanguage(language string, dirs ...string) string {
	if language == "" {
		return ""
	}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, language+".gram")
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// factoryBuiltinGrams 出厂内置 gram 接管白名单（local_behavior 通道，按
// 优先级）。均 <50MB 不进 auto 检测：wiki 包 30.6MB、标准包 9MB、
// lite 包 1.1MB（essay 裁剪）。
var factoryBuiltinGrams = []string{"zh-moqi-wiki", "zh-moqi", "zh-moqi-lite"}

// gramFileSHA256 带快速索引的哈希（size+mtime 未变直接用缓存值）。
func gramFileSHA256(path string) string {
	st, err := os.Stat(path)
	if err != nil {
		return ""
	}
	cache := loadGrammarPenaltyCache()
	grammarPenaltyCacheMu.Lock()
	fk, ok := cache.Files[path]
	grammarPenaltyCacheMu.Unlock()
	if ok && fk.Size == st.Size() && fk.ModSec == st.ModTime().Unix() && fk.SHA256 != "" {
		return fk.SHA256
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	sum := fmt.Sprintf("%x", h.Sum(nil))
	grammarPenaltyCacheMu.Lock()
	if cache.Files == nil {
		cache.Files = map[string]gramFileFastKey{}
	}
	cache.Files[path] = gramFileFastKey{Path: path, Size: st.Size(), ModSec: st.ModTime().Unix(), SHA256: sum}
	grammarPenaltyCacheMu.Unlock()
	saveGrammarPenaltyCache(cache)
	return sum
}

// ClampGrammarPenalty 手填值合法性：域内原样返回；越界/NaN 钳到默认 -4。
func ClampGrammarPenalty(v float64) float64 {
	if v != v || v < grammarPenaltyMin || v > grammarPenaltyMax {
		return grammarPenaltyDefault
	}
	return v
}

// FormatGrammarPenalty 罚分写成 schema patch 行里的数值文本（整数化呈现）。
func FormatGrammarPenalty(v float64) string {
	if v == float64(int(v)) {
		return strconv.Itoa(int(v))
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// resolveGrammarPenaltyForDeploy 部署期决定罚分：
// 返回 (value, known)。known=false 表示陌生模型需要校准（部署期不写罚分，
// 引擎就绪后后台校准再补写）。
func resolveGrammarPenaltyForDeploy(cfg localBehaviorConfig, language string, dirs ...string) (float64, bool) {
	if language == "" {
		return 0, false
	}
	// ① 手填最高优先
	if cfg.GrammarPenalty != nil {
		return ClampGrammarPenalty(*cfg.GrammarPenalty), true
	}
	path := gramFilePathForLanguage(language, dirs...)
	if path == "" {
		return 0, false
	}
	sum := gramFileSHA256(path)
	if sum == "" {
		return 0, false
	}
	// ② 出厂预设
	if v, ok := factoryGramPenalty[sum]; ok {
		return v, true
	}
	// ③ 校准缓存
	cache := loadGrammarPenaltyCache()
	grammarPenaltyCacheMu.Lock()
	v, ok := cache.Calibrated[sum]
	grammarPenaltyCacheMu.Unlock()
	return v, ok
}

// calibrationPair 一条校准样本：句子与其全拼按键串。
type calibrationPair struct {
	gold string
	keys string
}

// loadCalibrationPairs 解析内置校准对（calibration_pairs.txt，句<TAB>按键）。
func loadCalibrationPairs() []calibrationPair {
	body, err := calibrationFS.ReadFile("calibration_pairs.txt")
	if err != nil || len(body) == 0 {
		return nil
	}
	var pairs []calibrationPair
	for _, ln := range strings.Split(string(body), "\n") {
		ln = strings.TrimRight(ln, "\r")
		if ln == "" {
			continue
		}
		gold, keys, ok := strings.Cut(ln, "\t")
		if ok && gold != "" && keys != "" {
			pairs = append(pairs, calibrationPair{gold, keys})
		}
	}
	return pairs
}

// calibrateGrammarPenalty 活引擎校准：6 档 × 内置句集，读候选首项计分
// （不提交、不写用户词典），返回最优罚分。ok=false 表示条件不满足。
func calibrateGrammarPenalty(typer func(keys, gold string) bool) (float64, bool) {
	pairs := loadCalibrationPairs()
	if len(pairs) < 100 {
		return 0, false
	}
	type tier struct {
		p    float64
		hits int
	}
	tiers := []float64{-12, -8, -6, -4, -2, 0}
	results := make([]tier, 0, len(tiers))
	for _, p := range tiers {
		SetGrammarNonCollocationPenalty(p)
		hits := 0
		for _, pr := range pairs {
			if typer(pr.keys, pr.gold) {
				hits++
			}
		}
		results = append(results, tier{p, hits})
	}
	ClearGrammarNonCollocationPenalty()
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].hits != results[j].hits {
			return results[i].hits > results[j].hits
		}
		// 平手取更负（2026-10-25 验收：小模型 -8/-2 平手时 -8 实际更优）
		return results[i].p < results[j].p
	})
	if len(results) == 0 {
		return 0, false
	}
	return results[0].p, true
}

// recordCalibratedPenalty 校准结果入缓存（哈希键）。
func recordCalibratedPenalty(sum string, penalty float64) {
	cache := loadGrammarPenaltyCache()
	grammarPenaltyCacheMu.Lock()
	cache.Calibrated[sum] = penalty
	grammarPenaltyCacheMu.Unlock()
	saveGrammarPenaltyCache(cache)
}

// calibrateGrammarPenaltyIfNeeded 引擎就绪后的后台校准入口：
// 已知（手填/预设/缓存）直接返回；陌生模型校准→运行时生效→补写受管
// 补丁（下次重启随部署烘焙）+ 缓存（跨重启零成本）。
// 校准会话独立于用户输入会话；计分只读候选首项不提交，不写用户词典。
func calibrateGrammarPenaltyIfNeeded(sharedDir, userDir string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("grammar penalty calibration panic: %v", r)
		}
	}()
	cfg := loadLocalBehaviorConfig()
	lang := resolveGrammarLanguage(cfg, sharedDir, userDir)
	if lang == "" {
		return
	}
	if _, known := resolveGrammarPenaltyForDeploy(cfg, lang, sharedDir, userDir); known {
		return
	}
	path := gramFilePathForLanguage(lang, sharedDir, userDir)
	if path == "" {
		return
	}
	sum := gramFileSHA256(path)
	if sum == "" {
		return
	}
	start := time.Now()
	session, ok := StartSession()
	if !ok {
		log.Println("grammar penalty calibration: session unavailable")
		return
	}
	defer EndSession(session)
	typer := func(keys, gold string) bool {
		if !SetInput(session, keys) {
			return false
		}
		menu, ok := GetMenu(session)
		ClearComposition(session)
		return ok && menu.NumCandidates > 0 && menu.Candidates[0].Text == gold
	}
	penalty, ok := calibrateGrammarPenalty(typer)
	if !ok {
		return
	}
	// 运行时立即生效（当前引擎生命周期内）
	SetGrammarNonCollocationPenalty(penalty)
	recordCalibratedPenalty(sum, penalty)
	log.Printf("grammar penalty calibrated: lang=%s sha=%s penalty=%g elapsed=%s",
		lang, sum[:12], penalty, time.Since(start))
	// 补写受管补丁：下次重启随部署烘焙（getMenu 判定菜单可用，校准期间
	// 用户打字不受影响——rime 多会话隔离）。
	syncFrostCustomPatch(userDir, sharedDir)
}
