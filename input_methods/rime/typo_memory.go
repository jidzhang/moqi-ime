// typo_memory.go：错字自适应记忆（谷歌拼音"用一周变舒服"的同款机制）。
//
// 机理：用户以带错字的按键串（邻键/多打/漏打）打出某词并选中后，把
// (raw input → 上屏词) 记进用户目录 TSV。下次同一 raw input 出现时，
// 若记忆词已在候选里（二遍拼接通常能送进第一页），把它提到第 1 位——
// 原第 1 顺延，不新增候选、不参与引擎打分，主列表零结构风险。
//
// 数据面：TSV 三列 count\tRAW\tTEXT，按 count 降序，上限 typoMemoryMaxEntries。
// 写入：提交路径记忆（内存 map 常驻 + 阈值落盘）；读取：启动载入 +
// 每次提交后异步刷新（增量 merge，TTL 内同一文件只读一次）。
package rime

import (
	"bufio"
	"github.com/gaboolic/moqi-ime/imecore"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	typoMemoryFileName    = "typo_memory.tsv"
	typoMemoryMaxEntries  = 5000
	typoMemoryMinRawLen   = 3 // 过短 raw（"wo"类）歧义面太大，不记不提
	typoMemoryMinTextRune = 1 // 单字也记（"的的"类错法高频）
	typoMemorySaveEvery   = 8 // 累计 N 次新记/命中即落盘
	typoMemoryReloadEvery = 5 * time.Minute
)

type typoMemoryEntry struct {
	count int
	text  string
}

type typoMemoryStore struct {
	mu          sync.Mutex
	entries     map[string]*typoMemoryEntry // raw → entry（同 raw 多词取最高 count，TSV 每行一词）
	order       []string                    // count 降序维护（持久化顺序）
	dirty       int                         // 未落盘变更数
	lastLoaded  time.Time
	loadedMtime time.Time
}

var typoMemory = &typoMemoryStore{entries: map[string]*typoMemoryEntry{}}

func typoMemoryPath() string {
	dir := moqiAppDataDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, typoMemoryFileName)
}

// load 读盘；TTL 内跳过（mtime 未变时即使过 TTL 也不重读）。
func (t *typoMemoryStore) load() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if time.Since(t.lastLoaded) < typoMemoryReloadEvery {
		return
	}
	t.lastLoaded = time.Now()
	path := typoMemoryPath()
	if path == "" {
		return
	}
	st, err := os.Stat(path)
	if err != nil {
		return
	}
	if !t.loadedMtime.IsZero() && !st.ModTime().After(t.loadedMtime) {
		return // 别的进程没写过，内存即最新
	}
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	entries := map[string]*typoMemoryEntry{}
	var order []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		parts := strings.Split(line, "\t")
		if len(parts) != 3 {
			continue
		}
		n, err := strconv.Atoi(parts[0])
		if err != nil || n <= 0 || parts[1] == "" || parts[2] == "" {
			continue
		}
		raw := parts[1]
		if old, ok := entries[raw]; !ok || n > old.count {
			entries[raw] = &typoMemoryEntry{count: n, text: parts[2]}
			order = append(order, raw)
		}
	}
	t.entries = entries
	t.order = order
	t.loadedMtime = st.ModTime()
	t.dirty = 0
}

// save 全量重写（条目量级 5e3，毫秒级；仅 dirty 达阈值或进程退出时）。
func (t *typoMemoryStore) save() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.saveLocked()
}

