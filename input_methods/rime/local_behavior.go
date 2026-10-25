package rime

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// 本地输入行为开关（4.2 斜杠效果 / 4.3 音形辅码）。
// 配置文件 local_behavior.json（用户目录），字段省略即保持上游原行为。
// 每次重部署前按开关自动生成 rime_frost.custom.yaml，用户手工改过该文件
// 且与本工具生成内容无关时不覆盖（只管理自己写的那份标记段）。

const localBehaviorFileName = "local_behavior.json"

const frostCustomFileName = "rime_frost.custom.yaml"

const frostCustomManagedMark = "# managed by moqi local_behavior.json"

type localBehaviorConfig struct {
	DisableSlashPunct *bool   `json:"disable_slash_punct,omitempty"`
	SlashPunctVOnly   *bool   `json:"slash_punct_v_only,omitempty"`
	DisableUnicode    *bool   `json:"disable_unicode_trigger,omitempty"`
	DisableNumber     *bool   `json:"disable_number_trigger,omitempty"`
	DisableLunar      *bool   `json:"disable_lunar_trigger,omitempty"`
	DisableCalculator *bool   `json:"disable_calculator,omitempty"`
	DisableAuxLookup  *bool   `json:"disable_aux_lookup,omitempty"`
	GrammarModel      *string `json:"grammar_model,omitempty"`
	// GrammarPenalty 手填组词罚分（grammar_penalty）：最高优先，合法域
	// [-20, 0]，越界/非法钳到 -4；缺省 = auto（预设/缓存/校准）。
	GrammarPenalty *float64 `json:"grammar_penalty,omitempty"`
}

func localBehaviorConfigPath() string {
	root := moqiAppDataDir()
	if root == "" {
		return ""
	}
	return filepath.Join(root, localBehaviorFileName)
}

func loadLocalBehaviorConfig() localBehaviorConfig {
	var cfg localBehaviorConfig
	path := localBehaviorConfigPath()
	if path == "" {
		return cfg
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return cfg
	}
	if err := json.Unmarshal(body, &cfg); err != nil {
		log.Printf("解析本地行为开关失败，保持上游默认: %v", err)
		return localBehaviorConfig{}
	}
	return cfg
}

func boolFlag(v *bool) bool {
	return v != nil && *v
}

// buildFrostCustomPatch 按开关拼出 rime_frost.custom.yaml 全文；
// 无任何开关开启时返回空（不写文件、不动用户现有文件）。
// dirs 用于大 gram 自动检测（grammar_model=auto 时扫描，共享目录在前）。
func buildFrostCustomPatch(cfg localBehaviorConfig, dirs ...string) string {
	return buildFrostCustomPatchEx(cfg, loadContextualConfig().BigramRerank.MixedPair, dirs...)
}

