//go:build windows

// 真机句子评估入口：
//
//	go run ./eval/cmd/eval-sentences --corpus <zhihu3466.txt> \
//	  --pinyin <pinyin.txt> --data <rime data dir> --user <rime user dir> [--limit N]
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/gaboolic/moqi-ime/eval"
)

func main() {
	corpus := flag.String("corpus", "", "gold sentence corpus (one pure-hanzi sentence per line)")
	pinyin := flag.String("pinyin", "", "mozillazg pinyin-data pinyin.txt")
	dataDir := flag.String("data", "", "deployed rime data dir (sharedDir)")
	userDir := flag.String("user", "", "rime user dir (writable)")
	limit := flag.Int("limit", 400, "max sentences (<=0 for all)")
	debug := flag.Bool("debug", false, "print per-sentence detail for the first 3 sentences")
	burstTSV := flag.String("burst", "", "bigram TSV(s), comma-separated: run burst eval instead of sentence eval")
	burstMode := flag.String("burstMode", "fragment", "burst mode: fragment | continuation")
	stack := flag.String("stack", "native", "judge stack: native (rime.dll direct) | ime (full Go pipeline)")
	input := flag.String("input", "full", "input mode: full | jianpin")
	triple := flag.Bool("triple", false, "burst: use chained 3-word pairs")
	burstTopN := flag.Int("burstTopN", 2000, "top-N bigram pairs for burst eval")
	mixedTop := flag.Bool("mixedTop", false, "mixed: also report hit@3/hit@page (ime stack, slow path)")
	noFastPath := flag.Bool("noFastPath", false, "disable fast-path typing so Go-side rerank/lift run (A/B Go pipeline features)")
	burstDedupInput := flag.Bool("burstDedupInput", false, "burst: drop later pairs whose d3 input (W1 full pinyin + W2 initial) collides with an earlier pair; same-input golds cap top-2000 at 62.25%")
	flag.Parse()

	if *pinyin == "" || *dataDir == "" || *userDir == "" || (*burstTSV == "" && *corpus == "") {
		fmt.Fprintln(os.Stderr, "missing required flags: --corpus/--burst --pinyin --data --user")
		flag.Usage()
		os.Exit(2)
	}

	var golds []string
	if *corpus != "" {
		var err error
		golds, err = eval.LoadGoldSentences(*corpus, 5)
		if err != nil {
			fmt.Fprintln(os.Stderr, "load corpus:", err)
			os.Exit(1)
		}
	}
	readings, err := eval.LoadCharReadings(*pinyin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "load pinyin:", err)
		os.Exit(1)
	}

	// native 裁判按需打开：同进程先后两次 RimeInit（native + ime）会互相
	// 干扰（实测 burst ime 臂被打回 ascii 裸提交）。ime 栈路径绝不打开它。
	var judge *eval.NativeJudge
	openNative := func() *eval.NativeJudge {
		if judge != nil {
			return judge
		}
		judge = &eval.NativeJudge{DataDir: *dataDir, UserDir: *userDir}
		if err := judge.Open(); err != nil {
			fmt.Fprintln(os.Stderr, "open judge:", err)
			os.Exit(1)
		}
		return judge
	}
	defer func() {
		if judge != nil {
			judge.Close()
		}
	}()

	if *stack == "ime" && *burstTSV == "" {
		// 全栈整句评估（burst 请求由下方 burst 分支处理）。
		imeJudge := &eval.IMEJudge{DataDir: *dataDir, UserDir: *userDir + "-ime/rime"}
		if err := imeJudge.Open(); err != nil {
			fmt.Fprintln(os.Stderr, "open ime judge:", err)
			os.Exit(1)
		}
		defer imeJudge.Close()
		eval.InputMode = *input
		rep := imeJudge.Evaluate(golds, readings, *limit)
		fmt.Printf("fast-path: hit=%d missSet=%d missCommit=%d warmup=%d\n",
			eval.FastPathStats.Hits, eval.FastPathStats.MissSet, eval.FastPathStats.MissCommit, eval.FastPathStats.Warmups)
		fmt.Printf("top-1 %.2f%% (%d/%d) macroCER %.4f\n",
			100*rep.Accuracy(), rep.Matched, rep.Total, rep.MacroCER)
		if avg, p95, mx, n := eval.LatencyReport(); n > 0 {
			fmt.Printf("latency avg=%.3fms p95=%.3fms max=%.3fms keys=%d\n", avg, p95, mx, n)
		}
		for _, f := range rep.Fails {
			fmt.Printf("  FAIL gold=%q got=%q cer=%.3f\n", f.Gold, f.Got, f.CER)
		}
		return
	}

	if *debug {
		n := 0
		for _, gold := range golds {
			if n >= 3 {
				break
			}
			pinyin, ok := eval.GoldToPinyin(gold, readings)
			if !ok {
				continue
			}
			fmt.Printf("  gold=%q pinyin=%q\n", gold, pinyin)
			openNative().DebugType(pinyin)
			n++
		}
		return
	}

	if *burstTSV != "" {
		eval.InputMode = *input
		var pairs []eval.BurstPair
		var err error
		if *triple {
			pairs, err = eval.LoadTriplePairs(strings.Split(*burstTSV, ","), *burstTopN)
		} else {
			pairs, err = eval.LoadBurstPairs(strings.Split(*burstTSV, ","), *burstTopN)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "load burst pairs:", err)
			os.Exit(1)
		}
		fmt.Printf("burst pairs=%d mode=%s stack=%s\n", len(pairs), *burstMode, *stack)
		if *burstMode == "continuation" || *noFastPath {
			// d4 延续依赖 Go 管线的提交后处理（短窗 lift）；--noFastPath
			// 供 Go 侧特性（如 mixed pair rerank）A/B，快路径会绕过它们。
			eval.EvalFastTypingOff()
		}
		var burstJudge eval.BurstEvaluator
		if *stack == "ime" {
			imeJudge := &eval.IMEJudge{DataDir: *dataDir, UserDir: *userDir + "-ime/rime"}
			if err := imeJudge.Open(); err != nil {
				fmt.Fprintln(os.Stderr, "open ime judge:", err)
				os.Exit(1)
			}
			defer imeJudge.Close()
			burstJudge = imeJudge
		} else {
			burstJudge = openNative()
		}
		if *burstDedupInput && *burstMode == "mixed" && !*triple {
			seen := map[string]bool{}
			deduped := pairs[:0]
			for _, p := range pairs {
				py, ok := eval.GoldToPinyin(p.W1, readings)
				if !ok || p.W2 == "" {
					continue
				}
				w2head := []rune(p.W2)[:1]
				jp, ok2 := eval.GoldToJianpin(string(w2head), readings)
				if !ok2 || jp == "" {
					continue
				}
				in := py + jp
				if seen[in] {
					continue
				}
				seen[in] = true
				deduped = append(deduped, p)
			}
			pairs = deduped
			fmt.Printf("burst dedup: %d pairs remain (unique d3 inputs)\n", len(pairs))
		}
		var rep eval.BurstReport
		if *burstMode == "mixed" && *mixedTop {
			ij, ok := burstJudge.(*eval.IMEJudge)
			if !ok {
				fmt.Fprintln(os.Stderr, "--mixedTop requires --stack ime")
				os.Exit(1)
			}
			rep, h3, hPage := ij.EvaluateMixedTop(pairs, readings)
			fmt.Printf("mixedTop hit@1 %.2f%% (%d/%d) hit@3 %.2f%% (%d/%d) hit@page %.2f%% (%d/%d)\n",
				100*float64(rep.FragmentOK)/float64(rep.Total), rep.FragmentOK, rep.Total,
				100*float64(h3)/float64(rep.Total), h3, rep.Total,
				100*float64(hPage)/float64(rep.Total), hPage, rep.Total)
			return
		}
		if *burstMode == "mixed" {
			rep = burstJudge.EvaluateMixed(pairs, readings)
			fmt.Printf("mixed top-1 %.2f%% (%d/%d)\n",
				100*float64(rep.FragmentOK)/float64(rep.Total), rep.FragmentOK, rep.Total)
		} else if *burstMode == "continuation" {
			rep = burstJudge.EvaluateContinuation(pairs, readings)
			fmt.Printf("continuation hit@1 %.2f%% (%d/%d) hit@3 %.2f%% (%d/%d)\n",
				100*float64(rep.Hit1)/float64(rep.Total), rep.Hit1, rep.Total,
				100*float64(rep.Hit3)/float64(rep.Total), rep.Hit3, rep.Total)
		} else {
			rep = burstJudge.EvaluateFragment(pairs, readings)
			fmt.Printf("fragment top-1 %.2f%% (%d/%d)\n",
				100*float64(rep.FragmentOK)/float64(rep.Total), rep.FragmentOK, rep.Total)
		}
		for _, s := range rep.Samples {
			fmt.Println("  MISS", s)
		}
		return
	}

	eval.InputMode = *input
	rep := openNative().Evaluate(golds, readings, *limit)
	fmt.Printf("top-1 %.2f%% (%d/%d) macroCER %.4f\n",
		100*rep.Accuracy(), rep.Matched, rep.Total, rep.MacroCER)
	if avg, p95, mx, n := eval.LatencyReport(); n > 0 {
		fmt.Printf("latency avg=%.3fms p95=%.3fms max=%.3fms keys=%d\n", avg, p95, mx, n)
	}
	for _, f := range rep.Fails {
		fmt.Printf("  FAIL gold=%q got=%q cer=%.3f\n", f.Gold, f.Got, f.CER)
	}
}
