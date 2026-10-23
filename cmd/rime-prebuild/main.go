//go:build windows

// rime-prebuild 预编译 Rime 词库（消除用户首键等待）。
//
// 全新用户目录第一次按键时，librime 要现场编译全部 yaml 词库（约 10 分钟，
// 用户表现为"装完第一次按键卡几分钟"）。librime 的 PrebuiltDataDir 机制：
// <data>/build/ 里已有编译好的 .table.bin/.prism.bin 时，部署只做校验拷贝。
//
// 本工具在打包阶段把词库编译一遍，产物放回 <data>/build/ 随安装包分发：
//
//	go run ./cmd/rime-prebuild -data <打包后的 input_methods/rime/data>
//
// 目录布局要求：rime.dll 在 data 的上级目录（与 server 运行时一致）。
package main

import (
	"io/fs"
	"strings"

	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/gaboolic/moqi-ime/input_methods/rime"
)

func main() {
	dataDir := flag.String("data", "", "packaged rime shared data dir (input_methods/rime/data)")
	patchSet := flag.String("patch-set", "all", "patches to apply: all|bugfix|bigram|none")
	deploy := flag.Bool("deploy", true, "run deploy after patches (-deploy=false to only patch files)")
	flag.Parse()
	if *dataDir == "" {
		fmt.Fprintln(os.Stderr, "missing required flag: -data")
		os.Exit(2)
	}

	apply := func(set string) bool { return *patchSet == "all" || *patchSet == set }

	if apply("bugfix") {
		if err := patchSlashDirectCommit(*dataDir); err != nil {
			log.Printf("斜杠直通补丁失败（继续）: %v", err)
		}
		if err := patchCandidateMarksLua(*dataDir); err != nil {
			log.Printf("候选标记 lua 改写失败（继续）: %v", err)
		}
		if err := patchDisableRadicalLookup(*dataDir); err != nil {
			log.Printf("特殊模式禁用补丁失败（继续）: %v", err)
		}
		if err := patchPageSizeDefault(*dataDir); err != nil {
			log.Printf("候选数默认值对齐失败（继续）: %v", err)
		}
		if err := patchRemoveSmallGram(*dataDir); err != nil {
			log.Printf("内置小 gram 移除失败（继续）: %v", err)
		}
		if err := installPlainNumberProcessor(*dataDir); err != nil {
			log.Printf("数字直通处理器安装失败（继续）: %v", err)
		}
		if err := installRareDemoteFilter(*dataDir); err != nil {
			log.Printf("生僻字降位过滤器安装失败（继续）: %v", err)
		}
		if err := patchSyllablePrefixAbbrev(*dataDir); err != nil {
			log.Printf("音节前缀规则补丁失败（继续）: %v", err)
		}
	}
	if apply("bigram") {
		if err := mergeJianpinPatch(*dataDir); err != nil {
			log.Printf("简拼补丁合并失败（继续预编译）: %v", err)
		}
		if err := mergeBigramDictPatch(*dataDir); err != nil {
			log.Printf("bigram 补充词典合入失败（继续预编译）: %v", err)
		}
	}
	if *patchSet == "all" {
		if err := patchDefaultShiftR(*dataDir); err != nil {
			log.Printf("右 Shift 配置对齐失败（继续预编译）: %v", err)
		}
		if err := patchSwitcherHotkey(*dataDir); err != nil {
			log.Printf("方案选单快捷键补丁失败（继续预编译）: %v", err)
		}
		if err := patchMeltEngT9Include(*dataDir); err != nil {
			log.Printf("melt_eng_t9 include 收窄失败（继续预编译）: %v", err)
		}
	}
	// 全部补丁落盘后、部署开始前，把 yaml 源文件 mtime 向下截到偶数秒
	// （根因见 normalizeYamlMtimes：librime 秒级 == 比对 + Inno 2 秒粒度取整，不截齐则装机后必判 source file changed、首切逐个重编）。
	if err := normalizeYamlMtimes(*dataDir); err != nil {
		log.Printf("yaml 时间戳对齐失败（继续预编译）: %v", err)
	}
	if !*deploy {
		return
	}

	tmp, err := os.MkdirTemp("", "moqi-rime-prebuild-*")
	if err != nil {
		log.Fatalf("create temp user dir: %v", err)
	}
	defer os.RemoveAll(tmp)

	start := time.Now()
	if !rime.RimeInit(*dataDir, tmp, "MoqiIM", "prebuild", true) {
		log.Fatal("RimeInit failed")
	}
	rime.JoinMaintenanceThread()
	rime.Finalize()
	fmt.Printf("deploy done in %s\n", time.Since(start))

	src := filepath.Join(tmp, "build")
	dst := filepath.Join(*dataDir, "build")
	if err := copyTree(src, dst); err != nil {
		log.Fatalf("copy build artifacts: %v", err)
	}
	fmt.Printf("prebuilt artifacts installed to %s\n", dst)
}

