package rime

import (
	"os"
	"path/filepath"
	"strings"
)

// 大语法模型（gram）自动启用：
// 官方包只带 7.3MB 的 zh-moqi.gram（统计太稀疏，基本无感）。实测换上
// 206MB 的 essay 级 bigram gram 后 zhihu3466 整句 +1.5 个点。大模型不进
// 安装包（体积），用户自行下载 .gram 放进数据目录，重新部署即自动启用。
// 支持两个落点（2026-10-21 起扫描两处）：
//   共享目录 <安装>\moqi-ime\input_methods\rime\data\（要管理员权限，
//     且覆盖安装会被 [InstallDelete] 删掉）；
//   用户目录 %APPDATA%\Moqi\Rime\（普通权限即可，升级永不丢；
//     octagram 经 librime ResourceResolver 用户目录优先加载）。
//
// local_behavior.json 可显式控制：
//   "grammar_model": "auto"（默认，自动选两处里最大的 gram）
//   "grammar_model": "off"（不启用任何外部 gram）
//   "grammar_model": "zh-hans-t-essay-bgw"（指定语言名）

const grammarModelAuto = "auto"

// 内置小 gram 不参与自动选择——它统计太稀疏，选它等于没开。
const minAutoGramBytes = 64 << 20 // 64MB

// detectAutoGrammarLanguage 扫描各数据目录，返回应启用的语言名
// （取最大的 .gram 文件，去掉扩展名）；没有合格文件返回空。
// 同尺寸时先出现的目录优先（共享目录排在前 = 管理员安装的优先）。
func detectAutoGrammarLanguage(dirs ...string) string {
	best := ""
	var bestSize int64
	for _, dir := range dirs {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".gram") {
				continue
			}
			info, err := e.Info()
			if err != nil || info.Size() < minAutoGramBytes {
				continue
			}
			if info.Size() > bestSize {
				bestSize = info.Size()
				best = strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
			}
		}
	}
	return best
}

// resolveGrammarLanguage 按 local_behavior 配置返回要写进 schema patch 的
// grammar/language 值；空 = 不打 patch（保持上游默认）。
func resolveGrammarLanguage(cfg localBehaviorConfig, dirs ...string) string {
	setting := strings.TrimSpace(stringOrDefault(cfg.GrammarModel, grammarModelAuto))
	switch strings.ToLower(setting) {
	case "", grammarModelAuto:
		return detectAutoGrammarLanguage(dirs...)
	case "off", "none", "false":
		return ""
	default:
		lang := strings.TrimSuffix(strings.TrimSpace(setting), ".gram")
		return lang
	}
}

func stringOrDefault(v *string, def string) string {
	if v == nil {
		return def
	}
	return *v
}