// buildFrostCustomPatchEx 按开关拼出 rime_frost.custom.yaml 全文；
// rerankMixed 联动 mixed 输入的用户词典禁用（见下方 emit 注释）。
func buildFrostCustomPatchEx(cfg localBehaviorConfig, rerankMixed bool, dirs ...string) string {
	var b strings.Builder
	b.WriteString(frostCustomManagedMark + "\n")
	b.WriteString("# 由 local_behavior.json 自动生成；手工改动请删掉首行标记后自行维护。\n")
	b.WriteString("patch:\n")
	lines := 0
	emit := func(s string) {
		b.WriteString("  " + s + "\n")
		lines++
	}
	if boolFlag(cfg.DisableSlashPunct) {
		emit("recognizer/patterns/punct: null")
	} else if boolFlag(cfg.SlashPunctVOnly) {
		emit(`recognizer/patterns/punct: "^v([0-9]|10|[A-Za-z]+)$"`)
	}
	if boolFlag(cfg.DisableUnicode) {
		emit("recognizer/patterns/unicode: null")
	}
	if boolFlag(cfg.DisableNumber) {
		emit("recognizer/patterns/number: null")
	}
	if boolFlag(cfg.DisableLunar) {
		emit("recognizer/patterns/gregorian_to_lunar: null")
	}
	if boolFlag(cfg.DisableCalculator) {
		emit("recognizer/patterns/calculator: null")
	}
	if boolFlag(cfg.DisableAuxLookup) {
		emit("engine/translators/-: table_translator@frost_aux")
		emit("engine/filters/-: lua_filter@*aux_lookup_filter")
	}
	if isDebugLoggingEnabled() {
		// debug 档显示候选 * / ∞ 来源标记（默认对用户隐藏；见 rime-prebuild
		// patchCandidateMarksLua 与上游 lua/is_in_user_dict.lua）。
		// 语义（2026-10-22 定）：普通模式（error）不显示任何标记；debug
		// 起（含打字习惯日志采集档）显示——调试者需要看到候选来源。
		emit(`"debug/show_candidate_marks": true`)
	}
	if rerankMixed {
		// 混合输入（全拼+首字母尾巴）禁用用户词典查询与学习（2026-10-22
		// 定档）：abbrev 提交学到的词会劫持后续同/跨输入的候选页（dedup
		// −1.3、原口径 −1.8，逐对归因 58 失/21 得）。拼音音节结尾字母只有
		// aeioungr，输入以其它辅音结尾即混合形态；librime 为全串
		// regex_match，须带 .* 前缀。全拼输入（含空格分隔的 d1/d2 形态）
		// 学习完整保留。不依赖 gram 是否在位。
		emit(`"translator/disable_user_dict_for_patterns": [".*[bcdfhjklmpqstvwxyz]$"]`)
	}
	if lang := resolveGrammarLanguage(cfg, dirs...); lang != "" {
		emit("grammar/language: " + lang)
		emit(`"translator/contextual_suggestions": true`)
		// 组词罚分：手填 > 出厂哈希预设 > 校准缓存；都已知时随补丁烘焙
		// 进部署（重启即生效、零运行时开销）。陌生模型此处不写，等引擎
		// 就绪后后台校准（grammar_penalty.go）再补写。
		if penalty, known := resolveGrammarPenaltyForDeploy(cfg, lang, dirs...); known {
			emit(`"grammar/non_collocation_penalty": ` + FormatGrammarPenalty(penalty))
		}
		// 大语法模型就位时的产品调优：
		// 1) abbrev_sentences 已移除（2026-10-23 用户拍板，方案 A）：它让混合
		//    输入（全拼+首字母）也构建组句候选，真人日志回放把混合首选率
		//    打到 29.7%（去掉后 64.8%，与 gram 全关 65.7% 噪声级）；"我没/
		//    想在/看一"式凑句压真词是主投诉。10-18 为 d3 基准 +2.4 定档时
		//    还没有 mixed_pair 重排，且 d3 测的是十多字长句连打（用户：
		//    "一般人不会连续打十多个字"）。代价 d3 65.9→56.5；如需为长句
		//    人群恢复须改回此处（local_behavior.json 无此开关）。
		// 2) melt_eng 权重降到 0.01：中文可解析的输入不再被英文词劫持
		//    （der→的人 而非英文 der）；纯英文输入不受影响（hello 照常首选）。
		//    配对 A/B +0.33。
		if bigGrammarActive(lang, dirs...) {
			emit(`"melt_eng/initial_quality": 0.01`)
			// 上游自带的英文降位过滤器开全量模式：把"拼音前缀+尾字母"型
			// 英文词（der/nas/mad…约 500 个）降到第 2 位，中文解析优先。
			// 纯英文输入不受影响（hello/ok 不在词表）。
			emit(`"reduce_english_filter/mode": all`)
			// 句子候选质量补设 B=−2（2026-10-18 夜扫参定档）：仅在 abbrev
			// 门（混合输入组句）触发时生效，exp(weight−2)+initial_quality。
			// 扫参 B∈{0,-2,-4}：−2 即平台，d3 +0.95（55.40 vs 54.45），
			// d5 逐位不动、d1 噪声内（0.05）。与 bigram 补充词典组合后
			// +0.5（整词命中替代部分组句，作用面变小），仍净正。
			emit(`"translator/sentence_quality_boost": -2`)
			// 换位容错（2026-10-19 定档 −8）：T 类错字 23.4→51.6，守卫
			// d1 −0.10 / d5 −0.12 / d3 ~−0.3 边界、每键延迟 +7.6%，全部达标。
			// 漏字边（enable_deletion + deletion_credibility）机制已实现但
			// 守卫超标（d5 −0.5~−0.9），暂不启用，待门控精细化。
			emit(`"translator/enable_transpose": true`)
			emit(`"translator/transpose_credibility": -8`)
		}
	} else if setting := strings.TrimSpace(stringOrDefault(cfg.GrammarModel, "")); setting == "" || strings.EqualFold(setting, grammarModelAuto) {
		// 仅 auto/缺省时接管小模型；grammar_model=off/显式指定名不越权。
		// 内置小模型（<64MB 不进 auto 检测，原本走上游 schema 默认）：
		// 产品接管罚分（2026-10-25 扫参：-6 比上游默认 -4 高 0.35 点）。
		// 按内容哈希确认是出厂 zh-moqi 才接管，改动过/陌生的原样走默认。
		if small := factorySmallGramPath(dirs...); small != "" {
			if sum := gramFileSHA256(small); sum != "" {
				if v, ok := factoryGramPenalty[sum]; ok {
					emit("grammar/language: zh-moqi")
					emit(`"grammar/non_collocation_penalty": ` + FormatGrammarPenalty(v))
				}
			}
		}
	}
	if lines == 0 {
		return ""
	}
	return b.String()
}