func copyTree(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		body, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), body, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// patchDefaultShiftR 把打包数据 default.yaml 里 ascii_composer/switch_key
// 的 Shift_R 从上游默认 noop 对齐为 commit_code（与 Shift_L 一致）。
// 引擎按 Shift 释放事件的键身份查 switch_key 表：右 Shift 若为 noop，
// 无论后端判别多准确都无法切换中英。上游子模块指向原作者仓库，不便
// 直接改，故在打包期统一改写。幂等：已是目标值则原样返回。
func patchDefaultShiftR(dataDir string) error {
	path := filepath.Join(dataDir, "default.yaml")
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(body), "\n")
	patched := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "Shift_R:") {
			continue
		}
		value := trimmed[len("Shift_R:"):]
		if idx := strings.Index(value, "#"); idx >= 0 {
			value = value[:idx]
		}
		if strings.TrimSpace(value) != "noop" {
			break // 已是目标值（或被用户改过非 noop 的动作），不动
		}
		comment := ""
		if idx := strings.Index(line, "#"); idx >= 0 {
			comment = " " + strings.TrimSpace(line[idx:])
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		lines[i] = indent + "Shift_R: commit_code" + comment
		patched = true
		break // switch_key 下只有一个 Shift_R
	}
	if !patched {
		return nil
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
}

// patchSlashDirectCommit 让斜线 '/' 直接上屏半角原样（用户要求：不进任何
// 输入模式、不变全角、不弹菜单，按键即出）。
// 三处联动，缺一不可：
//  1. default.yaml punctuator half/full_shape 的 '/' 从复选菜单 [ ／, ÷ ]
//     改为单值 '/'（rime 语义：单值标点立即上屏）；
//  2. schema speller/alphabet 摘掉 '/'——否则 speller 先把 '/' 吸进组字框，
//     标点处理器永远接管不到该键；
//  3. recognizer/patterns/punct 覆写为 "$^"（'/xx' 符号查表模式彻底关闭，
//     在 patchDisableRadicalLookup 里做）。
//
// 组字框有待选内容时的标准标点语义保持：先上屏组字内容（有效拼音转汉字、
// 无候选字母串按原文），再立即输出 '/'。幂等：已是目标形态则跳过。
func patchSlashDirectCommit(dataDir string) error {
	path := filepath.Join(dataDir, "default.yaml")
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(body), "\n")
	patched := 0
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "'/' :") {
			continue
		}
		value := strings.TrimSpace(trimmed[len("'/' :"):])
		if !strings.HasPrefix(value, "[") {
			continue // 已是单值（直接上屏）或用户自定义，不动
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		lines[i] = indent + "'/' : /"
		patched++
	}
	if patched > 0 {
		fmt.Printf("[prebuild] slash direct-commit patched %d mapping(s) in default.yaml\n", patched)
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
			return err
		}
	}

	// alphabet 摘 '/'
	schemaPath := filepath.Join(dataDir, "rime_frost.schema.yaml")
	schema, err := os.ReadFile(schemaPath)
	if err != nil {
		return err
	}
	schemaLines := strings.Split(string(schema), "\n")
	for i, line := range schemaLines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "alphabet:") {
			continue
		}
		value := strings.TrimSpace(trimmed[len("alphabet:"):])
		if !strings.HasSuffix(value, "/") {
			continue
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		schemaLines[i] = indent + "alphabet: " + strings.TrimSuffix(value, "/") + "  # '/' 已移除（直通上屏，prebuild 对齐）"
		patched++
	}
	if strings.Join(schemaLines, "\n") != string(schema) {
		if err := os.WriteFile(schemaPath, []byte(strings.Join(schemaLines, "\n")), 0o644); err != nil {
			return err
		}
	}
	if patched == 0 {
		return nil
	}
	return nil
}