func (t *typoMemoryStore) saveLocked() {
	path := typoMemoryPath()
	if path == "" || len(t.entries) == 0 {
		return
	}
	type row struct {
		raw   string
		entry *typoMemoryEntry
	}
	rows := make([]row, 0, len(t.entries))
	for raw, e := range t.entries {
		rows = append(rows, row{raw, e})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].entry.count != rows[j].entry.count {
			return rows[i].entry.count > rows[j].entry.count
		}
		return rows[i].raw < rows[j].raw
	})
	if len(rows) > typoMemoryMaxEntries {
		rows = rows[:typoMemoryMaxEntries]
	}
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(strconv.Itoa(r.entry.count))
		b.WriteByte('\t')
		b.WriteString(r.raw)
		b.WriteByte('\t')
		b.WriteString(r.entry.text)
		b.WriteByte('\n')
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return
	}
	if st, err := os.Stat(path); err == nil {
		t.loadedMtime = st.ModTime()
	}
	t.dirty = 0
}

// record 提交路径调用：raw 非空且与 text 不同才记（相同=正常输入，无信息量）。
func (t *typoMemoryStore) record(raw, text string) {
	raw = strings.TrimSpace(raw)
	text = strings.TrimSpace(text)
	if raw == "" || text == "" {
		return
	}
	// raw 必须是纯小写字母串：混合输入/符号输入不是"错字"场景
	//（测试实证：raw==text 判断不成立，纯拼音与汉字永不相等）。
	for _, r := range raw {
		if r < 'a' || r > 'z' {
			return
		}
	}
	if len(raw) < typoMemoryMinRawLen {
		return
	}
	if runeCount(text) < typoMemoryMinTextRune {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if e, ok := t.entries[raw]; ok {
		if e.text == text {
			e.count++
		} else {
			// 同一 raw 用户改选了别的词：跟随最新选择（count 减半防抖动）
			e.text = text
			e.count = e.count/2 + 1
		}
		t.dirty++
	} else {
		t.entries[raw] = &typoMemoryEntry{count: 1, text: text}
		t.dirty++
	}
	if t.dirty >= typoMemorySaveEvery {
		t.saveLocked()
	}
}

// lookup 返回记忆词；未命中返回 ""。
func (t *typoMemoryStore) lookup(raw string) string {
	t.load()
	t.mu.Lock()
	defer t.mu.Unlock()
	if e, ok := t.entries[strings.TrimSpace(raw)]; ok {
		return e.text
	}
	return ""
}

// candidateTexts 提取候选文本（无依赖的窄接口，便于测试）。
func candidateTexts(cands []candidateItem) []string {
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.Text)
	}
	return out
}

// hoistCandidate 把 remembered 提到第 1 位（原第 1 顺延），其余顺序不动。
// 未命中或已在第 1 位时原样返回（调用方判断 changed 用指针相等）。
func hoistCandidate(list []string, remembered string) []string {
	if len(list) < 2 || list[0] == remembered {
		return list
	}
	idx := -1
	for i, text := range list {
		if text == remembered {
			idx = i
			break
		}
	}
	if idx <= 0 {
		return list
	}
	out := make([]string, len(list))
	out[0] = remembered
	copy(out[1:], append(list[:idx:idx], list[idx+1:]...))
	return out
}

func hoistCandidateEntry(entries []imecore.CandidateEntry, remembered string) []imecore.CandidateEntry {
	if len(entries) < 2 || entries[0].Text == remembered {
		return entries
	}
	idx := -1
	for i, e := range entries {
		if e.Text == remembered {
			idx = i
			break
		}
	}
	if idx <= 0 {
		return entries
	}
	out := make([]imecore.CandidateEntry, len(entries))
	out[0] = entries[idx]
	copy(out[1:], append(entries[:idx:idx], entries[idx+1:]...))
	return out
}

// recordTypoMemory 提交路径调用：用 raw input 与上屏词配记忆。
// raw 优先取当前跟踪值（选中候选时仍活跃），回退到提交按键上的快照
//（空格/回车提交时跟踪值已清零）。
func (ime *IME) recordTypoMemory(commit string) {
	raw := ime.rawInputTracked
	if raw == "" {
		raw = ime.rawInputAtCommit
	}
	ime.rawInputAtCommit = ""
	if raw == "" {
		return
	}
	typoMemory.record(raw, commit)
}

func runeCount(s string) int { return len([]rune(s)) }
