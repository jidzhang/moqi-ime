// probe-autopen2 — 产品化校准验证：自有句集；计分读首候选（不提交不学习）。
// 用法：probe-autopen2.exe <dataDir> <userDir> <gram> [--debug]
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
	debug := len(os.Args) > 4 && os.Args[4] == "--debug"
	readings, err := eval.LoadCharReadings(`Z:\dev\IME\google-input-tools\reference\pinyin-data\pinyin.txt`)
	if err != nil {
		panic(err)
	}
	golds, err := eval.LoadGoldSentences(`Z:\dev\IME\MoqiIME\moqi-ime\input_methods\rime\calibration_sentences.txt`, 5)
	if err != nil {
		panic(err)
	}
	fmt.Printf("calib set: %d sentences\n", len(golds))

	os.WriteFile(userDir+`\rime_frost.custom.yaml`,
		[]byte("patch:\n  grammar/language: "+gram+
			"\n  \"translator/contextual_suggestions\": true\n"+
			"\n  \"translator/enable_user_dict\": false\n"), 0o644)

	t0 := time.Now()
	if !rime.RimeInit(dataDir, userDir, "MoqiIM", "ap2", false) {
		panic("RimeInit failed")
	}
	rime.JoinMaintenanceThread()
	fmt.Printf("init: %v\n", time.Since(t0))

	sess, _ := rime.StartSession()
	defer rime.EndSession(sess)

	score := func(p int) (hits int, dur time.Duration) {
		t := time.Now()
		rime.SetGrammarNonCollocationPenalty(float64(p))
		for i, g := range golds {
			py, ok := eval.GoldToPinyin(g, readings)
			if !ok {
				continue
			}
			committed := ""
			if rime.SetInput(sess, py) {
				rime.ProcessKey(sess, 0x20, 0)
				if c, ok := rime.GetCommit(sess); ok {
					committed = c.Text
				}
			}
			if debug && i < 3 {
				fmt.Printf("  [%d] py=%s gold=%s commit=%q\n", i, py, g, committed)
			}
			if committed == g {
				hits++
			}
			rime.ClearComposition(sess)
		}
		return hits, time.Since(t)
	}

	type result struct {
		p, hits int
	}
	var rs []result
	calibStart := time.Now()
	for _, p := range []int{-12, -8, -6, -4, -2, 0} {
		h, d := score(p)
		fmt.Printf("penalty=%3d hits=%4d/%d  tier=%v\n", p, h, len(golds), d)
		rs = append(rs, result{p, h})
	}
	rime.ClearGrammarNonCollocationPenalty()
	sort.Slice(rs, func(i, j int) bool {
		if rs[i].hits != rs[j].hits {
			return rs[i].hits > rs[j].hits
		}
		abs := func(v int) int {
			if v < -4 {
				return -4 - v
			}
			return v + 4
		}
		return abs(rs[i].p) < abs(rs[j].p)
	})
	fmt.Printf("BEST penalty=%d  calibTotal=%v\n", rs[0].p, rs[0].hits, time.Since(calibStart))
}
