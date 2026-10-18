//go:build windows

// 排障探针：native 直连 rime.dll 打纯简拼（每字首字母），
// 打印每对的组字串/前三候选/上屏结果，与 ime 栈同输入对照。
// 用法：probe-jianpin.exe <topN>
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/gaboolic/moqi-ime/eval"
)

func main() {
	topN := 25
	if len(os.Args) > 1 {
		fmt.Sscanf(os.Args[1], "%d", &topN)
	}
	j := &eval.NativeJudge{
		DataDir: `Z:\dev\IME\MoqiIME\eval-workspace\data`,
		UserDir: `Z:\dev\IME\MoqiIME\eval-workspace\dbg-user-n`,
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
	}, topN)
	if err != nil {
		panic(err)
	}
	readings, err := eval.LoadCharReadings(`Z:\dev\IME\google-input-tools\reference\pinyin-data\pinyin.txt`)
	if err != nil {
		panic(err)
	}
	okCnt, total := 0, 0
	for _, p := range pairs {
		gold := p.W1 + p.W2
		jp, ok := eval.GoldToJianpin(gold, readings)
		if !ok {
			continue
		}
		fmt.Printf("== %s (%s)\n", gold, jp)
		got, _ := j.DebugType(jp)
		total++
		if normalize(got) == normalize(gold) {
			okCnt++
		}
	}
	fmt.Printf("native-jianpin fragment: %d/%d\n", okCnt, total)
}

// normalize 与 eval 包同口径（包内未导出，这里复制一份）。
func normalize(text string) string {
	repl := [][2]string{
		{"其它", "其他"}, {"她", "他"}, {"它", "他"}, {"地", "的"}, {"得", "的"},
	}
	for _, r := range repl {
		text = strings.ReplaceAll(text, r[0], r[1])
	}
	return text
}
