// probe-calibrate — 罚分自动校准耗时实测：
// 模拟产品机制（改 custom.yaml → StartMaintenance 重部署 → 打校准句集 → 计命中）。
// 用法：probe-calibrate.exe <dataDir> <userDir> <gram|- > <句子数> [penalties 逗号分隔]
package main

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gaboolic/moqi-ime/eval"
	rime "github.com/gaboolic/moqi-ime/input_methods/rime"
)

func langLine(gram string) string {
	if gram == "-" {
		return ""
	}
	return "  grammar/language: " + gram + "\n"
}

func main() {
	dataDir, userDir, gram := os.Args[1], os.Args[2], os.Args[3]
	n := 200
	if len(os.Args) > 4 {
		fmt.Sscanf(os.Args[4], "%d", &n)
	}
	pens := []int{-12, -6, -4, -2, 0}
	if len(os.Args) > 5 {
		pens = nil
		for _, t := range strings.Split(os.Args[5], ",") {
			if v, err := strconv.Atoi(t); err == nil {
				pens = append(pens, v)
			}
		}
	}
	readings, err := eval.LoadCharReadings(`Z:\dev\IME\google-input-tools\reference\pinyin-data\pinyin.txt`)
	if err != nil {
		fmt.Println("readings:", err)
		os.Exit(1)
	}
	golds, err := eval.LoadGoldSentences(`Z:\dev\IME\google-input-tools\reference\corpus\eval-zhihu-3466.txt`, 5)
	if err != nil {
		fmt.Println("corpus:", err)
		os.Exit(1)
	}
	if len(golds) > n {
		// 校准句取语料尾部（难句区，罚分敏感段）；前段句子太容易区分不出配置差异
		golds = golds[len(golds)-n:]
	}

	t0 := time.Now()
	if !rime.RimeInit(dataDir, userDir, "MoqiIM", "calib", false) {
		fmt.Println("RimeInit failed")
		os.Exit(1)
	}
	rime.JoinMaintenanceThread()
	fmt.Printf("init+deploy: %v\n", time.Since(t0))

	type result struct {
		p, hits int
		dur     time.Duration
	}
	var rs []result
	for _, p := range pens {
		t := time.Now()
		body := "patch:\n" + langLine(gram) +
			fmt.Sprintf("  \"grammar/non_collocation_penalty\": %d\n  \"translator/contextual_suggestions\": true\n", p)
		if err := os.WriteFile(userDir+`\rime_frost.custom.yaml`, []byte(body), 0o644); err != nil {
			fmt.Println("write custom:", err)
			os.Exit(1)
		}
		// mtime 秒级截断会让部署器误判"未变化"：显式把修改时间推后 2 秒
		future := time.Now().Add(2 * time.Second)
		os.Chtimes(userDir+`\rime_frost.custom.yaml`, future, future)
		// 罚分只在引擎初始化时被语法组件读取：每档重初始化引擎
		rime.Finalize()
		if !rime.RimeInit(dataDir, userDir, "MoqiIM", "calib", false) {
			fmt.Println("reinit failed at penalty", p)
			os.Exit(1)
		}
		rime.JoinMaintenanceThread()
		sess, ok := rime.StartSession()
		if !ok {
			fmt.Println("StartSession failed")
			os.Exit(1)
		}
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
		d := time.Since(t)
		fmt.Printf("penalty=%4d hits=%4d/%d  armDur=%v\n", p, hits, len(golds), d)
		rs = append(rs, result{p, hits, d})
	}
	sort.Slice(rs, func(i, j int) bool {
		if rs[i].hits != rs[j].hits {
			return rs[i].hits > rs[j].hits
		}
		return rs[i].dur < rs[j].dur
	})
	fmt.Printf("best: penalty=%d hits=%d\n", rs[0].p, rs[0].hits)
}
