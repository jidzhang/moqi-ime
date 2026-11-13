//go:build windows

// learn-teach: controlled probe of Rime context_reorder corrective learning.
// Natural context -> target chunks are <=10 characters. Separate fresh userDirs
// are used for control and taught arms. Candidate text/rank is read ONLY by the
// taught arm; the scored outcome is a plain top-1 commit in both arms.
// Same-day mechanism probe only; it is not D3/D7 and does not inject OS keys.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gaboolic/moqi-ime/eval"
	rime "github.com/gaboolic/moqi-ime/input_methods/rime"
)

type Pair struct {
	ID            string `json:"id"`
	Source        string `json:"source"`
	Context       string `json:"context"`
	ContextPinyin string `json:"context_pinyin"`
	Target        string `json:"target"`
	TargetPinyin  string `json:"target_pinyin"`
}

type Row struct {
	ID             string `json:"id"`
	Context        string `json:"context"`
	Target         string `json:"target"`
	ContextGot     string `json:"context_committed"`
	ContextOK      bool   `json:"context_ok"`
	BaselineRank   int    `json:"baseline_rank"` // zero-based; -1 means absent
	BaselineTop1   string `json:"baseline_top1,omitempty"`
	TeachCommit    string `json:"teach_commit,omitempty"`
	TeachSelected  bool   `json:"teach_selected"`
	AfterTeachTop1 string `json:"after_teach_top1,omitempty"`
	AfterTeachOK   bool   `json:"after_teach_ok"`
	ControlTop1    string `json:"control_top1,omitempty"`
	ControlOK      bool   `json:"control_ok"`
	Skipped        string `json:"skipped,omitempty"`
	CorrectionNote string `json:"correction_note,omitempty"`
}

type Result struct {
	Arm      string                 `json:"arm"`
	PairFile string                 `json:"pair_file"`
	Probe    int                    `json:"probe"`
	Rows     []Row                  `json:"rows"`
	Summary  map[string]interface{} `json:"summary"`
}

func loadPairs(path string) ([]Pair, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	first := true
	var out []Pair
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if first {
			first = false
			continue
		}
		if line == "" {
			continue
		}
		p := strings.Split(line, "\t")
		if len(p) < 8 {
			return nil, fmt.Errorf("malformed pair row: %q", line)
		}
		if p[6] != "checked" || p[7] != "checked" {
			return nil, fmt.Errorf("pair %s has not passed reading/text audit", p[0])
		}
		if len([]rune(p[2])) > 10 || len([]rune(p[4])) > 10 {
			return nil, fmt.Errorf("pair %s exceeds <=10-character unit limit", p[0])
		}
		out = append(out, Pair{ID: p[0], Source: p[1], Context: p[2], ContextPinyin: p[3], Target: p[4], TargetPinyin: p[5]})
	}
	return out, sc.Err()
}

func keysOf(pinyin string) [][2]int {
	keys := make([][2]int, 0, len(pinyin))
	for i := 0; i < len(pinyin); i++ {
		ch := pinyin[i]
		up := ch
		if ch >= 'a' && ch <= 'z' {
			up = ch - 32
		}
		keys = append(keys, [2]int{int(up), int(ch)})
	}
	return keys
}

func stripMark(s string) string {
	if i := strings.IndexByte(s, ' '); i > 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// resetContext follows the validated 42-PoC reset: commit an invalid ASCII token
// so context_reorder clears transient prev1/prev2 while preserving its userdb.
func resetContext(j *eval.IMEJudge) {
	j.TypeKeysCollect([][2]int{{int('Z'), int('z')}, {0x0D, 0x0D}}) // z + Return
}

func commitContext(j *eval.IMEJudge, p Pair) (string, bool) {
	got, _ := j.Type(p.ContextPinyin)
	got = strings.TrimSpace(got)
	return got, got == p.Context
}

func commitTargetTop1(j *eval.IMEJudge, pinyin string) string {
	got, _ := j.Type(pinyin)
	return strings.TrimSpace(got)
}

func save(out string, r Result) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(out, append(b, '\n'), 0o644)
}

