//go:build windows

// 2/3字词简拼专项：top-1 / hit@3 / hit@页(10) + miss 分桶
// （词不在页 / 在页但非首位）。用法：
// probe-jp-word <dataDir> <userDir> <corpus> <inputMode shengmu|jianpin> [limit]
package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/gaboolic/moqi-ime/eval"
)

func normalizeText(s string) string {
	var b strings.Builder
	for _, r := range s {
		r = unicode.ToLower(r)
		switch r {
		case '，', '。', '！', '？', '、':
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func main() {
	dataDir, userDir, corpus, mode := os.Args[1], os.Args[2], os.Args[3], os.Args[4]
	limit := 500
	if len(os.Args) > 5 {
		fmt.Sscanf(os.Args[5], "%d", &limit)
	}
	j := &eval.IMEJudge{DataDir: dataDir, UserDir: userDir}
	if err := j.Open(); err != nil {
		panic(err)
	}
	defer j.Close()
	golds, err := eval.LoadGoldSentences(corpus, 2)
	if err != nil {
		panic(err)
	}
	if limit > 0 && len(golds) > limit {
		golds = golds[:limit]
	}
	readings, err := eval.LoadCharReadings(`Z:\dev\IME\google-input-tools\reference\pinyin-data\pinyin.txt`)
	if err != nil {
		panic(err)
	}
	var total, hit1, hit3, hitPage int
	var hit1Sim, hit3Sim, hitPageSim int
	var missTop []string
	var missPage []string
	for _, gold := range golds {
		eval.InputMode = mode
		in, ok := eval.KeysForGold(gold, readings)
		if !ok || in == "" {
			continue
		}
		total++
		keys := make([][2]int, 0, len(in))
		for i := 0; i < len(in); i++ {
			up := in[i]
			if up >= 'a' && up <= 'z' {
				up -= 32
			}
			keys = append(keys, [2]int{int(up), int(in[i])})
		}
		cands, _ := j.MenuAfterKeys(keys)
		strip := func(s string) string {
			if i := strings.IndexByte(s, ' '); i > 0 {
				s = s[:i]
			}
			return normalizeText(strings.TrimSpace(s))
		}
		goldN := normalizeText(gold)
		rank := -1
		for i, c := range cands {
			if strip(c) == goldN {
				rank = i
				break
			}
		}
		switch {
		case rank == 0:
			hit1++
			hit3++
			hitPage++
		case rank > 0 && rank < 3:
			hit3++
			hitPage++
		case rank >= 3:
			hitPage++
		default:
			missPage = append(missPage, gold+"("+in+")")
		}
		// 重排模拟：gold 在页内(1..9)非首位、gold 词对有据(≥300)且首位无据(<300)
		// 时，mixed 重排会把它提到首位（与产品 999/300 语义一致的简化模拟）。
		if rank >= 1 && rank < 10 {
			goldR := []rune(gold)
			firstW := strip(cands[0])
			firstR := []rune(firstW)
			if len(goldR) >= 2 && len(firstR) >= 2 {
				g := pairMass(dataDir, string(goldR[0]), string(goldR[1]))
				f := pairMass(dataDir, string(firstR[0]), string(firstR[1]))
				if g >= 300 && f < 300 {
					hit1Sim++
					hit3Sim++
					hitPageSim++
					continue
				}
			}
		}
		if rank != 0 && len(missTop) < 15 {
			missTop = append(missTop, fmt.Sprintf("%s(%s) got=%v", gold, in, cands))
		}
	}
	fmt.Printf("total=%d hit@1=%.2f%%(%d) hit@3=%.2f%%(%d) hit@页=%.2f%%(%d)\n",
		float64(total), float64(hit1)/float64(total)*100, hit1,
		float64(hit3)/float64(total)*100, hit3,
		float64(hitPage)/float64(total)*100, hitPage)
	fmt.Printf("重排模拟: hit@1=%.2f%%(%d) hit@3=%.2f%%(%d) 页=%.2f%%(%d)\n",
		float64(hit1Sim)/float64(total)*100, hit1Sim,
		float64(hit3Sim)/float64(total)*100, hit3Sim,
		float64(hitPageSim)/float64(total)*100, hitPageSim)
	if len(missPage) > 0 {
		fmt.Println("页外 miss 样本:", strings.Join(missPage[:min(len(missPage), 10)], " / "))
	}
	if len(missTop) > 0 {
		fmt.Println("非首位样本:", strings.Join(missTop[:min(len(missTop), 8)], " ||| "))
	}
}

var pairCache = map[[2]string]int{}
var pairLoaded bool

// pairMass 查 (前字, 后字) 词对频次：读 mining 四源 TSV（与重排表同源）。
func pairMass(dataDir, first, second string) int {
	if !pairLoaded {
		pairLoaded = true
		base := filepath.Dir(dataDir)
		for _, name := range []string{"subs", "news", "ted", "leipzig"} {
			f, err := os.Open(filepath.Join(base, "mining", name+"-bigram.txt"))
			if err != nil {
				continue
			}
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 0, 1<<20), 8*1024*1024)
			for sc.Scan() {
				line := sc.Text()
				if !strings.HasPrefix(line, "n\t") {
					continue
				}
				parts := strings.Split(line, "\t")
				if len(parts) != 3 {
					continue
				}
				w1 := parts[1]
				for _, tok := range strings.Fields(parts[2]) {
					i := strings.LastIndexByte(tok, ':')
					if i <= 0 {
						continue
					}
					w2 := tok[:i]
					c, err := strconv.Atoi(tok[i+1:])
					if err != nil {
						continue
					}
					key := [2]string{w1, w2}
					if old, ok := pairCache[key]; !ok || c > old {
						pairCache[key] = c
					}
				}
			}
			f.Close()
		}
	}
	return pairCache[[2]string{first, second}]
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
