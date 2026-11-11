package eval

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// 黄金读音覆盖表：gold（整句或词）→ 定版音节序列（无声调，ü→v，空格分隔）。
//
// 为什么需要：考卷按键由语料转音生成（词表最长匹配 + 单字第一音回退），
// 多音字走错分支时按键本身就是错音，该句永远打不中——评测的是被污染的
// 考卷（zhihu120 事故 6 句、"尼泊尔"案）。冻结读音 sidecar 由
// freeze-readings 生成 + LLM 全量审读定版；运行期命中覆盖表即跳过任何
// 现算，pypinyin/词表只作为生成 sidecar 的初始值，不再是权威。
//
// 格式：TSV，# 注释，`gold<TAB>sy1 sy2 ...`（音节空格分隔以支持
// 全拼/简拼/声母三种模式派生）。gold 与语料加载后的字符串逐字节一致。
var ReadingsOverride = map[string][]string{}

// LoadReadingsOverrides 加载一个读音 sidecar 到 ReadingsOverride。
// 校验：非空 gold、音节非空且只含 a-z、音节数与 rune 数一致（逐字转音
// 的口径；语料过滤本就只留纯汉字）。
func LoadReadingsOverrides(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	ln := 0
	for sc.Scan() {
		ln++
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		gold, syl, ok := strings.Cut(line, "\t")
		gold = strings.TrimSpace(gold)
		if !ok || gold == "" {
			return n, fmt.Errorf("%s:%d 缺 TAB 或 gold 为空", path, ln)
		}
		fields := strings.Fields(syl)
		if len(fields) == 0 {
			return n, fmt.Errorf("%s:%d 音节列为空", path, ln)
		}
		runes := []rune(gold)
		if len(fields) != len(runes) {
			return n, fmt.Errorf("%s:%d 音节数 %d != 字数 %d (%q)", path, ln, len(fields), len(runes), gold)
		}
		for _, s := range fields {
			if !isASCIILetters(s) {
				return n, fmt.Errorf("%s:%d 非法音节 %q", path, ln, s)
			}
		}
		ReadingsOverride[gold] = fields
		n++
	}
	return n, sc.Err()
}

func isASCIILetters(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 'a' || c > 'z' {
			return false
		}
	}
	return true
}

// MissingFrozen 返回 golds 中未命中 ReadingsOverride/PinyinOverride 的条目
//（验收门用：考卷必须有冻结读音才能开考，现算口径只许调试不许出数）。
func MissingFrozen(golds []string) []string {
	var miss []string
	for _, g := range golds {
		if _, ok := ReadingsOverride[g]; ok {
			continue
		}
		if _, ok := PinyinOverride[g]; ok {
			continue
		}
		miss = append(miss, g)
	}
	return miss
}
