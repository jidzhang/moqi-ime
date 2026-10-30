// probe-autopen — 罚分自动校准原型：运行时改罚分（不重初始化），测行为响应
// 与耗时。验收：essay→-4 / 万象→0 / 小模型→-6，全程 ≤5 秒。
// 用法：probe-autopen.exe <dataDir> <userDir> <gram> <句数> [采样:head|tail|stride]
package main

import (
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/gaboolic/moqi-ime/eval"
	rime "github.com/gaboolic/moqi-ime/input_methods/rime"
)

func main() {
	dataDir, userDir, gram := os.Args[1], os.Args[2], os.Args[3]
	n := 400
	if len(os.Args) > 4 {
		fmt.Sscanf(os.Args[4], "%d", &n)
	}
	mode := "stride"
	if len(os.Args) > 5 {
		mode = os.Args[5]
	}
	readings, err := eval.LoadCharReadings(`Z:\dev\IME\google-input-tools\reference\pinyin-data\pinyin.txt`)
	if err != nil {
		panic(err)
	}
	all, err := eval.LoadGoldSentences(`Z:\dev\IME\google-input-tools\reference\corpus\eval-zhihu-3466.txt`, 5)
	if err != nil {
		panic(err)
	}
	var golds []string
	switch mode {
	case "head":
		golds = all[:n]
	case "tail":
		golds = all[len(all)-n:]
	default: // stride：全量等距抽样，覆盖难度谱
		step := len(all) / n
		for i := 0; i < len(all) && len(golds) < n; i += step {
			golds = append(golds, all[i])
		}
	}

	os.WriteFile(userDir+`\rime_frost.custom.yaml`,
		[]byte("patch:\n  grammar/language: "+gram+
			"\n  \"translator/contextual_suggestions\": true\n"+
			"\n  \"translator/enable_user_dict\": false\n"), 0o644) // 校准必须关学习：跨档共享会话时学习效应淹没罚分差异

	t0 := time.Now()
	if !rime.RimeInit(dataDir, userDir, "MoqiIM", "ap", false) {
		panic("RimeInit failed")
	}
	rime.JoinMaintenanceThread()
	fmt.Printf("init: %v  probe=%d(%s)\n", time.Since(t0), len(golds), mode)

	type result struct {
		p, hits int
	}
	var rs []result
	calibStart := time.Now()
	for _, p := range []int{-12, -8, -6, -4, -2, 0} {
		t := time.Now()
		rime.SetGrammarNonCollocationPenalty(float64(p))
		sess, _ := rime.StartSession()
		hits := 0
		for _, g := range golds {
			py, ok := eval.GoldToPinyin(g, readings)
			if !ok {
				continue
			}
			if got, ok := eval.TypeSentence(sess, py); ok && got == g {
				hits++
			}
			rime.ClearComposition(sess)
		}
		rime.EndSession(sess)
		fmt.Printf("penalty=%3d hits=%4d/%d  tierDur=%v\n", p, hits, len(golds), time.Since(t))
		rs = append(rs, result{p, hits})
	}
	rime.ClearGrammarNonCollocationPenalty()
	sort.Slice(rs, func(i, j int) bool {
		if rs[i].hits != rs[j].hits {
			return rs[i].hits > rs[j].hits
		}
		// 平手取离默认 -4 更近的（保守）
		abs := func(v int) int { if v < -4 { return -4 - v }; return v + 4 }
		return abs(rs[i].p) < abs(rs[j].p)
	})
	fmt.Printf("BEST penalty=%d  calibTotal=%v\n", rs[0].p, time.Since(calibStart))
}
