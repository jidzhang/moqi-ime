package eval

import (
	"testing"
)

func TestNormalizeCollapsesStyleVariants(t *testing.T) {
	cases := [][2]string{
		{"其它", "其他"},
		{"她去了", "他去了"},
		{"高兴地跳", "高兴的跳"},
		{"跑得快", "跑的快"},
	}
	for _, c := range cases {
		if got := normalize(c[0]); got != c[1] {
			t.Fatalf("normalize(%q)=%q want %q", c[0], got, c[1])
		}
	}
}

func TestLevenshteinRuneBased(t *testing.T) {
	if got := levenshtein("你好", "你好"); got != 0 {
		t.Fatalf("identical: got %d", got)
	}
	if got := levenshtein("你好", "你妈"); got != 1 {
		t.Fatalf("one substitution: got %d", got)
	}
	if got := levenshtein("你好吗", "你好"); got != 1 {
		t.Fatalf("one deletion: got %d", got)
	}
}

func TestScoreSentenceExactAndCER(t *testing.T) {
	res := ScoreSentence("你好吗", "你好吗")
	if !res.Match || res.CER != 0 {
		t.Fatalf("exact match: %+v", res)
	}
	res = ScoreSentence("你好吗", "你好妈")
	if res.Match {
		t.Fatal("expected mismatch")
	}
	if res.CER < 0.33 || res.CER > 0.34 {
		t.Fatalf("expected CER=1/3, got %v", res.CER)
	}
	// 风格差异不算错。
	res = ScoreSentence("她跑得快", "他跑的快")
	if !res.Match {
		t.Fatalf("style variants should match: %+v", res)
	}
}

func TestSummarizeMacroCER(t *testing.T) {
	results := []SentenceResult{
		ScoreSentence("ab", "ab"),
		ScoreSentence("abcd", "abce"),
	}
	rep := Summarize(results, 10)
	if rep.Total != 2 || rep.Matched != 1 {
		t.Fatalf("unexpected summary: %+v", rep)
	}
	if rep.Accuracy() != 0.5 {
		t.Fatalf("expected accuracy 0.5, got %v", rep.Accuracy())
	}
	wantCER := (0.0 + 0.25) / 2
	if rep.MacroCER != wantCER {
		t.Fatalf("expected macro CER %v, got %v", wantCER, rep.MacroCER)
	}
	if len(rep.Fails) != 1 {
		t.Fatalf("expected 1 fail recorded, got %d", len(rep.Fails))
	}
}

func TestGoldToPinyinPhraseLevelPolyphone(t *testing.T) {
	readings := CharReadings{"重": "zhong", "做": "zuo", "银": "yin", "行": "xing", "走": "zou", "了": "liao"}
	old := PhraseReadings
	defer func() { PhraseReadings = old }()
	PhraseReadings = map[string][]string{
		"银行": {"yin", "hang"},
		"走了": {"zou", "le"},
	}
	cases := []struct{ gold, want string }{
		{"重做", "chongzuo"},    // 纠错词典（a1e2d1d 同源）
		{"银行", "yinhang"},     // 词表命中
		{"走了", "zoule"},       // 词表命中（了→le 而非单字 liao）
		{"做银行", "zuoyinhang"}, // 词级+单字混合
	}
	for _, c := range cases {
		if got, ok := GoldToPinyin(c.gold, readings); !ok || got != c.want {
			t.Fatalf("GoldToPinyin(%q)=%q,%v want %q", c.gold, got, ok, c.want)
		}
	}
	PhraseReadings = nil // 回退旧口径：单字第一音（纠错词典仍生效，用非纠错词测）
	if got, _ := GoldToPinyin("银行", readings); got != "yinxing" {
		t.Fatalf("per-char fallback: got %q", got)
	}
}

func TestGoldToJianpinShengmuWithPhrases(t *testing.T) {
	readings := CharReadings{"长": "zhang", "重": "zhong"}
	old := PhraseReadings
	defer func() { PhraseReadings = old }()
	PhraseReadings = map[string][]string{"长江": {"chang", "jiang"}}
	if got, _ := GoldToJianpin("长江", readings); got != "cj" {
		t.Fatalf("jianpin 长江: %q", got)
	}
	if got, _ := GoldToShengmu("长江", readings); got != "chj" {
		t.Fatalf("shengmu 长江: %q", got)
	}
	if got, _ := GoldToShengmu("重做", readings); got != "chz" {
		t.Fatalf("shengmu 重做: %q", got)
	}
}
