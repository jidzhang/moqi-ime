//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const slashSample = `punctuator:
  full_shape:
    ' ' : { commit: '　' }
    '/' : [ /, ÷ ]
  half_shape:
    ',' : { commit: ， }
    '.' : { commit: 。 }
    '/' : [ ／, ÷ ]
`

const schemaSample = `engine:
  segmentors:
    - ascii_segmentor
    - matcher
    - abc_segmentor
    - affix_segmentor@radical_lookup  # 部件拆字自定义 tag
    - punct_segmentor
    - fallback_segmentor
recognizer:
  import_preset: default
  patterns:
    punct: "^/([0-9]|10|[A-Za-z]+)$"    # 响应 symbols_v.yaml 的 symbols，用 'v' 替换 '/'
    radical_lookup: "^u[a-z]+$"         # 响应部件拆字的反查，与 radical_lookup/prefix 匹配
    unicode: "^U[a-f0-9]+"              # 脚本将自动获取第 2 个字符 U 作为触发前缀
    number: "^R[0-9]+[.]?[0-9]*"        # 数字金额大写
    gregorian_to_lunar: "^N[0-9]{1,8}"  # 公历转农历
    calculator: "^[Vv].*$"              #计算器功能引导
radical_lookup:
  tag: radical_lookup
  dictionary: radical_pinyin
  prefix: "u"
  tips: "  〔拆字〕"
speller:
  alphabet: zyxwvutsrqponmlkjihgfedcbaZYXWVUTSRQPONMLKJIHGFEDCBA` + "`/" + `
  initials: zyxwvutsrqponmlkjihgfedcbaZYXWVUTSRQPONMLKJIHGFEDCBA
`

const pageSizeSample = `menu:
  page_size: 8  # 候选词个数
  page_down_cycle: true
`

func writeTempFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readTempFile(t *testing.T, dir, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// '/' 必须从复选菜单 [ ／, ÷ ] 变成单值直接上屏，alphabet 摘掉 '/',
// 其他标点映射与其他字母不受影响。
func TestPatchSlashDirectCommit(t *testing.T) {
	dir := t.TempDir()
	writeTempFile(t, dir, "default.yaml", slashSample)
	writeTempFile(t, dir, "rime_frost.schema.yaml", schemaSample)

	if err := patchSlashDirectCommit(dir); err != nil {
		t.Fatal(err)
	}
	def := readTempFile(t, dir, "default.yaml")
	if c := strings.Count(def, "'/' : /"); c != 2 {
		t.Fatalf("half/full_shape 两处 '/' 应均改为单值直接上屏，实际 %d 处:\n%s", c, def)
	}
	if strings.Contains(def, "／") || strings.Contains(def, "÷") {
		t.Fatalf("菜单候选残留:\n%s", def)
	}
	if !strings.Contains(def, "'.' : { commit: 。 }") || !strings.Contains(def, "',' : { commit: ， }") {
		t.Fatalf("其他标点映射被误改:\n%s", def)
	}
	schema := readTempFile(t, dir, "rime_frost.schema.yaml")
	if strings.Contains(schema, "alphabet: zyxwvutsrqponmlkjihgfedcbaZYXWVUTSRQPONMLKJIHGFEDCBA`/") {
		t.Fatalf("alphabet 仍含 '/':\n%s", schema)
	}
	if !strings.Contains(schema, "alphabet: zyxwvutsrqponmlkjihgfedcbaZYXWVUTSRQPONMLKJIHGFEDCBA`") {
		t.Fatalf("alphabet 被误删其他字符:\n%s", schema)
	}

	// 幂等
	if err := patchSlashDirectCommit(dir); err != nil {
		t.Fatal(err)
	}
	if readTempFile(t, dir, "default.yaml") != def || readTempFile(t, dir, "rime_frost.schema.yaml") != schema {
		t.Fatalf("重复执行改变了文件内容")
	}
}

