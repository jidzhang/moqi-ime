//go:build windows

package eval

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/gaboolic/moqi-ime/input_methods/rime"
)

// 真实打字突发评估：整句口径把权重压在长句上，但用户实际节奏是
// "两三个词一批次"。BurstPair 来自语料高频词对（Viterbi 切分、计数排序），
// 天然词对齐、频率贴近真实。
//
// 两种模式：
//   fragment     整对打全拼一次上屏，测词对级 top-1（两词准确率）
//   continuation 先上屏前词，再敲后词拼音首字母，看首候选字是否命中
//                （"你好 + m → 吗"，短窗连贯的真实度量）
type BurstPair struct {
	W1, W2 string
	Count  int
}

// LoadBurstPairs 解析 build_bigram_from_text.js 的 TSV
// （n\t词\t后词:计数 ...），按计数降序取前 topN 对。
func LoadBurstPairs(paths []string, topN int) ([]BurstPair, error) {
	counts := map[[2]string]int{}
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			return nil, err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
		for sc.Scan() {
			line := sc.Text()
			if !strings.HasPrefix(line, "n\t") {
				continue
			}
			tab1 := strings.IndexByte(line, '\t')
			tab2 := strings.IndexByte(line[tab1+1:], '\t')
			if tab1 < 0 || tab2 < 0 {
				continue
			}
			w1 := line[tab1+1 : tab1+1+tab2]
			for _, tok := range strings.Fields(line[tab1+1+tab2+1:]) {
				i := strings.LastIndexByte(tok, ':')
				if i <= 0 {
					continue
				}
				w2 := tok[:i]
				cnt, err := strconv.Atoi(tok[i+1:])
				if err != nil || cnt <= 0 {
					continue
				}
				counts[[2]string{w1, w2}] += cnt
			}
		}
		f.Close()
		if err := sc.Err(); err != nil {
			return nil, err
		}
	}
	pairs := make([]BurstPair, 0, len(counts))
	for k, c := range counts {
		pairs = append(pairs, BurstPair{W1: k[0], W2: k[1], Count: c})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].Count > pairs[j].Count })
	if topN > 0 && len(pairs) > topN {
		pairs = pairs[:topN]
	}
	return pairs, nil
}

// BurstReport 突发模式汇总。
type BurstReport struct {
	Total      int
	FragmentOK int
	Hit1       int // continuation：首候选首字命中
	Hit3       int // continuation：前三候选任一首字命中
	Samples    []string
}

// EvaluateFragment 两词整对：一次上屏（InputMode=full 全拼 / jianpin 简拼）。
// 注意必须走 KeysForGold：曾硬编码 GoldToPinyin，导致 native 臂在
// --input jianpin 下实际测的是全拼，与 ime 臂口径不一致（已修）。
func (j *NativeJudge) EvaluateFragment(pairs []BurstPair, readings CharReadings) BurstReport {
	rep := BurstReport{}
	for _, p := range pairs {
		gold := p.W1 + p.W2
		py, ok := KeysForGold(gold, readings)
		if !ok {
			continue
		}
		got, ok := j.Type(py)
		rep.Total++
		if ok && normalize(got) == normalize(gold) {
			rep.FragmentOK++
		} else if len(rep.Samples) < 15 {
			rep.Samples = append(rep.Samples,
				fmt.Sprintf("gold=%s(%s) got=%s", gold, py, got))
		}
	}
	return rep
}

// EvaluateContinuation 短窗延续：先上屏前词，再敲后词拼音首字母。
func (j *NativeJudge) EvaluateContinuation(pairs []BurstPair, readings CharReadings) BurstReport {
	rep := BurstReport{}
	for _, p := range pairs {
		py1, ok1 := GoldToPinyin(p.W1, readings)
		py2, ok2 := GoldToPinyin(p.W2, readings)
		if !ok1 || !ok2 || py2 == "" {
			continue
		}
		// 上屏前词（真实 commit，让用户词典/gram 上下文生效）。
		if _, ok := j.Type(py1); !ok {
			continue
		}
		// 只敲后词拼音的第一个字母，看首候选。
		rime.ClearComposition(j.session)
		rime.ProcessKey(j.session, int(py2[0]), 0)
		menu, ok := rime.GetMenu(j.session)
		rep.Total++
		if ok && menu.NumCandidates > 0 {
			wantFirst := string([]rune(p.W2)[0])
			hit := false
			for i := 0; i < menu.NumCandidates && i < 3; i++ {
				text := string([]rune(menu.Candidates[i].Text))
				if text != "" && string([]rune(text)[0]) == wantFirst {
					hit = true
					if i == 0 {
						rep.Hit1++
					}
					break
				}
			}
			if hit {
				rep.Hit3++
			} else if len(rep.Samples) < 15 {
				rep.Samples = append(rep.Samples,
					fmt.Sprintf("%s+%s → want %q got %q", p.W1, string(py2[0]),
						string([]rune(p.W2)[0]), firstCandidateText(menu)))
			}
		}
	}
	return rep
}