// patchCandidateMarksLua 重写打包数据的 lua/is_in_user_dict.lua：
// 上游给用户词加 * 、整句候选加 ∞ 的自造标记对普通用户是视觉噪声
// （用户实报）。改为默认不加标记，仅当 schema 配置 debug/show_candidate_marks
// 为 true（由 Go 侧在 MoqiLauncher 日志档=debug/trace 时下发）才显示，
// 供排查用户词典/组句来源时用。幂等：lua 已含新标记头则跳过。
func patchCandidateMarksLua(dataDir string) error {
	path := filepath.Join(dataDir, "lua", "is_in_user_dict.lua")
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	const head = "-- moqi-prebuild: 标记仅调试配置显示（debug/show_candidate_marks）"
	if strings.Contains(string(body), head) {
		return nil
	}
	replacement := head + `
-- 默认不显示 * / ∞ 标记；调试档（Go 侧下发 show_candidate_marks）恢复上游行为。
local M = {}

function M.init(env)
    local config = env.engine.schema.config
    M.show_marks = config:get_bool("debug/show_candidate_marks") or false
end

function M.func(input, env)
    if not M.show_marks then
        for cand in input:iter() do
            yield(cand)
        end
        return
    end
    for cand in input:iter() do
        if cand.type == "user_phrase" then
            cand.comment = '*'
        end
        if cand.type == 'sentence' then
            cand.comment = '∞'
        end
        yield(cand)
    end
end

return M
`
	return os.WriteFile(path, []byte(replacement), 0o644)
}

// patchPageSizeDefault 把 default.yaml 的 menu/page_size 从上游默认 8 对齐为 9。
// 根因：界面"候选字数量"默认 9（可选 3/5/7/9），引擎分页来自打包数据
// page_size: 8；两个默认值只在用户手动改设置时才同步，首次安装即出现
// "设置 9 实显 8"。UI 从不提供 8 选项，8 是永远够不到的死值，直接对齐。
func patchPageSizeDefault(dataDir string) error {
	path := filepath.Join(dataDir, "default.yaml")
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(body), "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "page_size:") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(trimmed, "page_size:"))
		if idx := strings.Index(value, "#"); idx >= 0 {
			value = strings.TrimSpace(value[:idx])
		}
		if value != "8" {
			return nil // 已对齐或用户自定义，不动
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		lines[i] = indent + "page_size: 9  # 候选词个数（与界面默认对齐，prebuild）"
		fmt.Printf("[prebuild] menu/page_size aligned 8 -> 9\n")
		return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
	}
	return nil
}

// patchRemoveSmallGram 删除内置的 7MB 小语法模型 zh-moqi.gram，并把各 schema
// 里默认的 grammar/language: zh-moqi 引用一并摘掉。
//
// 依据：内置小 gram 统计太稀疏，实测对准确率没有提升（d5 59.92 -> 59.95，
// 噪声级），却让每个安装包白白多背 7MB。用户明确要求"别打进安装包了"。
// 大 gram（essay-bgw 196.8MB）不进安装包，由用户单独下载放进 data 目录，
// 后端按 grammar/language 自动启用（见 input_methods/rime/grammar_model.go）。
//
// 必须连引用一起摘：octagram 插件按 grammar/language 去找 <language>.gram，
// 留下悬空引用会每次部署刷错误日志。
// 幂等：文件不在、引用已摘时直接返回。
func patchRemoveSmallGram(dataDir string) error {
	gramPath := filepath.Join(dataDir, "zh-moqi.gram")
	if _, err := os.Stat(gramPath); err == nil {
		if err := os.Remove(gramPath); err != nil {
			return err
		}
		fmt.Println("[prebuild] removed built-in zh-moqi.gram (7MB, no accuracy gain)")
	}

	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".schema.yaml") {
			continue
		}
		path := filepath.Join(dataDir, name)
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lines := strings.Split(string(body), "\n")
		inGrammar := false
		changed := false
		for i, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			indent := len(line) - len(strings.TrimLeft(line, " \t"))
			if indent == 0 {
				inGrammar = trimmed == "grammar:"
				continue
			}
			if !inGrammar || !strings.HasPrefix(trimmed, "language:") {
				continue
			}
			value := strings.TrimSpace(strings.TrimPrefix(trimmed, "language:"))
			if idx := strings.Index(value, "#"); idx >= 0 {
				value = strings.TrimSpace(value[:idx])
			}
			if value != "zh-moqi" {
				continue
			}
			lines[i] = "  # language: zh-moqi 已移除（内置 7MB 小 gram 无提效；大 gram 由 rime_frost.custom.yaml 指定）"
			changed = true
		}
		if changed {
			if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
				return err
			}
			fmt.Printf("[prebuild] grammar/language: zh-moqi removed from %s\n", name)
		}
	}
	return nil
}

