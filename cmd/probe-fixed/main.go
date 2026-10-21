//go:build windows

// Corrected latency probe: every key (including Space) goes through filterKeyDown,
// which is the only path that feeds the engine. onKeyDown just reads the result.
package main

import (
	"fmt"
	"os"
	"sort"
	"time"

	imecore "github.com/gaboolic/moqi-ime/imecore"
	rime "github.com/gaboolic/moqi-ime/input_methods/rime"
)

type stat struct {
	name  string
	n     int
	total time.Duration
	max   time.Duration
	over  int
	vals  []float64
}

func (s *stat) add(d time.Duration) {
	ms := float64(d.Microseconds()) / 1000
	s.n++
	s.total += d
	s.vals = append(s.vals, ms)
	if d > s.max {
		s.max = d
	}
	if d >= 30*time.Millisecond {
		s.over++
	}
}

func (s *stat) report() {
	if s.n == 0 {
		return
	}
	sort.Float64s(s.vals)
	p := func(q float64) float64 {
		i := int(q * float64(len(s.vals)-1))
		return s.vals[i]
	}
	fmt.Printf("%-14s n=%4d mean=%6.2f p50=%6.2f p90=%6.2f p99=%6.2f max=%6.2f >=30ms=%d\n",
		s.name, s.n, float64(s.total.Microseconds())/float64(s.n)/1000,
		p(0.50), p(0.90), p(0.99),
		float64(s.max.Microseconds())/1000, s.over)
}

func main() {
	userRoot := os.Args[1]
	dataDir := os.Args[2]
	delayMs := 120
	if len(os.Args) > 3 {
		fmt.Sscanf(os.Args[3], "%d", &delayMs)
	}
	mode := "human"
	if len(os.Args) > 4 {
		mode = os.Args[4]
	}
	os.Setenv("APPDATA", userRoot)
	ime, _ := rime.New(&imecore.Client{ID: "bench"}).(*rime.IME)
	if !ime.InitWithDirs(dataDir, userRoot+`\rime`) {
		panic("init failed")
	}
	defer ime.Close()
	rime.JoinMaintenanceThread()

	seq := 0
	req := func(method string, key, char int) (*imecore.Response, time.Duration) {
		seq++
		t0 := time.Now()
		resp := ime.HandleRequest(&imecore.Request{Method: method, SeqNum: seq, KeyCode: key, CharCode: char})
		return resp, time.Since(t0)
	}
	upper := func(b byte) byte { return b - 32 }
	pause := time.Duration(delayMs) * time.Millisecond

	fd := &stat{name: "filterKeyDown"}
	od := &stat{name: "onKeyDown"}
	fu := &stat{name: "filterKeyUp"}
	ou := &stat{name: "onKeyUp"}
	spaceFD := &stat{name: "space.filterDown"}

	req("onActivate", 0, 0)
	// warmup
	for _, ch := range "nihao" {
		k, c := int(upper(byte(ch))), int(ch)
		req("filterKeyDown", k, c)
		req("onKeyDown", k, c)
		req("filterKeyUp", k, c)
		req("onKeyUp", k, c)
	}
	resp, _ := req("filterKeyDown", 0x20, 0x20)
	req("onKeyDown", 0x20, 0x20)
	fmt.Printf("warmup space commit=%q comp=%q ret=%d\n", resp.CommitString, resp.CompositionString, resp.ReturnValue)
	req("filterKeyUp", 0x20, 0x20)
	req("onKeyUp", 0x20, 0x20)

	words := []string{"nihao", "womingtian", "qu", "beijing", "kankan", "zhege", "dongxi", "zenmeyang"}
	rounds := 6
	if mode == "burst" {
		rounds = 2
		pause = 0
	}
	commits := 0
	for round := 0; round < rounds; round++ {
		for _, w := range words {
			for _, ch := range w {
				k, c := int(upper(byte(ch))), int(ch)
				_, d := req("filterKeyDown", k, c)
				fd.add(d)
				_, d = req("onKeyDown", k, c)
				od.add(d)
				_, d = req("filterKeyUp", k, c)
				fu.add(d)
				_, d = req("onKeyUp", k, c)
				ou.add(d)
				time.Sleep(pause)
			}
			_, d := req("filterKeyDown", 0x20, 0x20)
			spaceFD.add(d)
			resp, _ = req("onKeyDown", 0x20, 0x20)
			od.add(d)
			if resp.CommitString != "" {
				commits++
			}
			if round == 0 && resp.CompositionString != "" {
				fmt.Printf("WARN composition not cleared: %q commit=%q\n", resp.CompositionString, resp.CommitString)
			}
			req("filterKeyUp", 0x20, 0x20)
			req("onKeyUp", 0x20, 0x20)
			time.Sleep(pause)
		}
	}

	fmt.Printf("mode=%s delay=%dms words=%d commits=%d/%d\n", mode, delayMs, fd.n, commits, rounds*len(words))
	fd.report()
	spaceFD.report()
	od.report()
	fu.report()
	ou.report()
}
