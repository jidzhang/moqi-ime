//go:build windows

// 真实日志回放：逐键重放用户输入（不提交、不学习），测量实选词的候选位次。
package eval

import (
	"github.com/gaboolic/moqi-ime/imecore"
	"github.com/gaboolic/moqi-ime/input_methods/rime"
)

// ReplayRank 逐键输入 pinyin（不发空格、不提交），返回 gold 在候选页中的
// 位次（1 基；0=不在页内），并给出当前首候选文本。用于把用户真实打字日志
// 变成回放测评维度（输入+实选=gold）。
func (j *IMEJudge) ReplayRank(pinyin, gold string) (int, string) {
	if sid := j.ime.DebugSessionID(); sid != 0 {
		rime.ClearComposition(sid)
	}
	var resp *imecore.Response
	for _, ch := range pinyin {
		resp = j.sendKey(int(upper(byte(ch))), int(ch))
	}
	if resp == nil || len(resp.CandidateEntries) == 0 {
		return 0, ""
	}
	first := resp.CandidateEntries[0].Text
	for i, e := range resp.CandidateEntries {
		if e.Text == gold {
			return i + 1, first
		}
	}
	return 0, first
}