// installPlainNumberProcessor 安装"数字/小数点直通上屏"的 Lua 处理器，
// 并挂进 frost 系 schema 的 engine/processors（ascii_composer 之后）。
//
// 背景（用户实报）：按 "4." 光标停在 "." 上、数字 4 被吞。根因是
// speller/alphabet 不含数字，数字键既进不了组字框也没有 punctuator 规则，
// 引擎直接丢弃；"." 则被 punctuator 收进组字框，要按空格才上屏。
// 处理器把"无组字时的数字"和"紧跟数字的小数点"直接上屏，对齐微软拼音；
// 组字中（有拼音/有候选）的数字仍由 selector 选候选，不影响选字。
//
// 幂等：lua 内容一致且 schema 已挂载时不动。
func installPlainNumberProcessor(dataDir string) error {
	src := filepath.Join("input_methods", "rime", "patches", "moqi_plain_number.lua")
	body, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	dst := filepath.Join(dataDir, "lua", "moqi_plain_number.lua")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if cur, err := os.ReadFile(dst); err != nil || string(cur) != string(body) {
		if err := os.WriteFile(dst, body, 0o644); err != nil {
			return err
		}
		fmt.Printf("[prebuild] plain-number lua installed -> %s\n", dst)
	}

	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "rime_frost") || !strings.HasSuffix(name, ".schema.yaml") {
			continue
		}
		if strings.Contains(strings.ToLower(name), "t9") {
			continue // T9 数字九宫格：数字是输入，不能接管
		}
		path := filepath.Join(dataDir, name)
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(body)
		if strings.Contains(text, "lua_processor@*moqi_plain_number") {
			continue // 已挂载
		}
		lines := strings.Split(text, "\n")
		idx := -1
		for i, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "- ascii_composer" {
				if idx >= 0 {
					idx = -1 // 出现多处，放弃自动挂载
					break
				}
				idx = i
			}
		}
		if idx < 0 {
			fmt.Printf("[prebuild] skip %s (no unique ascii_composer anchor)\n", name)
			continue
		}
		indent := lines[idx][:len(lines[idx])-len(strings.TrimLeft(lines[idx], " \t"))]
		inserted := append([]string{}, lines[:idx+1]...)
		inserted = append(inserted, indent+"- lua_processor@*moqi_plain_number   # 数字/小数点直通上屏（prebuild）")
		inserted = append(inserted, lines[idx+1:]...)
		if err := os.WriteFile(path, []byte(strings.Join(inserted, "\n")), 0o644); err != nil {
			return err
		}
		fmt.Printf("[prebuild] plain-number processor hooked into %s\n", name)
	}
	return nil
}