func main() {
	data := flag.String("data", "", "product-aligned Rime shared data")
	user := flag.String("user", "", "isolated Rime userDir")
	pairsFile := flag.String("pairs", "", "LLM-audited context-target TSV")
	out := flag.String("out", "", "result JSON")
	probe := flag.Int("probe", 0, "first N pairs; 0=all")
	onlyID := flag.String("onlyID", "", "run exactly one pair ID")
	noTeach := flag.Bool("noTeach", false, "control arm: measure only, no candidate inspection")
	flag.Parse()
	if *data == "" || *user == "" || *pairsFile == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "required: --data --user --pairs --out")
		os.Exit(2)
	}
	eval.EvalFastTypingOff()
	pairs, err := loadPairs(*pairsFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "load pairs:", err)
		os.Exit(1)
	}
	if *onlyID != "" {
		filtered := pairs[:0]
		for _, p := range pairs {
			if p.ID == *onlyID {
				filtered = append(filtered, p)
			}
		}
		pairs = filtered
		if len(pairs) != 1 {
			fmt.Fprintf(os.Stderr, "onlyID %q matched %d pairs\n", *onlyID, len(pairs))
			os.Exit(2)
		}
	}
	if *probe > 0 && *probe < len(pairs) {
		pairs = pairs[:*probe]
	}
	j := &eval.IMEJudge{DataDir: *data, UserDir: *user}
	if err := j.Open(); err != nil {
		fmt.Fprintln(os.Stderr, "open judge:", err)
		os.Exit(1)
	}
	defer j.Close()
	arm := "taught"
	if *noTeach {
		arm = "control"
	}
	res := Result{Arm: arm, PairFile: filepath.Base(*pairsFile), Probe: *probe}

	for _, p := range pairs {
		row := Row{ID: p.ID, Context: p.Context, Target: p.Target, BaselineRank: -1}
		resetContext(j)
		ctx, ok := commitContext(j, p)
		row.ContextGot, row.ContextOK = ctx, ok
		if !ok {
			row.Skipped = "context_top1_mismatch"
			res.Rows = append(res.Rows, row)
			continue
		}

		if *noTeach {
			// Match the taught arm's context exposure count: after the initial
			// context commit, reset and re-enter context once before scoring.
			resetContext(j)
			ctx, ok = commitContext(j, p)
			if !ok {
				row.Skipped = "context_top1_mismatch_before_control_measure"
				res.Rows = append(res.Rows, row)
				continue
			}
			row.ControlTop1 = commitTargetTop1(j, p.TargetPinyin)
			row.ControlOK = row.ControlTop1 == p.Target
			res.Rows = append(res.Rows, row)
			continue
		}

		// Candidate inspection occurs only in the correction phase.
		cands, _ := j.MenuAfterKeys(keysOf(p.TargetPinyin))
		for i, cand := range cands {
			word := stripMark(cand)
			if i == 0 {
				row.BaselineTop1 = word
			}
			if word == p.Target {
				row.BaselineRank = i
				break
			}
		}
		if row.BaselineRank < 0 {
			row.Skipped = "target_not_on_candidate_page"
			res.Rows = append(res.Rows, row)
			continue
		}
		if row.BaselineRank == 0 {
			// No correction signal is needed, but keep the target in the outcome
			// comparison: this is a valid case that was already correct.
			row.CorrectionNote = "already_top1_no_correction"
		} else {
			seq := append([][2]int{}, keysOf(p.TargetPinyin)...)
			for k := 0; k < row.BaselineRank; k++ {
				seq = append(seq, [2]int{0x28, 0}) // VK_DOWN, zero-based rank
			}
			seq = append(seq, [2]int{0x20, 0x20}) // Space
			commits := j.TypeKeysCollect(seq)
			if len(commits) > 0 {
				row.TeachCommit = strings.TrimSpace(commits[len(commits)-1])
			}
			row.TeachSelected = row.TeachCommit == p.Target
		}

		// Outcome stage: reset transient context, re-enter the gold context,
		// then submit target using plain top-1. No candidate read here.
		resetContext(j)
		ctx, ok = commitContext(j, p)
		if !ok {
			row.Skipped = "context_top1_mismatch_after_teach"
			res.Rows = append(res.Rows, row)
			continue
		}
		row.AfterTeachTop1 = commitTargetTop1(j, p.TargetPinyin)
		row.AfterTeachOK = row.AfterTeachTop1 == p.Target
		res.Rows = append(res.Rows, row)
	}

	valid, ctxBad, selected, controlPass, afterPass, wrongToRight := 0, 0, 0, 0, 0, 0
	for _, r := range res.Rows {
		if r.Skipped == "context_top1_mismatch" || r.Skipped == "context_top1_mismatch_after_teach" || r.Skipped == "context_top1_mismatch_before_control_measure" {
			ctxBad++
			continue
		}
		if r.Skipped == "target_not_on_candidate_page" {
			continue
		}
		valid++
		if r.TeachSelected {
			selected++
		}
		if r.ControlOK {
			controlPass++
		}
		if r.AfterTeachOK {
			afterPass++
			if !r.ControlOK {
				wrongToRight++
			}
		}
	}
	// Export the isolated arm's userdb snapshot for audit; this is post-measurement
	// and cannot influence any scored output.
	userDBSynced := rime.SyncUserData()
	res.Summary = map[string]interface{}{
		"arm": arm, "rows": len(res.Rows), "valid": valid, "context_mismatch": ctxBad,
		"userdb_snapshot_exported": userDBSynced,
		"gold_selected":            selected, "control_pass": controlPass, "after_teach_pass": afterPass,
		"wrong_to_right": wrongToRight,
		"note":           "same-day context-target corrective-learning probe; not D3/D7",
	}
	if *noTeach {
		res.Summary["control_pass"] = controlPass
		res.Summary["after_teach_pass"] = nil
	}
	if err := save(*out, res); err != nil {
		fmt.Fprintln(os.Stderr, "save:", err)
		os.Exit(1)
	}
	fmt.Printf("ARM %s rows=%d valid=%d ctx-mismatch=%d gold-selected=%d control=%d after-teach=%d wrong->right=%d\n",
		arm, len(res.Rows), valid, ctxBad, selected, controlPass, afterPass, wrongToRight)
	fmt.Println("saved:", *out)
}
