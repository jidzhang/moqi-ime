// replay-log：把 usage 日志里的（输入, 实选）重放进真实 Go 管线，统计实选
// 进入第一候选的比例。用法：
//   replay-log -log <usage-YYYY-MM-DD.log> [-data <sharedDataDir>] [-user <freshUserRoot>]
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/gaboolic/moqi-ime/eval"
)

type session struct {
	tokens []string
	first  string
	gold   string
	time   string
}

func isAbbrevToken(t string) bool {
	if len(t) == 2 && (t == "zh" || t == "ch" || t == "sh") {
		return true
	}
	if len(t) == 1 {
		switch t {
		case "a", "e", "o":
			return false
		default:
			return true
		}
	}
	return false
}

func hasCJK(s string) bool {
	for _, r := range s {
		if r >= 0x4E00 && r <= 0x9FFF {
			return true
		}
	}
	return false
}

func extract(path string) []session {
	f, err := os.Open(path)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	data, _ := io.ReadAll(f)
	var out []session
	var cur *session
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(parts) < 5 {
			continue
		}
		switch parts[1] {
		case "cand":
			toks := strings.Fields(parts[2])
			cands := nil2(parts)
			cur = &session{tokens: toks, first: cands, time: parts[0]}
		case "commit":
			gold := strings.TrimSpace(parts[4])
			if cur != nil && gold != "" && hasCJK(gold) && !strings.ContainsAny(gold, " \t") {
				cur.gold = gold
				out = append(out, *cur)
			}
			cur = nil
		}
	}
	return out
}

func nil2(parts []string) string {
	if len(parts) > 3 {
		f := strings.Split(parts[3], "∥")
		if len(f) > 0 {
			return strings.TrimSpace(f[0])
		}
	}
	return ""
}

func main() {
	logPath := flag.String("log", "", "usage 日志路径")
	dataDir := flag.String("data", `C:\Program Files (x86)\MoqiIM\moqi-ime\input_methods\rime\data`, "共享数据目录")
	userRoot := flag.String("user", "", "全新用户根目录（自动建 rime/ 与 Moqi/）")
	gram := flag.String("gram", "on", "语法模型 on/off/noabbrev（noabbrev=gram开+混合组句关）")
	limit := flag.Int("limit", 0, "最多回放条数（0=全部）")
	onlyCls := flag.String("cls", "", "只回放指定类别（full/mixed/abbrev，逗号分隔；空=全部）")
	flag.Parse()
	if *logPath == "" || *userRoot == "" {
		flag.Usage()
		os.Exit(1)
	}
	sessions := extract(*logPath)
	if len(sessions) == 0 {
		fmt.Println("没有可用会话")
		os.Exit(1)
	}
	// prod 臂补丁（与 suite-v2 相同）+ Go 管线 mixed_pair 开
	rimeDir := filepath.Join(*userRoot, "rime")
	moqiDir := filepath.Join(*userRoot, "Moqi")
	os.MkdirAll(rimeDir, 0o755)
	os.MkdirAll(moqiDir, 0o755)
	// gram 开关必须走 local_behavior.json（产品自己的配置入口）。
	// 不能手写 rime_frost.custom.yaml：无 managed 标记的该文件会被产品
	// 拒绝覆盖，开关被架空（2026-10-23 踩坑，AGENTS.md 已记）。
	lb := map[string]string{}
	if *gram == "off" {
		lb["grammar_model"] = "off"
	}
	if *gram == "noabbrev" {
		// 隔离实验：gram 在位，但不给混合输入构建组句候选。
		// 手写无 managed 标记的 custom.yaml，产品会尊重它不覆盖。
		os.WriteFile(filepath.Join(rimeDir, "rime_frost.custom.yaml"), []byte(`patch:
  grammar/language: zh-hans-t-essay-bgw
  "translator/contextual_suggestions": true
  "melt_eng/initial_quality": 0.01
  "reduce_english_filter/mode": all
  "translator/sentence_quality_boost": -2
  "translator/enable_transpose": true
  "translator/transpose_credibility": -8
`), 0o644)
	}
	if lbJSON, err := json.Marshal(lb); err == nil && len(lb) > 0 {
		os.WriteFile(filepath.Join(moqiDir, "local_behavior.json"), lbJSON, 0o644)
	}
	os.WriteFile(filepath.Join(moqiDir, "contextual_lift.json"), []byte(`{ "bigram_rerank": { "mixed_pair": true, "pair_ratio": 999, "pair_floor": 300 } }`+"\n"), 0o644)

	eval.EvalFastTypingOff()
	j := &eval.IMEJudge{DataDir: *dataDir, UserDir: rimeDir}
	if err := j.Open(); err != nil {
		panic(err)
	}
	defer j.Close()

	type miss struct {
		s     session
		rank  int
		first string
	}
	var misses []miss
	stat := map[string]*struct{ total, top1, found int }{}
	limitN := *limit
	for idx, s := range sessions {
		if limitN > 0 && idx >= limitN {
			break
		}
		raw := strings.Join(s.tokens, "")
		if raw == "" || !utf8.ValidString(raw) {
			continue
		}
		cls := "full"
		hasAbbr, hasFull := false, false
		for _, t := range s.tokens {
			if isAbbrevToken(t) {
				hasAbbr = true
			} else {
				hasFull = true
			}
		}
		if hasAbbr && hasFull {
			cls = "mixed"
		} else if hasAbbr {
			cls = "abbrev"
		}
		if len(*onlyCls) > 0 {
			ok := false
			for _, want := range strings.Split(*onlyCls, ",") {
				if cls == want {
					ok = true
				}
			}
			if !ok {
				continue
			}
		}
		st := stat[cls]
		if st == nil {
			st = &struct{ total, top1, found int }{}
			stat[cls] = st
		}
		st.total++
		rank, first := j.ReplayRank(raw, s.gold)
		if rank == 1 {
			st.top1++
		}
		if rank > 0 {
			st.found++
		} else {
			st.total-- // 不在页内的会话单独计，不进 top1 分母
			st.found--
			misses = append(misses, miss{s, -1, first})
			continue
		}
		if rank != 1 {
			misses = append(misses, miss{s, rank, first})
		}
	}
	fmt.Println("==== 重放统计（全新词典 + prod 补丁 + 真实 Go 管线）====")
	for _, cls := range []string{"full", "mixed", "abbrev"} {
		if st := stat[cls]; st != nil && st.total > 0 {
			fmt.Printf("%-6s 总%5d  首选%5d  首选率=%5.1f%%\n", cls, st.total, st.top1, 100*float64(st.top1)/float64(st.total))
		}
	}
	fmt.Printf("==== 未命中 %d 条 ====\n", len(misses))
	n := 0
	for _, m := range misses {
		if m.rank > 0 && m.rank <= 6 || m.rank < 0 {
			fmt.Printf("%s  %-14s 首选:%s  实选:%s  r=%d\n", m.s.time, strings.Join(m.s.tokens, " "), m.first, m.s.gold, m.rank)
			n++
			if n >= 60 {
				break
			}
		}
	}
}
