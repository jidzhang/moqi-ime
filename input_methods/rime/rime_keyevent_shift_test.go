//go:build windows

package rime

import (
	"testing"

	"github.com/gaboolic/moqi-ime/imecore"
)

// keyStatesWith 构造按键状态快照，code→state（GetKeyState 原始值）。
func keyStatesWith(pairs map[int]int) imecore.KeyStates {
	states := make(imecore.KeyStates, 256)
	for code, state := range pairs {
		states[code] = state
	}
	return states
}

// 右 Shift 必须由扫描码识别：VK_SHIFT 左右共用虚拟键，lParam 扫描码
// （左 0x2A / 右 0x36）是唯一可靠的物理身份。
// 曾用 GetKeyState 的 toggle 低位判别——非锁定键该位随每次按下翻转，
// 右 Shift 在引擎眼里一次 Shift_R 一次 Shift_L，配合上游 Shift_R: noop
// 配置，表现为"右 Shift 要按两次才切换中英"。
func TestTranslateKeyCodeIdentifiesShiftSideByScanCode(t *testing.T) {
	cases := []struct {
		name string
		req  *imecore.Request
		want int
	}{
		{
			name: "右Shift按下事件",
			req:  &imecore.Request{KeyCode: vkShift, ScanCode: 0x36},
			want: rimeShiftR,
		},
		{
			name: "左Shift按下事件",
			req:  &imecore.Request{KeyCode: vkShift, ScanCode: 0x2A},
			want: rimeShiftL,
		},
		{
			name: "右Shift释放事件（快照中R已抬起）",
			req: &imecore.Request{KeyCode: vkShift, ScanCode: 0x36,
				KeyStates: keyStatesWith(map[int]int{vkLShift: 0, vkRShift: 0})},
			want: rimeShiftR,
		},
		{
			name: "无扫描码时按住状态兜底为R",
			req: &imecore.Request{KeyCode: vkShift, ScanCode: 0,
				KeyStates: keyStatesWith(map[int]int{vkRShift: 0x80})},
			want: rimeShiftR,
		},
		{
			name: "无扫描码且两侧都按住时视为L",
			req: &imecore.Request{KeyCode: vkShift, ScanCode: 0,
				KeyStates: keyStatesWith(map[int]int{vkLShift: 0x80, vkRShift: 0x80})},
			want: rimeShiftL,
		},
		{
			name: "toggle低位不再参与判别（回归用例：老bug在此输入下误判为R）",
			req: &imecore.Request{KeyCode: vkShift, ScanCode: 0,
				KeyStates: keyStatesWith(map[int]int{vkRShift: 0x01})},
			want: rimeShiftL,
		},
	}
	for _, tc := range cases {
		if got := translateKeyCode(tc.req); got != tc.want {
			t.Errorf("%s: got %d want %d", tc.name, got, tc.want)
		}
	}
}

// Control 左右同扫描码（0x1D），用扩展位区分。
func TestTranslateKeyCodeIdentifiesControlSideByExtendedBit(t *testing.T) {
	cases := []struct {
		name string
		req  *imecore.Request
		want int
	}{
		{
			name: "右Ctrl（0x1D+扩展位）",
			req:  &imecore.Request{KeyCode: vkControl, ScanCode: 0x1D, IsExtended: true},
			want: rimeControlR,
		},
		{
			name: "左Ctrl（0x1D无扩展位）",
			req:  &imecore.Request{KeyCode: vkControl, ScanCode: 0x1D},
			want: rimeControlL,
		},
		{
			name: "toggle低位不再参与判别（回归：老bug在此输入下误判为R）",
			req: &imecore.Request{KeyCode: vkControl, ScanCode: 0,
				KeyStates: keyStatesWith(map[int]int{vkRControl: 0x01})},
			want: rimeControlL,
		},
	}
	for _, tc := range cases {
		if got := translateKeyCode(tc.req); got != tc.want {
			t.Errorf("%s: got %d want %d", tc.name, got, tc.want)
		}
	}
}
