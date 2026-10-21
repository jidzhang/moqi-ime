//go:build windows

// 用户词典学习污染复现：混合提交 A 输入后，共享候选页的其他输入 B 的
// 首选是否被学过的词顶掉。用法：probe-pollution <dataDir> <userDir> <commit输入> <观察输入>
package main

import (
	"fmt"
	"os"

	"github.com/gaboolic/moqi-ime/eval"
	rime "github.com/gaboolic/moqi-ime/input_methods/rime"
)

func topN(j *eval.IMEJudge, in string, n int) []string {
	keys := make([][2]int, 0, len(in))
	for i := 0; i < len(in); i++ {
		ch := in[i]
		up := ch
		if ch >= 'a' && ch <= 'z' {
			up = ch - 32
		}
		keys = append(keys, [2]int{int(up), int(ch)})
	}
	cands, _ := j.MenuAfterKeys(keys)
	if len(cands) > n {
		cands = cands[:n]
	}
	return cands
}

func main() {
	dataDir, userDir := os.Args[1], os.Args[2]
	commitIn, watchIn := os.Args[3], os.Args[4]
	rime.SetDebugLoggingEnabled(true)
	j := &eval.IMEJudge{DataDir: dataDir, UserDir: userDir}
	if err := j.Open(); err != nil {
		panic(err)
	}
	defer j.Close()
	fmt.Printf("BEFORE %s: %q\n", watchIn, topN(j, watchIn, 5))
	got, ok := j.Type(commitIn)
	fmt.Printf("COMMIT %s -> %q ok=%v\n", commitIn, got, ok)
	fmt.Printf("AFTER  %s: %q\n", watchIn, topN(j, watchIn, 5))
	got2, _ := j.Type(commitIn)
	fmt.Printf("COMMIT2 %s -> %q (二次提交应命中学习)\n", commitIn, got2)
}