// patchSwitcherHotkey 清空 default.yaml 的方案选单快捷键（hotkeys: []）。
// 根因：Control+grave 劫持 VS/VSCode 的集成终端快捷键 Ctrl+`；换绑 F4 又
// 撞 Total Commander。方案选单没有无冲突的全局键可用，产品形态上切换走
// 托盘菜单"切换方案集"（保留），快捷键一律不提供。
// 幂等：hotkeys 已为空列表则跳过。
func patchSwitcherHotkey(dataDir string) error {
	path := filepath.Join(dataDir, "default.yaml")
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(body), "\n")
	hotkeysIdx := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "hotkeys:") {
			continue
		}
		// 只动 switcher 下的 hotkeys（default.yaml 里该键唯一，仍校验缩进
		// 归属：switcher: 行之后、下一个同级键之前）。
		for j := i - 1; j >= 0; j-- {
			if strings.HasPrefix(lines[j], "switcher:") {
				hotkeysIdx = i
				break
			}
			trimmedJ := strings.TrimRight(lines[j], " \t")
			if trimmedJ != "" && !strings.HasPrefix(trimmedJ, "#") && !strings.HasPrefix(trimmedJ, " ") && !strings.HasPrefix(trimmedJ, "\t") {
				break // 到达上一个同级键，说明不在 switcher 块内
			}
		}
		if hotkeysIdx >= 0 {
			break
		}
	}
	if hotkeysIdx < 0 {
		return nil
	}
	// 已是空列表（hotkeys: []）：幂等出口。
	if strings.TrimSpace(strings.SplitN(strings.TrimSpace(lines[hotkeysIdx]), "#", 2)[0]) == "hotkeys: []" {
		return nil
	}
	// 找到 hotkeys 块的缩进与列表项范围。
	indent := lines[hotkeysIdx][:len(lines[hotkeysIdx])-len(strings.TrimLeft(lines[hotkeysIdx], " \t"))]
	end := hotkeysIdx + 1
	for ; end < len(lines); end++ {
		trimmed := strings.TrimSpace(lines[end])
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue // 空行/注释行不算块结束，但也不搬运
		}
		if strings.HasPrefix(lines[end], indent+"  ") || strings.HasPrefix(lines[end], indent+"\t") {
			continue // 列表项或其后续行
		}
		break
	}
	// 替换为空列表：hotkeys: []，丢弃 grave/F4 行；紧邻的纯注释行一并
	// 删除（避免留下无主的 "# - Alt+grave"）。后续块（save_options）
	// 原样保留。
	replacement := indent + "hotkeys: []  # 不提供全局快捷键（与常用软件冲突），切换走托盘菜单（prebuild）"
	newLines := append([]string{}, lines[:hotkeysIdx]...)
	newLines = append(newLines, replacement)
	// 找下一个实质内容行（跳过空行与紧邻注释）
	k := end
	for k < len(lines) {
		trimmed := strings.TrimSpace(lines[k])
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			k++
			continue
		}
		break
	}
	newLines = append(newLines, lines[k:]...)
	fmt.Printf("[prebuild] switcher hotkeys cleared (no global hotkey)\n")
	return os.WriteFile(path, []byte(strings.Join(newLines, "\n")), 0o644)
}

// patchMeltEngT9Include 把 melt_eng_t9.schema.yaml 的整树 include 收窄到
// engine 段。根因：__include: melt_eng.schema.yaml:/ 拉入整棵 melt_eng 树，
// 而 melt_eng 自身又有本文件内的 algebra include 链；librime
// HasCircularDependencies 对同资源多次展开按前缀匹配误判为循环依赖
// （config_compiler.cc:527 刷屏），每次部署对 melt_eng/radical_pinyin 反复
// 重试，单 schema 3~7 秒——装机后首切 ~10-24s 的主体（探针实测 config 编译
// 17.4s / deploy 17.6s）。t9 自带 schema/translator/speller 覆盖，缺的只有
// engine 骨架，收窄到 /engine 后环断、t9 语义不变。
// 幂等：已是 /engine 则跳过。
func patchMeltEngT9Include(dataDir string) error {
	path := filepath.Join(dataDir, "melt_eng_t9.schema.yaml")
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	// 只匹配行尾是 ":/" 的整树引用，天然幂等（/engine 行不会被再匹配）。
	const oldLine = "__include: melt_eng.schema.yaml:/\n"
	const newLine = "__include: melt_eng.schema.yaml:/engine\n"
	if !strings.Contains(string(body), oldLine) {
		return nil
	}
	patched := strings.Replace(string(body), oldLine, newLine, 1)
	fmt.Printf("[prebuild] melt_eng_t9 include narrowed to /engine (breaks deploy loop)\n")
	return os.WriteFile(path, []byte(patched), 0o644)
}

