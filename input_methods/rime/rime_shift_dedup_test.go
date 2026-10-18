package rime

import (
	"testing"

	"github.com/gaboolic/moqi-ime/imecore"
)

// 回归背景：短按 Shift 切中英"时不时失效"。同键去重（lastKeySkip）与 up/down
// 配对脱钩，一旦某个 Shift keyup 在 TSF 链路丢失（焦点切换、应用不转发 keyup），
// 后续 Shift keydown 会被按对吞掉，第 2、3 次按键无效、第 4 次才恢复。
// 修复后裸 Shift 豁免同键去重：每次物理按下都必须到达引擎。

func shiftDownReq(seq int) *imecore.Request {
	return &imecore.Request{SeqNum: seq, KeyCode: vkShift}
}

func shiftUpReq(seq int) *imecore.Request {
	return &imecore.Request{SeqNum: seq, KeyCode: vkShift}
}

func TestBareShiftTogglesSurviveLostKeyUp(t *testing.T) {
	ime := newIsolatedTestIME(t)
	backend := ime.backend.(*testBackend)

	seq := 0
	sendDown := func() {
		seq++
		ime.filterKeyDown(shiftDownReq(seq), imecore.NewResponse(seq, true))
	}
	sendUp := func() {
		seq++
		ime.filterKeyUp(shiftUpReq(seq), imecore.NewResponse(seq, true))
	}

	// 4 次短按 Shift，第 1 次的 keyup 丢失（真实场景：焦点切换吃掉 keyup）。
	sendDown() // down1：到达引擎
	// up1 丢失
	sendDown() // down2：必须到达引擎（修复前被去重吞掉）
	sendUp()   // up2：翻转
	sendDown() // down3：必须到达引擎（修复前被去重吞掉）
	sendUp()   // up3：修复前被 up 去重吞掉，不翻转
	sendDown() // down4：到达引擎
	sendUp()   // up4：翻转

	// down1/down2/down3/down4 + up2/up3/up4 全部到达引擎。
	if got := len(backend.translatedKeyCodes); got != 7 {
		t.Fatalf("expected 7 shift key events to reach backend, got %d", got)
	}
	// 3 次 keyup 翻转，ascii_mode 从 false 变 true。
	if !backend.GetOption("ascii_mode") {
		t.Fatal("expected ascii_mode toggled to true after 3 shift releases")
	}
}

func TestBareShiftDoubleDeliveredKeyUpReachesEngine(t *testing.T) {
	ime := newIsolatedTestIME(t)
	backend := ime.backend.(*testBackend)

	ime.filterKeyDown(shiftDownReq(1), imecore.NewResponse(1, true))
	ime.filterKeyUp(shiftUpReq(2), imecore.NewResponse(2, true))
	ime.filterKeyUp(shiftUpReq(3), imecore.NewResponse(3, true))

	// 双发的 keyup 不被去重吞掉；真实引擎里未按下状态的多余 release 是空操作。
	if got := len(backend.translatedKeyCodes); got != 3 {
		t.Fatalf("expected down+up+up to reach backend, got %d", got)
	}
}

func TestSameKeyDedupStillAppliesToNonModifierKeys(t *testing.T) {
	ime := newIsolatedTestIME(t)
	backend := ime.backend.(*testBackend)

	ime.filterKeyDown(&imecore.Request{SeqNum: 1, KeyCode: 0x4E, CharCode: 'n'}, imecore.NewResponse(1, true))
	ime.filterKeyDown(&imecore.Request{SeqNum: 2, KeyCode: 0x4E, CharCode: 'n'}, imecore.NewResponse(2, true))

	// 非修饰键的同键去重保持原行为：第二个 keydown 是重复事件，不到引擎。
	if got := len(backend.translatedKeyCodes); got != 1 {
		t.Fatalf("expected duplicate keydown to stay deduped, got %d backend calls", got)
	}
}

func TestCapsLockKeyDownDedupKept(t *testing.T) {
	ime := newIsolatedTestIME(t)
	backend := ime.backend.(*testBackend)

	ime.filterKeyDown(&imecore.Request{SeqNum: 1, KeyCode: vkCapital}, imecore.NewResponse(1, true))
	ime.filterKeyDown(&imecore.Request{SeqNum: 2, KeyCode: vkCapital}, imecore.NewResponse(2, true))

	// Caps 在 keydown 翻转，双发去重必须保留，否则会被双发事件翻转两次。
	if got := len(backend.translatedKeyCodes); got != 1 {
		t.Fatalf("expected duplicate caps keydown to stay deduped, got %d backend calls", got)
	}
	if !backend.GetOption("ascii_mode") {
		t.Fatal("expected ascii_mode toggled exactly once by caps keydown")
	}
}
