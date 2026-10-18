//go:build windows

package eval

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gaboolic/moqi-ime/imecore"
	rime "github.com/gaboolic/moqi-ime/input_methods/rime"
)

// IMEJudge 全栈裁判：请求走 rime.IME 完整 Go 管线（含 Go 侧重排/lift/
// 配置加载），比 NativeJudge（直打 rime.dll）多测一层真实运行时。
//
// 布局要求：rime.dll 在 data 同级目录；Go 侧配置（contextual_lift.json、
// local_behavior.json 等）从 APPDATA 读，因此 judge 把 APPDATA 指到
// user dir 的父目录（user dir 建为 %APPDATA%\MoqiIM）。
type IMEJudge struct {
	DataDir string
	UserDir string
	ime     *rime.IME
	oldAPP  string
}

// Open 初始化全栈 IME。
func (j *IMEJudge) Open() error {
	j.oldAPP = os.Getenv("APPDATA")
	if err := os.MkdirAll(filepath.Dir(j.UserDir), 0o755); err != nil {
		return err
	}
	_ = os.Setenv("APPDATA", filepath.Dir(j.UserDir))

	ime, ok := rime.New(&imecore.Client{ID: "eval"}).(*rime.IME)
	if !ok {
		return fmt.Errorf("rime.New returned unexpected type")
	}
	if !ime.InitWithDirs(j.DataDir, j.UserDir) {
		return fmt.Errorf("InitWithDirs failed dataDir=%q userDir=%q", j.DataDir, j.UserDir)
	}
	rime.JoinMaintenanceThread()
	j.ime = ime
	// IME 的 onActivate 建链（真实 C++ 激活路径）。
	j.ime.HandleRequest(&imecore.Request{Method: "onActivate", SeqNum: 1})
	return nil
}

// Close 恢复环境并收尾。
func (j *IMEJudge) Close() {
	if j.ime != nil {
		j.ime.Close()
		j.ime = nil
	}
	rime.Finalize()
	_ = os.Setenv("APPDATA", j.oldAPP)
}

var imeSeq int

func nextIMESeq() int {
	imeSeq++
	return imeSeq
}

// sendKey 模拟真实 C++ 宿主的完整按键序列
// filterKeyDown→onKeyDown→filterKeyUp→onKeyUp。keyUp 不能省：
// Go 侧 filterKeyUp 负责清 lastKeyDownCode，缺了会让下一个词的首字母
// 与本键相同时被同键去重吞掉，拼音串损坏。
func (j *IMEJudge) sendKey(keyCode, charCode int) *imecore.Response {
	req := &imecore.Request{
		Method:   "filterKeyDown",
		SeqNum:   nextIMESeq(),
		KeyCode:  keyCode,
		CharCode: charCode,
	}
	start := time.Now()
	filterResp := j.ime.HandleRequest(req)
	RecordKeyLatency(time.Since(start))
	var downResp *imecore.Response
	if filterResp.ReturnValue == 1 {
		downResp = j.ime.HandleRequest(&imecore.Request{
			Method:   "onKeyDown",
			SeqNum:   nextIMESeq(),
			KeyCode:  keyCode,
			CharCode: charCode,
		})
	} else {
		downResp = filterResp
	}
	j.ime.HandleRequest(&imecore.Request{Method: "filterKeyUp", SeqNum: nextIMESeq(), KeyCode: keyCode, CharCode: charCode})
	j.ime.HandleRequest(&imecore.Request{Method: "onKeyUp", SeqNum: nextIMESeq(), KeyCode: keyCode, CharCode: charCode})
	return downResp
}

// Type 打一句：逐字母 + 空格提交，返回 commit 文本。
// 先清组字串：词对评估中上一轮的单字母会残留在 composition 里，
// 不清掉会把本句拼音接在残串后面，commit 全错。
func (j *IMEJudge) Type(pinyin string) (string, bool) {
	if sid := j.ime.DebugSessionID(); sid != 0 {
		rime.ClearComposition(sid)
	}
	for _, ch := range pinyin {
		j.sendKey(int(upper(byte(ch))), int(ch))
	}
	// 空格只发 down（区分试验：字母发全序列、空格不发 keyUp）。
	resp := j.sendKeyDownOnly(0x20, 0x20)
	if resp == nil {
		return "", false
	}
	if resp.CommitString != "" {
		return resp.CommitString, true
	}
	// 空格未提交时再拉一次当前状态（部分路径 commit 在下一次响应返回）。
	resp = j.ime.HandleRequest(&imecore.Request{Method: "onKeyDown", SeqNum: nextIMESeq(), KeyCode: 0x20, CharCode: 0x20})
	if resp != nil && resp.CommitString != "" {
		return resp.CommitString, true
	}
	return "", false
}

// sendKeyDownOnly 只发 filterKeyDown→onKeyDown（诊断变体用）。
func (j *IMEJudge) sendKeyDownOnly(keyCode, charCode int) *imecore.Response {
	filterResp := j.ime.HandleRequest(&imecore.Request{Method: "filterKeyDown", SeqNum: nextIMESeq(), KeyCode: keyCode, CharCode: charCode})
	if filterResp.ReturnValue != 1 {
		return filterResp
	}
	return j.ime.HandleRequest(&imecore.Request{Method: "onKeyDown", SeqNum: nextIMESeq(), KeyCode: keyCode, CharCode: charCode})
}

