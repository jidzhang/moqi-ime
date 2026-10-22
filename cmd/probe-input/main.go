//go:build windows

// 单输入显微镜：按给定按键串输入，打印候选页与引擎原始顺序。
// 用法：probe-input <dataDir> <userDir> <keys> [预提交键串]
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/gaboolic/moqi-ime/eval"
)

func keysOf(in string) [][2]int {
	out := make([][2]int, 0, len(in)+1)
	for i := 0; i < len(in); i++ {
		up := in[i]
		if up >= 'a' && up <= 'z' {
			up -= 32
		}
		out = append(out, [2]int{int(up), int(in[i])})
	}
	return out
}

func main() {
	dataDir, userDir, input := os.Args[1], os.Args[2], os.Args[3]
	j := &eval.IMEJudge{DataDir: dataDir, UserDir: userDir}
	if err := j.Open(); err != nil {
		panic(err)
	}
	defer j.Close()
	if len(os.Args) > 4 {
		if got, ok := j.Type(os.Args[4]); ok {
			fmt.Printf("预提交 %q -> %q\n", os.Args[4], got)
		}
	}
	cands, compo := j.MenuAfterKeys(keysOf(input))
	fmt.Printf("输入 %q → 组字=%q\n  %s\n", input, compo, strings.Join(cands, "∥"))
	// 可选第 5 参数：按数字键选择，打印实际上屏（验证显示/引擎索引对齐）。
	if len(os.Args) > 5 {
		var idx int
		fmt.Sscanf(os.Args[5], "%d", &idx)
		got, ok := j.TypeDigits(input, idx)
		rankWord := ""
		if idx-1 < len(cands) {
			rankWord = cands[idx-1]
		}
		fmt.Printf("按数字%d（显示第%d名=%q）→ 上屏 %q ok=%v\n", idx, idx, rankWord, got, ok)
	}
}
