// Package eval 是墨奇输入法的可复现准确度评估框架。
//
// 设计借鉴 google-input-tools（同一维护者）的双层评估：
//  1. 回归网（regression_corpus.go）：手写小集，钉住每个已修 bug 的行为，
//     `go test ./eval/` 即可跑，跨平台；
//  2. 句子大集（sentences_windows.go）：真机 librime + zhihu3466 语料，
//     整句准确率 + macro CER，与 rime-schema-compare / eval-sentences.js
//     同口径（归一化规则一致），只能在 Windows 上跑。
//
// 一键入口：scripts/eval-all.ps1
package eval

import (
	"strings"
	"unicode/utf8"
)

// normalize 与 google-input-tools eval-sentences.js 同口径：
// 其它→其他，他/她/它→他，的/地/得→的。这些是书写风格差异，不是输入法错误。
func normalize(text string) string {
	replacements := [][2]string{
		{"其它", "其他"},
		{"她", "他"},
		{"它", "他"},
		{"地", "的"},
		{"得", "的"},
	}
	for _, r := range replacements {
		text = strings.ReplaceAll(text, r[0], r[1])
	}
	return text
}

// levenshtein 按 rune 计算编辑距离。
func levenshtein(a, b string) int {
	ar := []rune(a)
	br := []rune(b)
	if len(ar) < len(br) {
		ar, br = br, ar
	}
	if len(br) == 0 {
		return len(ar)
	}
	prev := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		cur := make([]int, len(br)+1)
		cur[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 0
			if ar[i-1] != br[j-1] {
				cost = 1
			}
			del := prev[j] + 1
			ins := cur[j-1] + 1
			sub := prev[j-1] + cost
			cur[j] = del
			if ins < cur[j] {
				cur[j] = ins
			}
			if sub < cur[j] {
				cur[j] = sub
			}
		}
		prev = cur
	}
	return prev[len(br)]
}

// InputMode 输入方式：full=全拼，jianpin=超级简拼（每字首字母），
// shengmu=声母简拼（zh/ch/sh 取双字母，其余首字母）。
// 评估工具全局开关（测量工具可接受；产品代码不用）。
var InputMode = "full"

// PinyinOverride 整句按键覆盖表：gold 句子 → 预生成按键串。
// 用于与外部黑盒基准公平对齐（如 windows-ime-bench 的 pypinyin 整句
// 转换，多音字按词定音；逐字查表无法表达），命中即跳过 KeysForGold。
var PinyinOverride map[string]string

// KeysForGold 按当前 InputMode 生成按键串。
func KeysForGold(gold string, readings CharReadings) (string, bool) {
	if py, ok := PinyinOverride[gold]; ok {
		return py, true
	}
	if InputMode == "jianpin" {
		return GoldToJianpin(gold, readings)
	}
	if InputMode == "shengmu" {
		return GoldToShengmu(gold, readings)
	}
	return GoldToPinyin(gold, readings)
}

// GoldToShengmu 声母简拼：zh/ch/sh 取双字母声母，其余取首字母
// （zhang→zh、chi→ch、an→a）。歧义远小于单字母简拼。
func GoldToShengmu(gold string, readings CharReadings) (string, bool) {
	var b strings.Builder
	for _, r := range gold {
		ch := string(r)
		py, ok := readings[ch]
		if !ok || py == "" {
			return "", false
		}
		if strings.HasPrefix(py, "zh") || strings.HasPrefix(py, "ch") || strings.HasPrefix(py, "sh") {
			b.WriteString(py[:2])
		} else {
			b.WriteByte(py[0])
		}
	}
	return b.String(), true
}

// SentenceResult 单句评估结果。
type SentenceResult struct {
	Gold     string
	Got      string
	Match    bool
	CER      float64
	GoldLen  int
	FailHint string
}

// ScoreSentence 对单句打分：整句完全匹配 + CER。
func ScoreSentence(gold, got string) SentenceResult {
	goldN := normalize(gold)
	gotN := normalize(got)
	res := SentenceResult{Gold: gold, Got: got, GoldLen: utf8.RuneCountInString(goldN)}
	res.Match = goldN == gotN
	if res.GoldLen > 0 {
		res.CER = float64(levenshtein(goldN, gotN)) / float64(res.GoldLen)
	}
	return res
}

// Report 多句汇总：整句准确率 + macro CER（每句 CER 取平均，避免少数
// 灾难句被多数简单句淹没）。
type Report struct {
	Total    int
	Matched  int
	MacroCER float64
	Fails    []SentenceResult
}

func (r Report) Accuracy() float64 {
	if r.Total == 0 {
		return 0
	}
	return float64(r.Matched) / float64(r.Total)
}

// Summarize 聚合单句结果；maxFails 限制记录的坏例数（0=全记）。
func Summarize(results []SentenceResult, maxFails int) Report {
	rep := Report{Total: len(results)}
	var cerSum float64
	for _, res := range results {
		if res.Match {
			rep.Matched++
		} else if maxFails <= 0 || len(rep.Fails) < maxFails {
			rep.Fails = append(rep.Fails, res)
		}
		cerSum += res.CER
	}
	if rep.Total > 0 {
		rep.MacroCER = cerSum / float64(rep.Total)
	}
	return rep
}

// BurstEvaluator 两种裁判（直打 dll / 全栈 Go）共用的突发评估接口。
type BurstEvaluator interface {
	EvaluateFragment(pairs []BurstPair, readings CharReadings) BurstReport
	EvaluateContinuation(pairs []BurstPair, readings CharReadings) BurstReport
	EvaluateMixed(pairs []BurstPair, readings CharReadings) BurstReport
}