func firstCandidateText(menu rime.RimeMenu) string {
	if menu.NumCandidates == 0 {
		return ""
	}
	return menu.Candidates[0].Text
}

// LoadTriplePairs 由 bigram 链构造三词组：取高频 (w1,w2)，
// 若 (w2,w3) 也存在则扩展为 (w1,w2,w3)，w3 取其后继中计数最高者。
func LoadTriplePairs(paths []string, topN int) ([]BurstPair, error) {
	succ := map[string]BurstPair{} // w2 → 最佳 (w2,w3)
	rows := make([][3]string, 0)   // (w1, w2, count)
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			return nil, err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
		for sc.Scan() {
			line := sc.Text()
			if !strings.HasPrefix(line, "n\t") {
				continue
			}
			tab1 := strings.IndexByte(line, '\t')
			rest := line[tab1+1:]
			tab2 := strings.IndexByte(rest, '\t')
			if tab1 < 0 || tab2 < 0 {
				continue
			}
			w1 := rest[:tab2]
			bestW2, bestC := "", 0
			for _, tok := range strings.Fields(rest[tab2+1:]) {
				i := strings.LastIndexByte(tok, ':')
				if i <= 0 {
					continue
				}
				c, _ := strconv.Atoi(tok[i+1:])
				if c > bestC {
					bestC = c
					bestW2 = tok[:i]
				}
			}
			if bestW2 == "" {
				continue
			}
			rows = append(rows, [3]string{w1, bestW2, strconv.Itoa(bestC)})
		}
		f.Close()
	}
	// 第二遍：w2 的最佳后继（跨文件取最大）
	best := map[string]string{}
	for _, p := range paths {
		f, _ := os.Open(p)
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
		for sc.Scan() {
			line := sc.Text()
			if !strings.HasPrefix(line, "n\t") {
				continue
			}
			tab1 := strings.IndexByte(line, '\t')
			rest := line[tab1+1:]
			tab2 := strings.IndexByte(rest, '\t')
			if tab2 < 0 {
				continue
			}
			w1 := rest[:tab2]
			bestW2, bestC := "", 0
			for _, tok := range strings.Fields(rest[tab2+1:]) {
				i := strings.LastIndexByte(tok, ':')
				if i <= 0 {
					continue
				}
				c, _ := strconv.Atoi(tok[i+1:])
				if c > bestC {
					bestC = c
					bestW2 = tok[:i]
				}
			}
			if bestW2 != "" {
				if prev, ok := best[w1]; !ok || bestC > 0 && prev == "" {
					best[w1] = bestW2
				}
			}
		}
		f.Close()
	}
	_ = succ
	triples := make([]BurstPair, 0, len(rows))
	seen := map[string]bool{}
	for _, r := range rows {
		w3, ok := best[r[1]]
		if !ok || w3 == "" || seen[r[0]+r[1]] {
			continue
		}
		seen[r[0]+r[1]] = true
		c, _ := strconv.Atoi(r[2])
		triples = append(triples, BurstPair{W1: r[0] + r[1], W2: w3, Count: c})
	}
	sort.Slice(triples, func(i, j int) bool { return triples[i].Count > triples[j].Count })
	if topN > 0 && len(triples) > topN {
		triples = triples[:topN]
	}
	return triples, nil
}

// EvaluateMixed 核心场景（用户定义）：前词全拼【不上屏】，同一组字串里
// 继续输后词的首字母（一个或多个），最后一次性空格上屏。
// 输入串 = 全拼(W1) + 简拼(W2)，期望上屏 == W1+W2。
func (j *NativeJudge) EvaluateMixed(pairs []BurstPair, readings CharReadings) BurstReport {
	rep := BurstReport{}
	for _, p := range pairs {
		py1, ok1 := GoldToPinyin(p.W1, readings)
		jp2, ok2 := GoldToJianpin(p.W2, readings)
		if !ok1 || !ok2 || jp2 == "" {
			continue
		}
		got, ok := j.Type(py1 + jp2)
		rep.Total++
		if ok && normalize(got) == normalize(p.W1+p.W2) {
			rep.FragmentOK++
		} else if len(rep.Samples) < 15 {
			rep.Samples = append(rep.Samples,
				fmt.Sprintf("%s|%s 输 %s%s → want %q got %q", p.W1, p.W2, py1, jp2, p.W1+p.W2, got))
		}
	}
	return rep
}
