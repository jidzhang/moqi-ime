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
