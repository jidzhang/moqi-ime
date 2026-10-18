//go:build windows

package eval

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gaboolic/moqi-ime/input_methods/rime"
)

// 真机句子评估：整句拼音 → 逐键 ProcessKey → 取首候选 → 空格提交取 commit，
// 与 google-input-tools eval-rime-native.sh 的 keys 法（typist 真实路径）一致。
// 语料复用那边的 zhihu3466（reference/corpus/eval-zhihu-3466.txt），
// 金句→拼音用其 reference/pinyin-data/pinyin.txt + phrase-pinyin-data.txt。

// CharReadings 单字读音表：字 → 首选读音（无声调，如 ni）。
type CharReadings map[string]string

// LoadCharReadings 解析 mozillazg/pinyin-data pinyin.txt（U+4E00: NǏ）。
func LoadCharReadings(path string) (CharReadings, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := CharReadings{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		code, readings, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		// 行尾带 "# 字" 注释（mozillazg pinyin-data 原版格式），必须剥掉。
		if idx := strings.Index(readings, "#"); idx >= 0 {
			readings = readings[:idx]
		}
		r, err := runeFromUPlus(strings.TrimSpace(code))
		if err != nil {
			continue
		}
		fields := strings.Split(strings.TrimSpace(readings), ",")
		if len(fields) == 0 {
			continue
		}
		out[string(r)] = stripTone(strings.TrimSpace(fields[0]))
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

// stripTone 去掉拼音声调符号（NǏ→NI→ni）。
func stripTone(s string) string {
	repl := strings.NewReplacer(
		"ā", "a", "á", "a", "ǎ", "a", "à", "a",
		"ē", "e", "é", "e", "ě", "e", "è", "e",
		"ī", "i", "í", "i", "ǐ", "i", "ì", "i",
		"ō", "o", "ó", "o", "ǒ", "o", "ò", "o",
		"ū", "u", "ú", "u", "ǔ", "u", "ù", "u",
		"ǖ", "v", "ǘ", "v", "ǚ", "v", "ǜ", "v", "ü", "v",
		"ń", "n", "ň", "n", "ǹ", "n",
		"ḿ", "m",
	)
	return strings.ToLower(repl.Replace(s))
}

// GoldToPinyin 金句逐字转连续小写拼音（无分隔，与 eval-sentences.js 一致）。
func GoldToPinyin(gold string, readings CharReadings) (string, bool) {
	var b strings.Builder
	for _, r := range gold {
		ch := string(r)
		py, ok := readings[ch]
		if !ok {
			return "", false
		}
		b.WriteString(py)
	}
	return b.String(), true
}

// GoldToJianpin 超级简拼：每字取拼音首字母（nh→你好）。
func GoldToJianpin(gold string, readings CharReadings) (string, bool) {
	var b strings.Builder
	for _, r := range gold {
		ch := string(r)
		py, ok := readings[ch]
		if !ok || py == "" {
			return "", false
		}
		b.WriteByte(py[0])
	}
	return b.String(), true
}

// TypeSentence 真机打一句：逐键送拼音，空格提交，返回 commit 文本。
func TypeSentence(session rime.RimeSessionId, pinyin string) (string, bool) {
	for _, ch := range pinyin {
		start := time.Now()
		if !rime.ProcessKey(session, int(ch), 0) {
			// 引擎不吃该键也继续，空格提交时看最终状态。
		}
		RecordKeyLatency(time.Since(start))
	}
	rime.ProcessKey(session, 0x20, 0)
	commit, ok := rime.GetCommit(session)
	if !ok {
		return "", false
	}
	return commit.Text, true
}

// KeyLatencyStats 每键延迟采样（跨配置 A/B 性能对比用）。
var keyLatencies []float64

// RecordKeyLatency 记录一次按键的引擎耗时（毫秒）。
func RecordKeyLatency(d time.Duration) {
	keyLatencies = append(keyLatencies, float64(d.Microseconds())/1000.0)
}

// LatencyReport 输出 avg/p95/max（ms/键）并清零。
func LatencyReport() (avg, p95, max float64, n int) {
	n = len(keyLatencies)
	if n == 0 {
		return
	}
	sum := 0.0
	sorted := append([]float64(nil), keyLatencies...)
	for _, v := range sorted {
		sum += v
	}
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j] < sorted[j-1]; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	avg = sum / float64(n)
	p95 = sorted[(n-1)*95/100]
	max = sorted[n-1]
	keyLatencies = nil
	return
}

// DebugTypeSentence 与 TypeSentence 相同，但打印中间状态（排障用）。
func DebugTypeSentence(session rime.RimeSessionId, pinyin string) (string, bool) {
	rime.ClearComposition(session)
	for _, ch := range pinyin {
		rime.ProcessKey(session, int(ch), 0)
	}
	if comp, ok := rime.GetComposition(session); ok {
		fmt.Printf("    preedit=%q len=%d cursor=%d sel=[%d,%d]\n",
			comp.Preedit, comp.Length, comp.CursorPos, comp.SelStart, comp.SelEnd)
	}
	if menu, ok := rime.GetMenu(session); ok {
		for i, c := range menu.Candidates {
			if i >= 3 {
				break
			}
			fmt.Printf("    cand[%d]=%q %q\n", i, c.Text, c.Comment)
		}
	}
	rime.ProcessKey(session, 0x20, 0)
	commit, ok := rime.GetCommit(session)
	if !ok {
		fmt.Println("    no commit")
		return "", false
	}
	fmt.Printf("    commit=%q\n", commit.Text)
	return commit.Text, true
}

// LoadGoldSentences 读语料：每行一句，纯汉字、长度>=minLen。
func LoadGoldSentences(path string, minLen int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := strings.Trim(strings.TrimSpace(sc.Text()), "\"'“”‘’")
		if !isPureHanzi(line) || runeLen(line) < minLen {
			continue
		}
		out = append(out, line)
	}
	return out, sc.Err()
}