// patchDisableRadicalLookup 默认禁用全部字母/符号触发的特殊模式（对齐微软
// 拼音设计原则：特殊模式用 Shift+字母触发、有开关、默认禁用；用户反馈
// 输入 u 稍停顿就误入〔拆字〕，直接字母触发违反打字预期）。
// 覆写的识别模式（值改为 "$^" 永不匹配）：
//   - radical_lookup  "^u[a-z]+$"   部件拆字反查（u）
//   - punct          "^/..."        符号查表（/，与斜杠直通配套）
//   - calculator     "^[Vv].*$"     计算器（连小写 v 都劫持）
//   - unicode        "^U[a-f0-9]+"  Unicode 输入（U）
//   - number         "^R[0-9]+..."  数字金额大写（R）
//   - gregorian_to_lunar "^N..."    公历转农历（N）
//
// 同时删除 engine/segmentors 的 affix_segmentor@radical_lookup 挂载（双保险）。
// 恢复任一模式：用户目录 rime_frost.custom.yaml 里 patch 对应 recognizer
// 模式回原值即可（即开关）。后续待办：产品设置界面开关 + Shift+字母触发。
// 大写字母参与英文造句（nihaoASDFzaijian）不是特殊模式，不受影响；
// email/url/underscore 识别保留（让 google.com 不被句号打断）。
// 幂等：值已是目标形态则跳过。
func patchDisableRadicalLookup(dataDir string) error {
	path := filepath.Join(dataDir, "rime_frost.schema.yaml")
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	const never = "\"$^\""
	disableKeys := map[string]bool{
		"punct": true, "radical_lookup": true, "calculator": true,
		"unicode": true, "number": true, "gregorian_to_lunar": true,
	}
	lines := strings.Split(string(body), "\n")
	patched := 0
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		key := trimmed
		if idx := strings.Index(key, ":"); idx >= 0 {
			key = key[:idx]
		}
		if !disableKeys[key] {
			// engine/segmentors 的拆字挂载一并移除
			if strings.HasPrefix(trimmed, "- affix_segmentor@radical_lookup") && line != "" {
				lines[i] = ""
				patched++
			}
			continue
		}
		// 只认带引号正则值的识别模式行；schema 里另有同名配置段
		// （radical_lookup: 后跟 tag/dictionary/prefix 的 map），值列为空，不能动
		value := strings.TrimSpace(trimmed[len(key)+1:])
		if !strings.HasPrefix(value, "\"") {
			continue
		}
		if strings.Contains(line, never) {
			continue // 已禁用
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		lines[i] = indent + key + ": " + never + "  # 默认禁用（prebuild；恢复方法见 rime-prebuild/main.go）"
		patched++
	}
	if patched == 0 {
		return nil
	}
	fmt.Printf("[prebuild] special-mode patterns disabled in rime_frost.schema.yaml (%d edit(s))\n", patched)
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
}

// mergeBigramDictPatch 把高频口语 bigram 补充词典合入打包数据：
// 拷 patches/bigram_dict.dict.yaml → <data>/cn_dicts/moqi_bigram.dict.yaml，
// installRareDemoteFilter 安装生僻字降位 lua 过滤器并挂到 rime_frost 全系
// schema 的 filters 链尾（uniquifier 之后）。
// 背景：上游默认挂载 41448/GB18030-2022 字表（2026-06 起随子模块进包），
// fe/tin/bun/won/den 等输入被精确音节匹配的 Ext-A/B+ 生僻字霸占首名，
// 且这些字走系统后备字体渲染、字号巨大（2026-10-22 用户实报，bug-list #7/#9）。
// 用户拍板：不能完全关（椑/𫸴 要能打出来），降优先级、翻页可达。
// 判据与实现见 patches/moqi_rare_demote.lua；幂等：内容一致且已挂载则跳过。
func installRareDemoteFilter(dataDir string) error {
	src := filepath.Join("input_methods", "rime", "patches", "moqi_rare_demote.lua")
	body, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	dst := filepath.Join(dataDir, "lua", "moqi_rare_demote.lua")
	if cur, err := os.ReadFile(dst); err != nil || string(cur) != string(body) {
		if err := os.WriteFile(dst, body, 0o644); err != nil {
			return err
		}
		fmt.Printf("[prebuild] rare-demote lua installed -> %s\n", dst)
	}
	const marker = "lua_filter@*moqi_rare_demote"
	const anchor = "- uniquifier"
	matches, _ := filepath.Glob(filepath.Join(dataDir, "rime_frost*.schema.yaml"))
	patched := 0
	for _, schema := range matches {
		b, err := os.ReadFile(schema)
		if err != nil || !strings.Contains(string(b), anchor) || strings.Contains(string(b), marker) {
			continue
		}
		lines := strings.Split(string(b), "\n")
		for i, line := range lines {
			// 行尾普遍带注释（"- uniquifier  # 去重"），按前缀匹配
			if !strings.HasPrefix(strings.TrimSpace(line), anchor) {
				continue
			}
			indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			lines[i] = line + "\n" + indent + "- lua_filter@*moqi_rare_demote   # 生僻字降位（moqi 打包期；翻页可达）"
			patched++
			break
		}
		if err := os.WriteFile(schema, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
			return err
		}
	}
	if patched > 0 {
		fmt.Printf("[prebuild] rare-demote filter hooked into %d schema(s)\n", patched)
	}
	return nil
}