// MenuAfter 单字母场景：返回当前首三候选（不打空格）。
func (j *IMEJudge) MenuAfter(letter byte) []string {
	j.sendKey(int(upper(letter)), int(letter))
	req := &imecore.Request{Method: "onKeyDown", SeqNum: nextIMESeq(), KeyCode: int(upper(letter)), CharCode: int(letter)}
	resp := j.ime.HandleRequest(req)
	if resp == nil {
		return nil
	}
	return resp.CandidateList
}

func upper(b byte) byte {
	if b >= 'a' && b <= 'z' {
		return b - 32
	}
	return b
}

// EvaluateFragment / EvaluateContinuation 与 NativeJudge 同口径，
// 但走全栈管线。
func (j *IMEJudge) EvaluateFragment(pairs []BurstPair, readings CharReadings) BurstReport {
	rep := BurstReport{}
	for _, p := range pairs {
		gold := p.W1 + p.W2
		py, ok := KeysForGold(gold, readings)
		if !ok {
			continue
		}
		got, ok := j.Type(py)
		rep.Total++
		if ok && normalize(got) == normalize(gold) {
			rep.FragmentOK++
		} else if len(rep.Samples) < 15 {
			rep.Samples = append(rep.Samples, fmt.Sprintf("gold=%s(%s) got=%s", gold, py, got))
		}
	}
	return rep
}

func (j *IMEJudge) EvaluateContinuation(pairs []BurstPair, readings CharReadings) BurstReport {
	rep := BurstReport{}
	for _, p := range pairs {
		py1, ok1 := KeysForGold(p.W1, readings)
		py2, ok2 := KeysForGold(p.W2, readings)
		if !ok1 || !ok2 || py2 == "" {
			continue
		}
		if _, ok := j.Type(py1); !ok {
			continue
		}
		cands := j.MenuAfter(py2[0])
		rep.Total++
		wantFirst := string([]rune(p.W2)[0])
		hit := false
		for i, text := range cands {
			if text == "" {
				continue
			}
			runes := []rune(text)
			if string(runes[0]) == wantFirst {
				hit = true
				if i == 0 {
					rep.Hit1++
				}
				break
			}
			if i >= 2 {
				break
			}
		}
		if hit {
			rep.Hit3++
		} else if len(rep.Samples) < 15 {
			first := ""
			if len(cands) > 0 {
				first = cands[0]
			}
			rep.Samples = append(rep.Samples,
				fmt.Sprintf("%s+%s → want %q got %q", p.W1, string(py2[0]), wantFirst, first))
		}
	}
	return rep
}

// MenuAfterDetailed 单字母后同时读 Go 响应候选与 dll 菜单（诊断分歧用）。
func (j *IMEJudge) MenuAfterDetailed(letter byte) (respList, menuList []string) {
	j.sendKey(int(upper(letter)), int(letter))
	req := &imecore.Request{Method: "onKeyDown", SeqNum: nextIMESeq(), KeyCode: int(upper(letter)), CharCode: int(letter)}
	resp := j.ime.HandleRequest(req)
	respList = resp.CandidateList
	if sid := j.ime.DebugSessionID(); sid != 0 {
		if menu, ok := rime.GetMenu(sid); ok {
			for i := 0; i < menu.NumCandidates && i < 3; i++ {
				menuList = append(menuList, menu.Candidates[i].Text)
			}
		}
	}
	return respList, menuList
}

// Evaluate 整句评估（与 NativeJudge.Evaluate 同口径，但走全栈管线）。
func (j *IMEJudge) Evaluate(golds []string, readings CharReadings, limit int) Report {
	var results []SentenceResult
	n := 0
	for _, gold := range golds {
		if limit > 0 && n >= limit {
			break
		}
		pinyin, ok := KeysForGold(gold, readings)
		if !ok {
			continue
		}
		got, ok := j.Type(pinyin)
		if !ok {
			got = ""
		}
		if gold == "" {
			continue
		}
		results = append(results, ScoreSentence(gold, got))
		n++
	}
	return Summarize(results, 20)
}

// EvaluateMixed 与 NativeJudge.EvaluateMixed 同口径（一次组字连打），走全栈管线。
func (j *IMEJudge) EvaluateMixed(pairs []BurstPair, readings CharReadings) BurstReport {
	rep := BurstReport{}
	for _, p := range pairs {
		py1, ok1 := GoldToPinyin(p.W1, readings)
		jp2, ok2 := GoldToJianpin(p.W2, readings)
		if !ok1 || !ok2 || jp2 == "" {
			continue
		}
		got, ok := j.Type(py1 + jp2)
		rep.Total++
		if ok && normalize(got) == normalize(p.W1+p.W2) {
			rep.FragmentOK++
		} else if len(rep.Samples) < 15 {
			rep.Samples = append(rep.Samples,
				fmt.Sprintf("%s|%s 输 %s%s → want %q got %q", p.W1, p.W2, py1, jp2, p.W1+p.W2, got))
		}
	}
	return rep
}
