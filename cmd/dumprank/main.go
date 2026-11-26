//go:build windows

// 排位探针:打印输入串所有页候选,报告目标词全局排位。
// 用法: go run dump-rank.go <pinyin> <want1> [want2...]
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/gaboolic/moqi-ime/eval"
	"github.com/gaboolic/moqi-ime/input_methods/rime"
)

func main() {
	self, _ := os.Executable()
	here := filepath.Dir(self)
	j := &eval.NativeJudge{
		DataDir: filepath.Join(here, "data"),
		UserDir: filepath.Join(here, "dbg-graph"),
	}
	if err := j.Open(); err != nil {
		panic(err)
	}
	defer j.Close()
	s := j.Session()
	py := os.Args[1]
	wants := os.Args[2:]
	rime.ClearComposition(s)
	for _, ch := range py {
		rime.ProcessKey(s, int(ch), 0)
	}
	fmt.Printf("== %s\n", py)
	rank := 0
	for page := 0; page < 30; page++ {
		menu, ok := rime.GetMenu(s)
		if !ok || len(menu.Candidates) == 0 {
			break
		}
		for _, c := range menu.Candidates {
			rank++
			mark := ""
			for _, w := range wants {
				if c.Text == w {
					mark = "   <<< WANT"
				}
			}
			fmt.Printf("  #%d %q%s\n", rank, c.Text, mark)
		}
		if !rime.ChangePage(s, false) {
			break
		}
	}
}
