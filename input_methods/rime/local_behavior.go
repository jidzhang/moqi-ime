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
		// 调试档恢复候选 * / ∞ 来源标记（默认对用户隐藏；见 rime-prebuild
		// patchCandidateMarksLua 与上游 lua/is_in_user_dict.lua）
		emit(`"debug/show_candidate_marks": true`)
	}
	if lang := resolveGrammarLanguage(cfg, dirs...); lang != "" {
		emit("grammar/language: " + lang)
		emit(`"translator/contextual_suggestions": true`)
		// 大语法模型就位时的产品调优（2026-10-18 实测定档）：
		// 1) abbrev_sentences：混合输入（全拼+首字母）也构建组句候选，让
		//    语法模型参与排序。实测核心场景 +2.4；必须以大 gram 为前提——
		//    无模型时句子路径反而大幅劣化（44.3 vs 52.2）。
		// 2) melt_eng 权重降到 0.01：中文可解析的输入不再被英文词劫持
		//    （der→的人 而非英文 der）；纯英文输入不受影响（hello 照常首选）。
		//    配对 A/B +0.33。显式配置小 gram 时不启用 1（守卫见 bigGrammarActive）。
		if bigGrammarActive(lang, dirs...) {
			emit(`"translator/abbrev_sentences": true`)
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