// 六个字母/符号触发的特殊模式全部默认禁用（$^ 永不匹配），
// 拆字 segmentor 摘除，其他 segmentor 与保留项（email/url 等继承项）不动。
func TestPatchDisableRadicalLookup(t *testing.T) {
	dir := t.TempDir()
	writeTempFile(t, dir, "rime_frost.schema.yaml", schemaSample)

	if err := patchDisableRadicalLookup(dir); err != nil {
		t.Fatal(err)
	}
	got := readTempFile(t, dir, "rime_frost.schema.yaml")
	if strings.Contains(got, "affix_segmentor@radical_lookup") {
		t.Fatalf("radical segmentor 未摘除:\n%s", got)
	}
	for _, key := range []string{"punct", "radical_lookup", "calculator", "unicode", "number", "gregorian_to_lunar"} {
		if !strings.Contains(got, key+": \"$^\"") {
			t.Fatalf("%s 模式未覆写为 $^:\n%s", key, got)
		}
	}
	if strings.Contains(got, "^[Vv].*$") {
		t.Fatalf("calculator 小写 v 劫持残留:\n%s", got)
	}
	// 同名配置段（radical_lookup: 后跟 tag/dictionary/prefix 的 map）不能被动
	cfgStart := strings.Index(got, "radical_lookup:\n")
	if cfgStart < 0 || !strings.Contains(got[cfgStart:], "tag: radical_lookup") ||
		!strings.Contains(got[cfgStart:], "prefix: \"u\"") {
		t.Fatalf("radical_lookup 配置段被误改:\n%s", got)
	}
	if !strings.Contains(got, "- punct_segmentor") || !strings.Contains(got, "- fallback_segmentor") {
		t.Fatalf("其他 segmentor 被误删:\n%s", got)
	}
	if !strings.Contains(got, "initials: zyxwvutsrqponmlkjihgfedcbaZYXWVUTSRQPONMLKJIHGFEDCBA") {
		t.Fatalf("speller initials 被误改:\n%s", got)
	}

	// 幂等
	if err := patchDisableRadicalLookup(dir); err != nil {
		t.Fatal(err)
	}
	if readTempFile(t, dir, "rime_frost.schema.yaml") != got {
		t.Fatalf("重复执行改变了文件内容")
	}
}

// page_size 8 → 9 对齐界面默认；非 8 值不动。
func TestPatchPageSizeDefault(t *testing.T) {
	dir := t.TempDir()
	writeTempFile(t, dir, "default.yaml", pageSizeSample)

	if err := patchPageSizeDefault(dir); err != nil {
		t.Fatal(err)
	}
	got := readTempFile(t, dir, "default.yaml")
	if !strings.Contains(got, "page_size: 9") {
		t.Fatalf("page_size 未对齐为 9:\n%s", got)
	}
	if !strings.Contains(got, "page_down_cycle: true") {
		t.Fatalf("menu 其他配置被误改:\n%s", got)
	}

	// 幂等：已是 9 则不动
	if err := patchPageSizeDefault(dir); err != nil {
		t.Fatal(err)
	}
	got2 := readTempFile(t, dir, "default.yaml")
	if strings.Count(got2, "page_size: 9") != 1 {
		t.Fatalf("重复执行异常:\n%s", got2)
	}

	// 用户自定义值（如 7）不动
	dir2 := t.TempDir()
	writeTempFile(t, dir2, "default.yaml", strings.ReplaceAll(pageSizeSample, "page_size: 8", "page_size: 7"))
	if err := patchPageSizeDefault(dir2); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readTempFile(t, dir2, "default.yaml"), "page_size: 7") {
		t.Fatalf("用户自定义 page_size 被误改")
	}
}

func TestMergeBigramDictPatch(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "cn_dicts"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "bigram_dict.dict.yaml")
	if err := os.WriteFile(src, []byte("# 生成文件\n---\nname: moqi_bigram\nversion: \"1\"\nsort: by_weight\n...\n的人\tde ren\t30000\n是的\tshi de\t30000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeTempFile(t, dir, "rime_frost.dict.yaml", "---\nname: rime_frost\nimport_tables:\n  - cn_dicts/8105     # 字表\n  - cn_dicts/base     # 基础词库\n...\n")

	if err := installBigramDict(src, dir); err != nil {
		t.Fatal(err)
	}
	got := readTempFile(t, dir, "rime_frost.dict.yaml")
	if !strings.Contains(got, "- cn_dicts/moqi_bigram") {
		t.Fatalf("import_tables 未挂上 moqi_bigram:\n%s", got)
	}
	if !strings.Contains(got, "- cn_dicts/base") || !strings.Contains(got, "- cn_dicts/8105") {
		t.Fatalf("原有 import 被误删:\n%s", got)
	}
	installed := readTempFile(t, dir, filepath.Join("cn_dicts", "moqi_bigram.dict.yaml"))
	if !strings.Contains(installed, "的人\tde ren\t30000") {
		t.Fatalf("补丁词典未拷入 cn_dicts/:\n%s", installed)
	}

	// 幂等：import 行不重复
	if err := installBigramDict(src, dir); err != nil {
		t.Fatal(err)
	}
	if c := strings.Count(readTempFile(t, dir, "rime_frost.dict.yaml"), "cn_dicts/moqi_bigram"); c != 1 {
		t.Fatalf("import 行重复 %d 次", c)
	}
}