// patchSyllablePrefixAbbrev 在 rime_frost.schema.yaml 的 speller/algebra 追加：
//  1. 「两字母起的音节前缀」abbrev 规则——未完成拼音（fe/tin/zhon…）给出
//     前缀音节的汉字候选（fe→费/分/风/飞，学手机输入法，2026-10-22 用户拍板）；
//  2. erase 掉生僻字表带来的 21 个非标音节（fe/tin/bun/wong…）——它们让
//     生僻字（𫸴/𬽞/𡎛/𥥈）的 canonical 拼写占住组字节点，前缀候选出不来
//     （机理同上游 erase/^m$/：取消独占、响应超级简拼）。这些音节无任何
//     常用字，erase 后对应生僻字不可直接输入（` 反查仍可达），换取 fe→费
//     的正常体验——2026-10-22 用户拍板优先正常体验。
// 用 abbrev 而非 derive：abbrev 拼写质量低于完整音节，不改变完整拼音排序；
// 前缀在多音节组字里只可能出现在输入末尾，完整拼音口径结构上不受影响。
// 前缀规则的 [a-z]+（而非 *）防止把被 erase 的两字母音节再生成出来。
// 幂等：标记行已存在则跳过。
func patchSyllablePrefixAbbrev(dataDir string) error {
	path := filepath.Join(dataDir, "rime_frost.schema.yaml")
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	const marker = "moqi-prebuild: syllable-prefix abbrev + poison-syllable erase"
	if strings.Contains(string(body), marker) {
		return nil
	}
	const anchor = "- abbrev/^([zcs]h).+$/"
	idx := strings.Index(string(body), anchor)
	if idx < 0 {
		return fmt.Errorf("rime_frost.schema.yaml: abbrev anchor not found")
	}
	lineEnd := strings.IndexByte(string(body)[idx:], '\n')
	if lineEnd < 0 {
		return fmt.Errorf("rime_frost.schema.yaml: unexpected EOF")
	}
	indent := "    "
	insert := "\n" +
		indent + "### moqi-prebuild: syllable-prefix abbrev + poison-syllable erase\n" +
		indent + "- erase/^(bong|bun|eh|fe|fiao|fong|kiu|lvan|nia|nun|pia|piang|pv|sei|sua|ten|tin|wong|yai|zhei|ziang)$/  # 非标音节取消独占（生僻字表带进来的，无常用字）\n" +
		indent + "- abbrev/^([a-z]{2})[a-z]+$/$1/\n" +
		indent + "- abbrev/^([a-z]{3})[a-z]+$/$1/\n" +
		indent + "- abbrev/^([a-z]{4})[a-z]+$/$1/\n" +
		indent + "- abbrev/^([a-z]{5})[a-z]+$/$1/"
	out := string(body)[:idx+lineEnd] + insert + string(body)[idx+lineEnd:]
	fmt.Printf("[prebuild] syllable-prefix abbrev + poison-syllable erase added\n")
	return os.WriteFile(path, []byte(out), 0o644)
}

// 并在 rime_frost.dict.yaml 的 import_tables 挂上（幂等：已挂则跳过）。
// 与简拼补丁互补：本词典走主翻译器词典，全拼/混合输入即可整词命中
// （简拼补丁只作用于两字母超级简拼）。生成方法见补丁文件头部注释。
func mergeBigramDictPatch(dataDir string) error {
	src := filepath.Join("input_methods", "rime", "patches", "bigram_dict.dict.yaml")
	return installBigramDict(src, dataDir)
}

