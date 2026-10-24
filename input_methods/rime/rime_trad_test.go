package rime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/gaboolic/moqi-ime/imecore"
)

// TestSwitchCommandTogglesTraditionalization 既有顶层开关项「简体→繁体」
// （buildMenu 动态开关段生成）完整链路：菜单命令翻转引擎开关 → 菜单勾选态
// 跟随 → 共享模式落盘 → 新实例继承。2026-10-24：该功能此前无专项测试。
func TestSwitchCommandTogglesTraditionalization(t *testing.T) {
	appData := t.TempDir()
	t.Setenv("APPDATA", appData)
	resetSharedAppearanceConfigForTest()

	ime := newTestIME()
	backend := ime.backend.(*testBackend)
	backend.EnsureSession()
	ime.inputStateShared = true

	// 菜单里的 traditionalization 开关项（动态段）。
	switchIndex := -1
	for i, sw := range ime.menuSwitches() {
		if sw.Name == "traditionalization" {
			switchIndex = i
			break
		}
	}
	if switchIndex < 0 {
		t.Fatal("expected traditionalization in menu switches")
	}
	commandID := switchCommandID(switchIndex)

	menuEntry := func() map[string]interface{} {
		for _, item := range ime.buildMenu() {
			if id, _ := item["id"].(int); id == commandID {
				return item
			}
		}
		t.Fatal("traditionalization switch item missing from menu")
		return nil
	}
	if checked, _ := menuEntry()["checked"].(bool); checked {
		t.Fatal("expected switch unchecked initially")
	}

	resp := ime.onCommand(&imecore.Request{
		SeqNum: 40,
		ID:     imecore.FlexibleID{Int: commandID, IsInt: true},
	}, imecore.NewResponse(40, true))
	if resp.ReturnValue != 1 {
		t.Fatalf("expected switch command handled, got %d", resp.ReturnValue)
	}
	if !ime.backend.GetOption("traditionalization") {
		t.Fatal("expected engine option toggled on")
	}
	if checked, _ := menuEntry()["checked"].(bool); !checked {
		t.Fatal("expected menu checked after toggle")
	}

	// 落盘：共享选项走通用 shared_options 映射。
	configPath := filepath.Join(appData, APP, appearanceConfigFileName)
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("expected appearance config persisted: %v", err)
	}
	var persisted map[string]any
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("expected valid config json: %v", err)
	}
	shared, ok := persisted["shared_options"].(map[string]any)
	if !ok || shared["traditionalization"] != true {
		t.Fatalf("expected shared_options.traditionalization true, got %#v", persisted["shared_options"])
	}

	// 新实例（读同一配置）在会话建立后继承繁体状态。
	second := newTestIME()
	second.HandleRequest(&imecore.Request{
		SeqNum: 41,
		Method: "onMenu",
		ID:     imecore.FlexibleID{String: "settings"},
	})
	second.createSession(nil)
	if !second.backend.GetOption("traditionalization") {
		t.Fatal("expected new instance to inherit traditionalization from shared config")
	}
}
