package eval

import (
	"os"
	"path/filepath"
	"testing"
)

// 冻结读音覆盖表守卫：多音字考卷必须以定版 sidecar 为准（zhihu120 六句事故、
// 尼泊尔案、堰塞体案都出自现算口径），覆盖表命中即跳过一切现算。

func writeTempSidecar(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "readings.tsv")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadReadingsOverrides(t *testing.T) {
	p := writeTempSidecar(t, "# comment\n重做\tchong zuo\n尼泊尔\tni bo er\n")
	saved := ReadingsOverride
	t.Cleanup(func() { ReadingsOverride = saved })
	ReadingsOverride = map[string][]string{}

	n, err := LoadReadingsOverrides(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if n != 2 {
		t.Fatalf("n=%d", n)
	}
	got := ReadingsOverride["重做"]
	if len(got) != 2 || got[0] != "chong" || got[1] != "zuo" {
		t.Fatalf("重做 = %v", got)
	}
}

func TestLoadReadingsOverridesRejectsBad(t *testing.T) {
	saved := ReadingsOverride
	t.Cleanup(func() { ReadingsOverride = saved })
	ReadingsOverride = map[string][]string{}

	cases := []struct {
		name string
		file string
	}{
		{"音节数不匹配", "重做\tchong zuo x\n"},
		{"非法音节", "重做\tchong1 zuo\n"},
		{"缺TAB", "重做chong zuo\n"},
		{"空音节列", "重做\t\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ReadingsOverride = map[string][]string{}
			p := writeTempSidecar(t, c.file)
			if _, err := LoadReadingsOverrides(p); err == nil {
				t.Fatalf("%s: 应报错", c.name)
			}
		})
	}
}

func TestFrozenOverridesConversion(t *testing.T) {
	// 无词表：现算只能逐字第一音（尼泊尔→ni po er，泊取湖泊音——真实事故）；
	// 冻结覆盖表命中后必须逐位采用定版（ni bo er）。
	savedOverride, savedPhrase := ReadingsOverride, PhraseReadings
	t.Cleanup(func() { ReadingsOverride, PhraseReadings = savedOverride, savedPhrase })
	ReadingsOverride = map[string][]string{}
	PhraseReadings = nil
	readings := CharReadings{"尼": "ni", "泊": "po", "尔": "er"}

	py, ok := GoldToPinyin("尼泊尔", readings)
	if !ok || py != "nipoer" {
		t.Fatalf("现算兜底口径应为 nipoer, got %q ok=%v", py, ok)
	}
	ReadingsOverride["尼泊尔"] = []string{"ni", "bo", "er"}
	py, ok = GoldToPinyin("尼泊尔", readings)
	if !ok || py != "niboer" {
		t.Fatalf("冻结命中后应为 niboer, got %q ok=%v", py, ok)
	}
	// 简派生同样走冻结音节（你得 → 两音节首字母 nd：dei 取 d 而非单字表 zhong 类错音）
	ReadingsOverride["你得"] = []string{"ni", "dei"}
	jp, ok := GoldToJianpin("你得", readings)
	if !ok || jp != "nd" {
		t.Fatalf("简派生 = %q ok=%v", jp, ok)
	}
}

func TestMissingFrozen(t *testing.T) {
	savedOverride, savedPinyin := ReadingsOverride, PinyinOverride
	t.Cleanup(func() { ReadingsOverride, PinyinOverride = savedOverride, savedPinyin })
	ReadingsOverride = map[string][]string{"正常人的做法": {"zheng", "chang", "ren", "de", "zuo", "fa"}}
	PinyinOverride = map[string]string{}

	miss := MissingFrozen([]string{"正常人的做法", "没冻结的句子"})
	if len(miss) != 1 || miss[0] != "没冻结的句子" {
		t.Fatalf("miss = %v", miss)
	}
}

// phrasePinyinCorrections 六句手工纠错必须原位保留（与 bench 仓 load_phrases_dict 同源）。
func TestPhraseCorrectionsIntact(t *testing.T) {
	want := map[string][2]string{
		"降雨":  {"jiang", "yu"},
		"受降雨": {"shou", "jiang"},
		"得重":  {"dei", "chong"},
		"重做":  {"chong", "zuo"},
		"重画":  {"chong", "hua"},
		"重绘":  {"chong", "hui"},
	}
	for w, pair := range want {
		got, ok := phrasePinyinCorrections[w]
		if !ok || len(got) < 2 || got[0] != pair[0] || got[1] != pair[1] {
			t.Fatalf("纠错词条 %s = %v", w, got)
		}
	}
}
