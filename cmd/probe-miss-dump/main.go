//go:build windows

// d3 混合连打 miss 明细导出：gold=W1+W2、输入=全拼(W1)+简拼(W2)，
// 输出实际得到的结果 + 语料计数，供词典补丁数据驱动设计。
package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/gaboolic/moqi-ime/eval"
)

func main() {
	j := &eval.NativeJudge{
		DataDir: `Z:\dev\IME\MoqiIME\eval-workspace\data`,
		UserDir: `Z:\dev\IME\MoqiIME\eval-workspace\users\v2-3-混合连打-prod-native`,
	}
	if err := j.Open(); err != nil {
		panic(err)
	}
	defer j.Close()
	pairs, err := eval.LoadBurstPairs([]string{
		`Z:\dev\IME\MoqiIME\eval-workspace\mining\subs-bigram.txt`,
		`Z:\dev\IME\MoqiIME\eval-workspace\mining\news-bigram.txt`,
		`Z:\dev\IME\MoqiIME\eval-workspace\mining\ted-bigram.txt`,
		`Z:\dev\IME\MoqiIME\eval-workspace\mining\leipzig-bigram.txt`,
	}, 2000)
	if err != nil {
		panic(err)
	}
	readings, err := eval.LoadCharReadings(`Z:\dev\IME\google-input-tools\reference\pinyin-data\pinyin.txt`)
	if err != nil {
		panic(err)
	}
	type miss struct{ w1, w2, got string; cnt int }
	var misses []miss
	for _, p := range pairs {
		py1, ok1 := eval.GoldToPinyin(p.W1, readings)
		jp2, ok2 := eval.GoldToJianpin(p.W2, readings)
		if !ok1 || !ok2 || jp2 == "" {
			continue
		}
		got, _ := j.Type(py1 + jp2)
		norm := strings.NewReplacer("她", "他", "它", "他", "地", "的", "得", "的")
		if got != "" && norm.Replace(got) == norm.Replace(p.W1+p.W2) {
			continue
		}
		misses = append(misses, miss{p.W1, p.W2, got, p.Count})
	}
	sort.Slice(misses, func(i, k int) bool { return misses[i].cnt > misses[k].cnt })
	f, _ := os.Create(`Z:\dev\IME\MoqiIME\eval-workspace\miss-pairs.txt`)
	defer f.Close()
	fmt.Fprintf(f, "# total=%d misses=%d\n", len(pairs), len(misses))
	for _, m := range misses {
		fmt.Fprintf(f, "%s|%s\tcnt=%d\tgot=%s\n", m.w1, m.w2, m.cnt, m.got)
	}
	fmt.Printf("dumped %d misses\n", len(misses))
}
