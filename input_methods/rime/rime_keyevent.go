package rime

import "github.com/gaboolic/moqi-ime/imecore"

const (
	voidSymbol = 0xFFFFFF

	rimeSpace      = 0x020
	rimeBackSpace  = 0xFF08
	rimeTab        = 0xFF09
	rimeClear      = 0xFF0B
	rimeReturn     = 0xFF0D
	rimePause      = 0xFF13
	rimeCapsLock   = 0xFFE5
	rimeEscape     = 0xFF1B
	rimePrior      = 0xFF55
	rimeNext       = 0xFF56
	rimeEnd        = 0xFF57
	rimeHome       = 0xFF50
	rimeLeft       = 0xFF51
	rimeUp         = 0xFF52
	rimeRight      = 0xFF53
	rimeDown       = 0xFF54
	rimeSelect     = 0xFF60
	rimePrint      = 0xFF61
	rimeExecute    = 0xFF62
	rimeInsert     = 0xFF63
	rimeDelete     = 0xFFFF
	rimeHelp       = 0xFF6A
	rimeMetaL      = 0xFFE7
	rimeMetaR      = 0xFFE8
	rimeNumLock    = 0xFF7F
	rimeScrollLock = 0xFF14
	rimeShiftL     = 0xFFE1
	rimeShiftR     = 0xFFE2
	rimeControlL   = 0xFFE3
	rimeControlR   = 0xFFE4
	rimeAltL       = 0xFFE9
	rimeAltR       = 0xFFEA
	rimeF1         = 0xFFBE

	shiftMask   = 1 << 0
	lockMask    = 1 << 1
	controlMask = 1 << 2
	altMask     = 1 << 3
	releaseMask = 1 << 30

	vkBack      = 0x08
	vkTab       = 0x09
	vkClear     = 0x0C
	vkReturn    = 0x0D
	vkShift     = 0x10
	vkControl   = 0x11
	vkMenu      = 0x12
	vkPause     = 0x13
	vkCapital   = 0x14
	vkEscape    = 0x1B
	vkSpace     = 0x20
	vkPrior     = 0x21
	vkNext      = 0x22
	vkEnd       = 0x23
	vkHome      = 0x24
	vkLeft      = 0x25
	vkUp        = 0x26
	vkRight     = 0x27
	vkDown      = 0x28
	vkSelect    = 0x29
	vkPrint     = 0x2A
	vkExecute   = 0x2B
	vkInsert    = 0x2D
	vkDelete    = 0x2E
	vkHelp      = 0x2F
	vkLWin      = 0x5B
	vkRWin      = 0x5C
	vkNumLock   = 0x90
	vkScroll    = 0x91
	vkLShift    = 0xA0
	vkRShift    = 0xA1
	vkLControl  = 0xA2
	vkRControl  = 0xA3
	vkLMenu     = 0xA4
	vkRMenu     = 0xA5
	vkOemPlus   = 0xBB
	vkOem1      = 0xBA
	vkOemMinus  = 0xBD
	vkOemPeriod = 0xBE
	vkOem3      = 0xC0
	vkF1        = 0x70
	vkF12       = 0x7B
)

var vkMaps = map[int]int{
	vkBack:     rimeBackSpace,
	vkTab:      rimeTab,
	vkClear:    rimeClear,
	vkReturn:   rimeReturn,
	vkMenu:     rimeAltL,
	vkPause:    rimePause,
	vkCapital:  rimeCapsLock,
	vkEscape:   rimeEscape,
	vkSpace:    rimeSpace,
	vkPrior:    rimePrior,
	vkNext:     rimeNext,
	vkEnd:      rimeEnd,
	vkHome:     rimeHome,
	vkLeft:     rimeLeft,
	vkUp:       rimeUp,
	vkRight:    rimeRight,
	vkDown:     rimeDown,
	vkSelect:   rimeSelect,
	vkPrint:    rimePrint,
	vkExecute:  rimeExecute,
	vkInsert:   rimeInsert,
	vkDelete:   rimeDelete,
	vkHelp:     rimeHelp,
	vkLWin:     rimeMetaL,
	vkRWin:     rimeMetaR,
	vkNumLock:  rimeNumLock,
	vkScroll:   rimeScrollLock,
	vkLShift:   rimeShiftL,
	vkRShift:   rimeShiftR,
	vkLControl: rimeControlL,
	vkRControl: rimeControlR,
	vkLMenu:    rimeAltL,
	vkRMenu:    rimeAltR,
	vkOem3:     '`',
}

