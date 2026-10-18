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
		if err := patchDisableRadicalLookup(*dataDir); err != nil {
			log.Printf("特殊模式禁用补丁失败（继续）: %v", err)
		}
		if err := patchPageSizeDefault(*dataDir); err != nil {
			log.Printf("候选数默认值对齐失败（继续）: %v", err)
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
	marker := "\t的人\tdr\t"
	target := filepath.Join(dataDir, "custom_phrase.txt")
	existing, err := os.ReadFile(target)
	if err != nil {
		return err
	}
	if strings.Contains(string(existing), marker) {
		return nil // 已合并
	}
	f, err := os.OpenFile(target, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, l := range patchLines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			return err
		}
	}
	fmt.Printf("[prebuild] jianpin patch merged %d entries -> %s\n", len(patchLines), target)
	return nil
}

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
