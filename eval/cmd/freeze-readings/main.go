//go:build windows

// freeze-readings —— 考卷读音冻结与审计工具。
//
// 背景：评测按键由语料现转（词表最长匹配 + 单字第一音回退），多音字走错
// 分支时考卷本身是错音（zhihu120 事故 6 句、尼泊尔案）。本工具把语料读音
// 一次性转出 → LLM 全量审读定版 → 冻结为 sidecar（gold<TAB>音节...），
// 评测运行期只认冻结值（eval.ReadingsOverride），无冻结拒跑（验收门）。
//
// 子命令：
//
//	freeze   corpus|burst|words  从语料生成初始 sidecar（转换器现算，未审计）
//	emit-llm sidecar             切批导出 LLM 审读输入（gold<TAB>现算音节）
//	audit    sidecar             frost 词库交叉审计（ERROR/REVIEW 分桶）
//
// 用法示例见 moqi-ime/eval/cmd/freeze-readings/README.md。
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"unicode"

	"github.com/gaboolic/moqi-ime/eval"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "freeze":
		err = cmdFreeze(os.Args[2:])
	case "emit-llm":
		err = cmdEmitLLM(os.Args[2:])
	case "audit":
		err = cmdAudit(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage:
  freeze-readings freeze   --corpus f.txt | --burst a.txt,b.txt [--triple] --topN 2000 | --words w.txt
                           --pinyin pinyin.txt --phrase phrase-pinyin-data.txt --out sidecar.tsv [--minLen 5]
  freeze-readings emit-llm --sidecar sidecar.tsv --emitDir dir --batchSize 250
  freeze-readings audit    --sidecar sidecar.tsv --frost rime-frost --pinyin pinyin.txt --report out.txt
`)
}

// ---------- 共用装载 ----------

func loadCharAllReadings(path string) (map[string][]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string][]string{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		code, readings, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if idx := strings.Index(readings, "#"); idx >= 0 {
			readings = readings[:idx]
		}
		r, err := runeFromUPlus(strings.TrimSpace(code))
		if err != nil {
			continue
		}
		var norm []string
		seen := map[string]bool{}
		for _, rd := range strings.Split(readings, ",") {
			t := stripToneLocal(strings.TrimSpace(rd))
			if t != "" && !seen[t] {
				seen[t] = true
				norm = append(norm, t)
			}
		}
		if len(norm) > 0 {
			out[string(r)] = norm
		}
	}
	return out, sc.Err()
}

func runeFromUPlus(code string) (rune, error) {
	var v int
	if _, err := fmt.Sscanf(strings.ToUpper(strings.TrimPrefix(code, "U+")), "%X", &v); err != nil {
		return 0, err
	}
	return rune(v), nil
}

var toneReplacer = strings.NewReplacer(
	"ā", "a", "á", "a", "ǎ", "a", "à", "a",
	"ē", "e", "é", "e", "ě", "e", "è", "e",
	"ī", "i", "í", "i", "ǐ", "i", "ì", "i",
	"ō", "o", "ó", "o", "ǒ", "o", "ò", "o",
	"ū", "u", "ú", "u", "ǔ", "u", "ù", "u",
	"ǖ", "v", "ǘ", "v", "ǚ", "v", "ǜ", "v", "ü", "v",
	"ń", "n", "ň", "n", "ǹ", "n",
	"ḿ", "m",
	"ê", "e", "Ê", "e",
)

func stripToneLocal(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.Is(unicode.Mn, r) { // 组合附加符（NFD 形态的 ü 等）
			continue
		}
		if r >= '0' && r <= '9' { // 数字调号形态 ni3
			continue
		}
		b.WriteRune(r)
	}
	return toneReplacer.Replace(b.String())
}

// loadSidecar 读 gold<TAB>音节... 的 sidecar（含 # 注释行）。
func loadSidecar(path string) (map[string][]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string][]string{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 16<<20), 16<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		gold, syl, ok := strings.Cut(line, "\t")
		gold = strings.TrimSpace(gold)
		if !ok || gold == "" {
			return nil, fmt.Errorf("缺 TAB: %s", line)
		}
		out[gold] = strings.Fields(syl)
	}
	return out, sc.Err()
}

func writeSidecar(path string, entries map[string][]string, header string) error {
	golds := make([]string, 0, len(entries))
	for g := range entries {
		golds = append(golds, g)
	}
	// 按长度升序、同长字典序：diff 稳定，LLM 批次切块可复现。
	sort.Slice(golds, func(i, j int) bool {
		gr, jr := []rune(golds[i]), []rune(golds[j])
		if len(gr) != len(jr) {
			return len(gr) < len(jr)
		}
		return golds[i] < golds[j]
	})
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	if header != "" {
		fmt.Fprintln(w, "# "+header)
	}
	for _, g := range golds {
		fmt.Fprintf(w, "%s\t%s\n", g, strings.Join(entries[g], " "))
	}
	return w.Flush()
}

// ---------- freeze ----------

func cmdFreeze(argv []string) error {
	fs := flag.NewFlagSet("freeze", flag.ExitOnError)
	corpus := fs.String("corpus", "", "整句语料（每行一句）")
	burst := fs.String("burst", "", "mining bigram TSV 列表（逗号分隔）")
	triple := fs.Bool("triple", false, "同时收集三词链词目")
	topN := fs.Int("topN", 2000, "burst 取前 N 词对")
	words := fs.String("words", "", "纯词表（每行一词）")
	pinyin := fs.String("pinyin", "", "pinyin.txt 单字表")
	phrase := fs.String("phrase", "", "phrase-pinyin-data.txt 词组表")
	minLen := fs.Int("minLen", 5, "整句语料最短长度")
	out := fs.String("out", "", "输出 sidecar 路径")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if *pinyin == "" || *phrase == "" || *out == "" {
		return fmt.Errorf("freeze 需要 --pinyin --phrase --out")
	}
	if *corpus == "" && *burst == "" && *words == "" {
		return fmt.Errorf("freeze 需要 --corpus 或 --burst 或 --words")
	}
	readings, err := eval.LoadCharReadings(*pinyin)
	if err != nil {
		return err
	}
	ph, err := eval.LoadPhraseReadings(*phrase)
	if err != nil {
		return err
	}
	eval.PhraseReadings = ph

	entries := map[string][]string{}
	nOK, nFail := 0, 0
	convert := func(gold string) {
		if _, dup := entries[gold]; dup {
			return
		}
		syl, ok := eval.GoldSyllables(gold, readings)
		if !ok {
			nFail++
			return
		}
		entries[gold] = syl
		nOK++
	}
	switch {
	case *corpus != "":
		golds, err := eval.LoadGoldSentences(*corpus, *minLen)
		if err != nil {
			return err
		}
		for _, g := range golds {
			convert(g)
		}
		fmt.Printf("corpus: %d 句参评, %d 转音成功, %d 失败\n", len(golds), nOK, nFail)
	case *burst != "":
		paths := splitList(*burst)
		pairs, err := eval.LoadBurstPairs(paths, *topN)
		if err != nil {
			return err
		}
		// 评测各模式实际传给 KeysForGold 的键：
		//   fragment=拼接串；mixed/continuation=W1 全拼 + W2 简拼/首字母。
		for _, p := range pairs {
			convert(p.W1 + p.W2)
			convert(p.W1)
			convert(p.W2)
		}
		nPairs := len(pairs)
		if *triple {
			triples, err := eval.LoadTriplePairs(paths, *topN)
			if err != nil {
				return err
			}
			for _, p := range triples {
				convert(p.W1 + p.W2)
				convert(p.W1)
				convert(p.W2)
			}
			fmt.Printf("burst: %d 对 + %d 三词链, ", nPairs, len(triples))
		} else {
			fmt.Printf("burst: %d 对, ", nPairs)
		}
		fmt.Printf("%d 词条目, %d 失败\n", nOK, nFail)
	case *words != "":
		f, err := os.Open(*words)
		if err != nil {
			return err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
		for sc.Scan() {
			w := strings.TrimSpace(sc.Text())
			if w != "" {
				convert(w)
			}
		}
		f.Close()
		fmt.Printf("words: %d 词条目, %d 失败\n", nOK, nFail)
	}
	if err := writeSidecar(*out, entries, "initial converter output; NOT audited"); err != nil {
		return err
	}
	fmt.Printf("wrote %d entries -> %s\n", len(entries), *out)
	return nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ---------- emit-llm ----------

func cmdEmitLLM(argv []string) error {
	fs := flag.NewFlagSet("emit-llm", flag.ExitOnError)
	sidecar := fs.String("sidecar", "", "输入 sidecar")
	emitDir := fs.String("emitDir", "", "批次输出目录")
	batchSize := fs.Int("batchSize", 250, "每批条数")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if *sidecar == "" || *emitDir == "" {
		return fmt.Errorf("emit-llm 需要 --sidecar --emitDir")
	}
	entries, err := loadSidecar(*sidecar)
	if err != nil {
		return err
	}
	golds := sortedKeys(entries)
	if err := os.MkdirAll(*emitDir, 0o755); err != nil {
		return err
	}
	for i := 0; i < len(golds); i += *batchSize {
		j := i + *batchSize
		if j > len(golds) {
			j = len(golds)
		}
		name := fmt.Sprintf("%s/batch_%03d.tsv", *emitDir, i / *batchSize)
		f, err := os.Create(name)
		if err != nil {
			return err
		}
		w := bufio.NewWriter(f)
		for _, g := range golds[i:j] {
			fmt.Fprintf(w, "%s\t%s\n", g, strings.Join(entries[g], " "))
		}
		if err := w.Flush(); err != nil {
			f.Close()
			return err
		}
		f.Close()
		fmt.Printf("%s: %d 条\n", name, j-i)
	}
	return nil
}

func sortedKeys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---------- audit ----------

// vendorDicts frost 词库文件（每文件视作独立注音源）。
var vendorDicts = []string{
	"cn_dicts/8105.dict.yaml",
	"cn_dicts/base.dict.yaml",
	"cn_dicts/ext.dict.yaml",
	"cn_dicts/tencent.dict.yaml",
	"cn_dicts/others.dict.yaml",
}

func cmdAudit(argv []string) error {
	fs := flag.NewFlagSet("audit", flag.ExitOnError)
	sidecar := fs.String("sidecar", "", "待审计 sidecar")
	frost := fs.String("frost", "", "rime-frost 仓库根（含 cn_dicts/）")
	pinyin := fs.String("pinyin", "", "pinyin.txt（机械多音字清单来源）")
	report := fs.String("report", "", "审计报告输出路径")
	except := fs.String("except", "", "已裁决豁免表（gold<TAB>tok<TAB>span，# 注释）——定版读音与词库分歧且经 LLM/人工裁定保留的条目")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if *sidecar == "" || *frost == "" || *pinyin == "" {
		return fmt.Errorf("audit 需要 --sidecar --frost --pinyin")
	}
	exc := map[string]bool{}
	if *except != "" {
		for line := range linesOf(*except) {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			p := strings.Split(line, "\t")
			if len(p) >= 3 {
				exc[p[0]+"\x00"+p[1]+"\x00"+p[2]] = true
			}
		}
	}
	entries, err := loadSidecar(*sidecar)
	if err != nil {
		return err
	}
	charAll, err := loadCharAllReadings(*pinyin)
	if err != nil {
		return err
	}
	poly := map[string]bool{} // ≥2 个去调读音的字
	for ch, rds := range charAll {
		if len(rds) >= 2 {
			poly[ch] = true
		}
	}
	vocab, err := loadVendorVocab(*frost)
	if err != nil {
		return err
	}
	fmt.Printf("sidecar %d 条, vendor 词条 %d, 多音字 %d\n", len(entries), len(vocab), len(poly))

	nErr, nRev, nOOV, nPoly, nExc := 0, 0, 0, 0, 0
	var rep io.Writer
	var repBuf *bufio.Writer
	if *report != "" {
		f, err := os.Create(*report)
		if err != nil {
			return err
		}
		defer f.Close()
		repBuf = bufio.NewWriter(f)
		rep = repBuf
	} else {
		rep = os.Stdout
	}
	defer func() {
		if repBuf != nil {
			repBuf.Flush()
		}
	}()
	for _, gold := range sortedKeys(entries) {
		syl := entries[gold]
		runes := []rune(gold)
		if len(syl) != len(runes) {
			nErr++
			fmt.Fprintf(rep, "[LENGTH] %s\t%d音节%d字\n", gold, len(syl), len(runes))
			continue
		}
		i := 0
		for i < len(runes) {
			tok := longestTok(runes[i:], vocab)
			n := len([]rune(tok))
			span := strings.Join(syl[i:i+n], "")
			if exc[gold+"\x00"+tok+"\x00"+span] {
				nExc++
				fmt.Fprintf(rep, "[EXCEPTED] %s\t%s\t%s\n", gold, tok, span)
				i += n
				continue
			}
			readings := vocab[tok]
			switch {
			case readings == nil:
				hasPoly := false
				for _, c := range tok {
					if poly[string(c)] {
						hasPoly = true
						break
					}
				}
				if hasPoly {
					nRev++
					fmt.Fprintf(rep, "[REVIEW-OOV] %s\t%s\t%s\n", gold, tok, span)
				} else {
					nOOV++
				}
			case n == 1 && len(readings) > 1:
				// 盲区修补：多音字落在无多字词锚定的位置，任选读音都不会与
				// 词库报警（如"长球"的 zhang/chang、"长知"类）——全部列出，
				// 由 LLM 按语境裁决。
				nPoly++
				fmt.Fprintf(rep, "[POLY-FALLBACK] %s\t%s\t%s\tset=%s\n", gold, tok, span, keysOf(readings))
			default:
				if !readings[span] {
					if len(readings) == 1 {
						nErr++
						fmt.Fprintf(rep, "[ERROR] %s\t%s\t%s\tvendor=%s\n", gold, tok, span, keysOf(readings))
					} else {
						nRev++
						fmt.Fprintf(rep, "[REVIEW-DIV] %s\t%s\t%s\tvendor=%s\n", gold, tok, span, keysOf(readings))
					}
				}
			}
			i += n
		}
	}
	fmt.Printf("audit: %d ERROR / %d REVIEW / %d EXCEPTED / %d POLY-FALLBACK / %d OOV(无多音字) / %d total\n", nErr, nRev, nExc, nPoly, nOOV, len(entries))
	if nErr > 0 {
		return fmt.Errorf("%d 条 ERROR 需处理", nErr)
	}
	return nil
}

func keysOf(m map[string]bool) string {
	l := make([]string, 0, len(m))
	for k := range m {
		l = append(l, k)
	}
	sort.Strings(l)
	return strings.Join(l, "|")
}

func linesOf(path string) chan string {
	ch := make(chan string)
	go func() {
		f, err := os.Open(path)
		if err != nil {
			close(ch)
			return
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			ch <- sc.Text()
		}
		close(ch)
	}()
	return ch
}

func longestTok(rest []rune, vocab map[string]map[string]bool) string {
	max := 8
	if len(rest) < max {
		max = len(rest)
	}
	for l := max; l >= 2; l-- {
		if vocab[string(rest[:l])] != nil {
			return string(rest[:l])
		}
	}
	return string(rest[:1])
}

// loadVendorVocab 词 → 各 dict 文件的读音集合（去调、ü→v）。
func loadVendorVocab(root string) (map[string]map[string]bool, error) {
	out := map[string]map[string]bool{}
	for _, rel := range vendorDicts {
		p := root + "/" + rel
		f, err := os.Open(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		inTable := false
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
		for sc.Scan() {
			s := strings.TrimRight(sc.Text(), "\r")
			if !inTable {
				if strings.TrimSpace(s) == "..." {
					inTable = true
				}
				continue
			}
			if s == "" || strings.HasPrefix(s, "#") {
				continue
			}
			parts := strings.Split(s, "\t")
			if len(parts) < 2 {
				continue
			}
			word := strings.TrimSpace(parts[0])
			if !isPureHanziLocal(word) {
				continue
			}
			var syls []string
			for _, sy := range strings.Fields(parts[1]) {
				syls = append(syls, stripToneLocal(sy))
			}
			if len(syls) != len([]rune(word)) || len(syls) == 0 {
				continue
			}
			rd := strings.Join(syls, "")
			m := out[word]
			if m == nil {
				m = map[string]bool{}
				out[word] = m
			}
			m[rd] = true
		}
		f.Close()
	}
	return out, nil
}

func isPureHanziLocal(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < 0x4e00 || r > 0x9fff {
			return false
		}
	}
	return true
}