// Set-1 扫描码（lParam 位 16-23，TSF 原样透传）：
// Shift 左右码不同（0x2A/0x36）；Ctrl/Alt 左右同码，用扩展位（位 24）区分。
const (
	scanCodeShiftL = 0x2A
	scanCodeShiftR = 0x36
	scanCodeCtrl   = 0x1D
)

// shiftSide 判别本次 Shift 事件来自左键还是右键。
// 优先用扫描码（物理身份）；缺失时回退到按键状态快照（GetKeyState 高位
// =按住）。不能用 toggle 低位判别：非锁定键该位随每次按下翻转，右 Shift
// 的身份会在两次按键间来回切换（曾表现为右 Shift 需按两次才切换中英）。
func shiftSide(req *imecore.Request) int {
	switch req.ScanCode {
	case scanCodeShiftR:
		return vkRShift
	case scanCodeShiftL:
		return vkLShift
	}
	if req.KeyStates.IsKeyDown(vkRShift) && !req.KeyStates.IsKeyDown(vkLShift) {
		return vkRShift
	}
	return vkLShift
}

// controlSide 左右 Ctrl 同为 0x1D，靠扩展位区分；无扫描码时按住状态兜底。
func controlSide(req *imecore.Request) int {
	if req.ScanCode == scanCodeCtrl && req.IsExtended {
		return vkRControl
	}
	if req.ScanCode == 0 && req.KeyStates.IsKeyDown(vkRControl) &&
		!req.KeyStates.IsKeyDown(vkLControl) {
		return vkRControl
	}
	return vkLControl
}

func translateKeyCode(req *imecore.Request) int {
	keyCode := req.KeyCode
	if keyCode == vkShift {
		keyCode = shiftSide(req)
	} else if keyCode == vkControl {
		keyCode = controlSide(req)
	}

	if mapped, ok := vkMaps[keyCode]; ok {
		// OEM_3 是 vkMaps 里唯一带字符语义的可打印键（其余 OEM 键走
		// CharCode）。Shift+` 前端 ToUnicodeEx 已给 CharCode='~'，常量
		// 映射必须让位，否则 shift 语义被吞（2026-10-29 用户实报
		// Shift+` 得到 `）。
		if keyCode == vkOem3 && isPrintableChar(req) && req.CharCode != '`' {
			return req.CharCode
		}
		return mapped
	}
	if isPrintableChar(req) {
		return req.CharCode
	}
	if keyCode >= vkF1 && keyCode <= vkF12 {
		return rimeF1 + keyCode - vkF1
	}
	return voidSymbol
}

func translateModifiers(req *imecore.Request, isUp bool) int {
	result := 0
	keyCode := req.KeyCode
	if keyCode != vkShift && req.KeyStates.IsKeyDown(vkShift) {
		result |= shiftMask
	}
	if req.KeyStates.IsKeyToggled(vkCapital) {
		result |= lockMask
	}
	if keyCode != vkControl && req.KeyStates.IsKeyDown(vkControl) {
		result |= controlMask
	}
	if keyCode != vkMenu && req.KeyStates.IsKeyDown(vkMenu) {
		result |= altMask
	}
	if isUp {
		result |= releaseMask
	}
	if keyCode == vkCapital && !isUp {
		result ^= lockMask
	}
	return result
}

func isPrintableChar(req *imecore.Request) bool {
	return req.CharCode > 0x1f && req.CharCode != 0x7f
}