func installBigramDict(src, dataDir string) error {
	body, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	entries := 0
	for _, l := range strings.Split(string(body), "\n") {
		l = strings.TrimRight(l, "\r")
		if l != "" && !strings.HasPrefix(l, "#") && strings.Contains(l, "\t") {
			entries++
		}
	}
	if entries == 0 {
		return nil // 空补丁，无事可做
	}
	dst := filepath.Join(dataDir, "cn_dicts", "moqi_bigram.dict.yaml")
	if cur, err := os.ReadFile(dst); err != nil || string(cur) != string(body) {
		if err := os.WriteFile(dst, body, 0o644); err != nil {
			return err
		}
		fmt.Printf("[prebuild] bigram dict installed (%d entries) -> %s\n", entries, dst)
	}

	dictPath := filepath.Join(dataDir, "rime_frost.dict.yaml")
	dictBody, err := os.ReadFile(dictPath)
	if err != nil {
		return err
	}
	const importMarker = "cn_dicts/moqi_bigram"
	if strings.Contains(string(dictBody), importMarker) {
		return nil
	}
	lines := strings.Split(string(dictBody), "\n")
	const anchor = "- cn_dicts/base"
	inserted := false
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), anchor) {
			indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			lines[i] = line + "\n" + indent + "- cn_dicts/moqi_bigram   # 高频口语 bigram 补充（moqi 打包期合入）"
			inserted = true
			break
		}
	}
	if !inserted {
		return fmt.Errorf("rime_frost.dict.yaml: anchor %q not found", anchor)
	}
	fmt.Printf("[prebuild] bigram dict hooked into import_tables\n")
	return os.WriteFile(dictPath, []byte(strings.Join(lines, "\n")), 0o644)
}

// mergeJianpinPatch 把简拼高频组合补丁合并进打包数据目录的
// custom_phrase.txt（幂等：已合并过则跳过）。
// 补丁来源 input_methods/rime/patches/jianpin_phrases.txt，生成方法见其头部注释。
func mergeJianpinPatch(dataDir string) error {
	patchPath := filepath.Join("input_methods", "rime", "patches", "jianpin_phrases.txt")
	patchLines, err := readNonCommentLines(patchPath)
	if err != nil {
		return err
	}
	if len(patchLines) == 0 {
		return nil
	}
	target := filepath.Join(dataDir, "custom_phrase.txt")
	existing, err := os.ReadFile(target)
	if err != nil {
		return err
	}
	// 幂等标记必须落在合并块里（老实现拿首条词当标记，一旦补丁首条变动
	// 就会重复合并，custom_phrase 里出现重复词条）。
	if strings.Contains(string(existing), jianpinMergeMarker) {
		return nil // 已合并
	}
	f, err := os.OpenFile(target, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString(jianpinMergeMarker + "\n"); err != nil {
		return err
	}
	for _, l := range patchLines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			return err
		}
	}
	fmt.Printf("[prebuild] jianpin patch merged %d entries -> %s\n", len(patchLines), target)
	return nil
}

// jianpinMergeMarker 标记 custom_phrase.txt 里已合入简拼补丁，
// 供 mergeJianpinPatch 做幂等判断（补丁首条词会变，不能拿它当标记）。
const jianpinMergeMarker = "# moqi-prebuild: jianpin-phrases merged"

func readNonCommentLines(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, l := range strings.Split(string(data), "\n") {
		l = strings.TrimRight(l, "\r")
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		out = append(out, l)
	}
	return out, nil
}

// normalizeYamlMtimes 把 dataDir 下全部 yaml 源文件的 mtime 向下截到偶数秒。
// 跳过 build/ 目录（产物内容时间戳由部署写入），不动非 yaml 文件。
//
// 根因：librime ConfigNeedsUpdate 用 (int)last_write_time 做秒级 == 比对，
// 差 1 秒即判 source file changed，逐个 schema 重走部署；
// 而 Inno Setup 按 DOS 时间 2 秒粒度向下取整写入文件时间。
// 打包机上的奇数秒时间戳装机后必对不上，首切逐个重编 ~10s。
// 只截到整秒不够：必须偶数秒 +0.5s 中置，躲开 librime 时钟换算抖动。
// 幂等：同输入恒得同输出（内容无关，只动 mtime）。
func normalizeYamlMtimes(dataDir string) error {
	count := 0
	err := filepath.WalkDir(dataDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "build" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".yaml") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		// 先向下截到偶数秒（对齐 Inno 的 DOS 时间 2 秒粒度），再 +0.5s 落在
		// [偶数, 偶数+1) 窗口正中：librime 的 file_clock→system_clock 换算有毫秒级
		// 抖动，整秒（如 9204.0000）会被抖到 9203.99x 再被 (int) 截成 9203
		// 而误判 changed；+0.5s 后抖动两边都碰不到整数界。floor2(9204.5)=9204，
		// 装机取整后依然一致。int() 与 floor2() 在此窗口内恒得偶数值。
		t := info.ModTime().Truncate(2 * time.Second).Add(500 * time.Millisecond)
		if err := os.Chtimes(path, t, t); err != nil {
			return err
		}
		count++
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Println("[prebuild] yaml mtimes normalized:", count, "file(s)")
	return nil
}