func isPureHanzi(s string) bool {
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

func runeLen(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

// NativeJudge 真机裁判：init 一次，多句复用 session（每句 ClearComposition）。
type NativeJudge struct {
	DataDir string
	UserDir string
	session rime.RimeSessionId
}

// Open 初始化 librime 并建 session。
func (j *NativeJudge) Open() error {
	if !rime.RimeInit(j.DataDir, j.UserDir, "MoqiIM", "eval", false) {
		return fmt.Errorf("RimeInit failed dataDir=%q", j.DataDir)
	}
	// 全新 user dir 会触发后台部署；不等部署完成就开始打字，会把
	// 半成品方案打出来（commit 混入 # 原始段），评估结果全废。
	rime.JoinMaintenanceThread()
	session, ok := rime.StartSession()
	if !ok {
		return fmt.Errorf("StartSession failed")
	}
	j.session = session
	return nil
}

// Close 结束 session。
func (j *NativeJudge) Close() {
	if j.session != 0 {
		rime.EndSession(j.session)
		j.session = 0
	}
	rime.Finalize()
}

// Session 暴露内部会话（探针/调试用）。
func (j *NativeJudge) Session() rime.RimeSessionId {
	return j.session
}

// Type 打一句并返回 commit。
func (j *NativeJudge) Type(pinyin string) (string, bool) {
	rime.ClearComposition(j.session)
	return TypeSentence(j.session, pinyin)
}

// DebugType 打一句并打印中间状态（排障用）。
func (j *NativeJudge) DebugType(pinyin string) (string, bool) {
	return DebugTypeSentence(j.session, pinyin)
}

// Evaluate 跑全部金句，返回汇总；limit<=0 跑全量。
func (j *NativeJudge) Evaluate(golds []string, readings CharReadings, limit int) Report {
	var results []SentenceResult
	n := 0
	for _, gold := range golds {
		if limit > 0 && n >= limit {
			break
		}
		pinyin, ok := KeysForGold(gold, readings)
		if !ok {
			continue
		}
		got, ok := j.Type(pinyin)
		if !ok {
			got = ""
		}
		res := ScoreSentence(gold, got)
		if res.Gold == "" {
			continue
		}
		results = append(results, res)
		n++
	}
	return Summarize(results, 20)
}