// syncFrostCustomPatch 在重部署前同步 custom.yaml；返回是否写入了文件。
// ensureFrostCustomPatch 供引擎初始化路径调用：只在能生成非空 patch 时
// 写入/更新，**绝不删除**已有配置。初始化可能撞上安装尾声的文件竞态
// （TSF DLL 按需拉起后端时共享目录尚未落齐，2026-10-21 实测：gram 扫描
// 扑空 → 空 patch → 旧逻辑删掉用户已生效的 gram 配置）。恢复上游默认
// 的删除语义只保留在托盘「刷新配置」路径（用户主动触发，无竞态）。
func ensureFrostCustomPatch(userDir, sharedDir string) bool {
	if strings.TrimSpace(userDir) == "" {
		return false
	}
	patch := buildFrostCustomPatch(loadLocalBehaviorConfig(), sharedDir, userDir)
	if patch == "" {
		return false
	}
	return writeFrostCustomPatch(userDir, patch)
}

func syncFrostCustomPatch(userDir, sharedDir string) bool {
	if strings.TrimSpace(userDir) == "" {
		return false
	}
	patch := buildFrostCustomPatch(loadLocalBehaviorConfig(), sharedDir, userDir)
	target := filepath.Join(userDir, frostCustomFileName)
	if patch == "" {
		// 无开关开启：如果现有文件是我们之前生成的，删掉以恢复上游行为；
		// 用户手工文件不动。
		body, err := os.ReadFile(target)
		if err != nil {
			return false
		}
		if strings.HasPrefix(string(body), frostCustomManagedMark) {
			_ = os.Remove(target)
			return true
		}
		return false
	}
	return writeFrostCustomPatch(userDir, patch)
}

// writeFrostCustomPatch 仅在目标不存在或为本工具托管文件时写入；
// 用户手工维护的同名文件不覆盖。
func writeFrostCustomPatch(userDir, patch string) bool {
	target := filepath.Join(userDir, frostCustomFileName)
	if existing, err := os.ReadFile(target); err == nil {
		if string(existing) == patch {
			return false
		}
		if !strings.HasPrefix(string(existing), frostCustomManagedMark) {
			log.Printf("用户已手工维护 %s，本地行为开关跳过写入", frostCustomFileName)
			return false
		}
	}
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		return false
	}
	if err := os.WriteFile(target, []byte(patch), 0o644); err != nil {
		log.Printf("写入 %s 失败: %v", frostCustomFileName, err)
		return false
	}
	return true
}

// bigGrammarActive 判断指定语言的 gram 文件是否达到大模型门槛（64MB），
// 任一数据目录命中即为真。abbrev_sentences 只应在大模型下启用
// （见 buildFrostCustomPatch 注释）。
func bigGrammarActive(lang string, dirs ...string) bool {
	if lang == "" {
		return false
	}
	for _, dir := range dirs {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		info, err := os.Stat(filepath.Join(dir, lang+".gram"))
		if err == nil && info.Size() >= minAutoGramBytes {
			return true
		}
	}
	return false
}
