//go:build windows

// Temporary diagnostic probe: per-key filterKeyDown timing inside one word.
package main

import (
	"fmt"
	"os"
	"time"

	imecore "github.com/gaboolic/moqi-ime/imecore"
	rime "github.com/gaboolic/moqi-ime/input_methods/rime"
)

func main() {
	userRoot := os.Args[1]
	dataDir := os.Args[2]
	delayMs := 120
	if len(os.Args) > 3 {
		fmt.Sscanf(os.Args[3], "%d", &delayMs)
	}
	os.Setenv("APPDATA", userRoot)
	ime, _ := rime.New(&imecore.Client{ID: "bench"}).(*rime.IME)
	if !ime.InitWithDirs(dataDir, userRoot+`\rime`) {
		panic("init failed")
	}
	defer ime.Close()
	rime.JoinMaintenanceThread()

	seq := 0
	req := func(method string, key, char int) time.Duration {
		seq++
		t0 := time.Now()
		ime.HandleRequest(&imecore.Request{Method: method, SeqNum: seq, KeyCode: key, CharCode: char})
		return time.Since(t0)
	}
	upper := func(b byte) byte { return b - 32 }
	pause := time.Duration(delayMs) * time.Millisecond

	req("onActivate", 0, 0)
	for _, ch := range "nihao" {
		k, c := int(upper(byte(ch))), int(ch)
		req("filterKeyDown", k, c)
		req("onKeyDown", k, c)
		req("filterKeyUp", k, c)
		req("onKeyUp", k, c)
	}
	req("onKeyDown", 0x20, 0x20)
	req("filterKeyUp", 0x20, 0x20)
	req("onKeyUp", 0x20, 0x20)

	words := []string{"nihao", "womingtian", "qu", "beijing", "kankan", "zhege", "dongxi", "zenmeyang"}
	for round := 0; round < 4; round++ {
		for _, w := range words {
			fmt.Printf("%-11s", w)
			for _, ch := range w {
				k, c := int(upper(byte(ch))), int(ch)
				d := req("filterKeyDown", k, c)
				fmt.Printf(" %c=%.2f", ch, float64(d.Microseconds())/1000)
				req("onKeyDown", k, c)
				req("filterKeyUp", k, c)
				req("onKeyUp", k, c)
				time.Sleep(pause)
			}
			d := req("onKeyDown", 0x20, 0x20)
			fmt.Printf(" [sp]=%.2f\n", float64(d.Microseconds())/1000)
			req("filterKeyUp", 0x20, 0x20)
			req("onKeyUp", 0x20, 0x20)
			time.Sleep(pause)
		}
	}
}
