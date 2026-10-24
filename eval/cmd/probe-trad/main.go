// probe-trad — 简繁支持功能实测：开 traditionalization 开关打字看输出。
// 用法：probe-trad.exe <dataDir> <userDir> [输入串...]
package main

import (
	"fmt"
	"os"

	"github.com/gaboolic/moqi-ime/eval"
	rime "github.com/gaboolic/moqi-ime/input_methods/rime"
)

func main() {
	judge := &eval.NativeJudge{DataDir: os.Args[1], UserDir: os.Args[2]}
	if err := judge.Open(); err != nil {
		fmt.Println("open:", err)
		os.Exit(1)
	}
	defer judge.Close()
	session := judge.Session()
	for _, py := range os.Args[3:] {
		rime.SetOption(session, "traditionalization", false)
		jian, ok1 := eval.TypeSentence(session, py)
		rime.SetOption(session, "traditionalization", true)
		fan, ok2 := eval.TypeSentence(session, py)
		fmt.Printf("%-16s 简=%q(ok=%v)  繁=%q(ok=%v)\n", py, jian, ok1, fan, ok2)
	}
}
