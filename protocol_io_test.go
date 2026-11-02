package main

// 协议 JSON 契约测试（2026-11-02 三次线上事故后补）。
//
// 事故根因都是「Go 侧 protojson 的线上形状」与「C++ 侧解析假设」脱节：
//   - EmitUnpopulated 让隐式零值字段出现在每条响应里（commandId:0、
//     type:BUTTON_TYPE_UNSPECIFIED、text:""、submenu:[]、showCandidates:false…）
//   - DLL 把这些零值当指令：洗掉按钮 commandId、叶子菜单变空子菜单、
//     每条辅助回执都清组字/清候选。
//
// 这两个测试锁定契约的两端：
//   1. 请求方向：DLL 发出的每个方法名/字段名都能被 protojson 严格解码
//      （DiscardUnknown=false，多一个字段/拼错名字 = 整条请求失败）。
//   2. 响应方向：零值字段的形状必须保持"可识别"（这是 DLL 侧
//      JsonCompat.h 零值守卫存在的前提）——改 proto 前先改这里和 DLL。
import (
	"encoding/json"
	"strings"
	"testing"

	moqipb "github.com/gaboolic/moqi-ime/proto"
)

// TestClientRequestMethodsDecode 锁定 methodNameToJson 的表值名与 DLL
// 实际发送的字段集合。任何一项改动都必须同步改 moqi-im-windows 侧，
// 否则请求会被 protojson 严格解码直接拒绝（表现为该方法整个失效）。
func TestClientRequestMethodsDecode(t *testing.T) {
	cases := []struct {
		method string
		body   string
		want   moqipb.Method
	}{
		{"init", `{"method":"METHOD_INIT","seqNum":0,"guid":"{3f6b5a12-8d44-4e71-9a2e-6b4f9c1d2a30}","isWindows8Above":true,"isMetroApp":false,"isUiLess":true,"isConsole":false}`, moqipb.Method_METHOD_INIT},
		{"onActivate", `{"method":"METHOD_ON_ACTIVATE","seqNum":1,"isKeyboardOpen":true}`, moqipb.Method_METHOD_ON_ACTIVATE},
		{"onDeactivate", `{"method":"METHOD_ON_DEACTIVATE","seqNum":2}`, moqipb.Method_METHOD_ON_DEACTIVATE},
		{"filterKeyDown", `{"method":"METHOD_FILTER_KEY_DOWN","seqNum":3,"keyEvent":{"charCode":105,"keyCode":73,"repeatCount":1,"scanCode":23,"isExtended":false,"keyStates":[0,1,2]}}`, moqipb.Method_METHOD_FILTER_KEY_DOWN},
		{"onKeyDown", `{"method":"METHOD_ON_KEY_DOWN","seqNum":4,"keyEvent":{"charCode":105,"keyCode":73,"repeatCount":1,"scanCode":23,"isExtended":false,"keyStates":[]}}`, moqipb.Method_METHOD_ON_KEY_DOWN},
		{"filterKeyUp", `{"method":"METHOD_FILTER_KEY_UP","seqNum":5,"keyEvent":{"charCode":105,"keyCode":73,"repeatCount":1,"scanCode":23,"isExtended":false,"keyStates":[]}}`, moqipb.Method_METHOD_FILTER_KEY_UP},
		{"onKeyUp", `{"method":"METHOD_ON_KEY_UP","seqNum":6,"keyEvent":{"charCode":105,"keyCode":73,"repeatCount":1,"scanCode":23,"isExtended":false,"keyStates":[]}}`, moqipb.Method_METHOD_ON_KEY_UP},
		{"onPreservedKey", `{"method":"METHOD_ON_PRESERVED_KEY","seqNum":7,"preservedKeyGuid":"{00000000-0000-0000-0000-000000000000}"}`, moqipb.Method_METHOD_ON_PRESERVED_KEY},
		{"onCommand", `{"method":"METHOD_ON_COMMAND","seqNum":8,"commandId":1,"commandType":1}`, moqipb.Method_METHOD_ON_COMMAND},
		{"onMenu", `{"method":"METHOD_ON_MENU","seqNum":9,"buttonId":"windows-mode-icon"}`, moqipb.Method_METHOD_ON_MENU},
		{"onCompartmentChanged", `{"method":"METHOD_ON_COMPARTMENT_CHANGED","seqNum":10,"compartmentGuid":"{00000000-0000-0000-0000-000000000000}"}`, moqipb.Method_METHOD_ON_COMPARTMENT_CHANGED},
		{"onKeyboardStatusChanged", `{"method":"METHOD_ON_KEYBOARD_STATUS_CHANGED","seqNum":11,"opened":true}`, moqipb.Method_METHOD_ON_KEYBOARD_STATUS_CHANGED},
		{"onCompositionTerminated", `{"method":"METHOD_ON_COMPOSITION_TERMINATED","seqNum":12,"forced":false}`, moqipb.Method_METHOD_ON_COMPOSITION_TERMINATED},
		{"highlightCandidate", `{"method":"METHOD_HIGHLIGHT_CANDIDATE","seqNum":13,"candidateIndex":0}`, moqipb.Method_METHOD_HIGHLIGHT_CANDIDATE},
		{"selectCandidate", `{"method":"METHOD_SELECT_CANDIDATE","seqNum":14,"candidateIndex":0}`, moqipb.Method_METHOD_SELECT_CANDIDATE},
		{"changePage", `{"method":"METHOD_CHANGE_PAGE","seqNum":15,"pageBackward":false}`, moqipb.Method_METHOD_CHANGE_PAGE},
	}
	for _, tc := range cases {
		msg, err := decodeClientRequest([]byte(tc.body))
		if err != nil {
			t.Fatalf("%s: decode failed (DLL request would be rejected): %v", tc.method, err)
		}
		if msg.GetMethod() != tc.want {
			t.Fatalf("%s: method decoded as %v, want %v", tc.method, msg.GetMethod(), tc.want)
		}
		if msg.GetMethod() == moqipb.Method_METHOD_UNSPECIFIED {
			t.Fatalf("%s: decoded to METHOD_UNSPECIFIED", tc.method)
		}
	}
}

