package rime

import (
	"testing"

	"github.com/gaboolic/moqi-ime/imecore"
)

func TestTranslateKeyCodeFallsBackToTopRowDigitKeyCode(t *testing.T) {
	// 现契约：可打印键由前端带 CharCode，翻译器直接透传；
	// 数字键不再从 KeyCode 推字符（数字直通走 DLL 侧）。
	req := &imecore.Request{KeyCode: 0x32, CharCode: '2'}

	if got := translateKeyCode(req); got != int('2') {
		t.Fatalf("expected top-row digit '2' passthrough, got %d", got)
	}
	// 只给 KeyCode 不给 CharCode 时不再猜字符，返回 void 符号。
	if got := translateKeyCode(&imecore.Request{KeyCode: 0x32}); got != voidSymbol {
		t.Fatalf("expected void for keyCode-only digit, got %d", got)
	}
}

func TestTranslateKeyCodeFallsBackToNumpadDigitKeyCode(t *testing.T) {
	req := &imecore.Request{KeyCode: 0x62, CharCode: '2'}

	if got := translateKeyCode(req); got != int('2') {
		t.Fatalf("expected numpad digit '2' passthrough, got %d", got)
	}
	if got := translateKeyCode(&imecore.Request{KeyCode: 0x62}); got != voidSymbol {
		t.Fatalf("expected void for keyCode-only numpad digit, got %d", got)
	}
}