// TestClientRequestRejectsUnknownField 记录严格解码行为：DLL 新增字段必须
// 先改 proto。若有人把 DiscardUnknown 打开，这个测试会失败并提醒同步评估。
func TestClientRequestRejectsUnknownField(t *testing.T) {
	_, err := decodeClientRequest([]byte(`{"method":"METHOD_ON_MENU","bogusField":1}`))
	if err == nil {
		t.Fatalf("unknown field was accepted: strict decode contract changed")
	}
	if !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("expected unknown-field error, got: %v", err)
	}
}

// TestServerResponseZeroValueShape 锁定 EmitUnpopulated 的线上形状 ——
// C++ 侧 JsonCompat.h 的零值守卫依赖这些形状可识别。
func TestServerResponseZeroValueShape(t *testing.T) {
	resp := &moqipb.ServerResponse{
		SeqNum:  7,
		Success: true,
		MenuItems: []*moqipb.MenuItem{
			{Id: 2000, Text: "中文 → 英文", Enabled: true},
			{Id: 0, Text: "", Separator: true},
			{Id: 0, Text: "维护", Enabled: true, Submenu: []*moqipb.MenuItem{
				{Id: 30, Text: "开启调试", Enabled: true},
			}},
		},
		ChangeButton: []*moqipb.ButtonInfo{
			{Id: "windows-mode-icon", Icon: `C:\x\chi.ico`, Enable: true},
		},
	}
	raw, err := encodeServerResponse(resp)
	if err != nil {
		t.Fatalf("encode failed: %v", err)
	}
	var wire map[string]interface{}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("invalid json: %v", err)
	}

	// 隐式字段必须在场（缺了 DLL 的清组字/关候选分支会失灵）。
	for _, key := range []string{"compositionString", "commitString", "candidateList",
		"showCandidates", "openKeyboard", "hideMessage", "setSelKeys", "error"} {
		if _, ok := wire[key]; !ok {
			t.Fatalf("implicit field %q missing from wire (EmitUnpopulated contract broken)", key)
		}
	}
	// optional 字段未设置时不得出现（出现即被 DLL 当成真实信息）。
	for _, key := range []string{"candidateCursor", "customizeUi", "style"} {
		if _, ok := wire[key]; ok {
			t.Fatalf("unset optional field %q leaked onto the wire", key)
		}
	}

	items := wire["menuItems"].([]interface{})
	leaf := items[0].(map[string]interface{})
	if sub, ok := leaf["submenu"].([]interface{}); !ok || len(sub) != 0 {
		t.Fatalf("leaf submenu shape = %v, want empty array (JsonCompat relies on it)", leaf["submenu"])
	}
	if _, ok := leaf["checked"]; !ok {
		t.Fatalf("menu leaf checked missing (zero value must be explicit)")
	}
	sep := items[1].(map[string]interface{})
	if sep["separator"] != true {
		t.Fatalf("separator flag missing: %v", sep)
	}
	parent := items[2].(map[string]interface{})
	if sub := parent["submenu"].([]interface{}); len(sub) != 1 {
		t.Fatalf("real submenu lost: %v", parent["submenu"])
	}

	btn := wire["changeButton"].([]interface{})[0].(map[string]interface{})
	if cmd, ok := btn["commandId"].(float64); !ok || cmd != 0 {
		t.Fatalf("changeButton commandId shape = %v, want explicit 0", btn["commandId"])
	}
	if typ := btn["type"]; typ != "BUTTON_TYPE_UNSPECIFIED" {
		t.Fatalf("changeButton type = %v, want symbolic enum name", typ)
	}
	if btn["enable"] != true {
		t.Fatalf("changeButton enable = %v, want true (backend always enables)", btn["enable"])
	}
	if _, ok := btn["text"]; !ok {
		t.Fatalf("changeButton text missing (zero value must be explicit)")
	}
}
