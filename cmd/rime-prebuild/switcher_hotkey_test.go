//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const switcherHotkeySample = `switcher:
  caption: 「方案选单」
  hotkeys:
    - Control+grave
    - Control+Shift+grave
    # - Alt+grave
  save_options:  # 开关记忆（方案中的 switches）
    - ascii_mode
`

// 方案选单快捷键必须整体移除：Control+grave 撞 VS/VSCode 终端、
// F4 撞 Total Commander，没有无冲突全局键。切换走托盘菜单。
func TestPatchSwitcherHotkeyCleared(t *testing.T) {
	dir := writeTempDefaultYaml(t, switcherHotkeySample)

	if err := patchSwitcherHotkey(dir); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "default.yaml"))
	got := string(body)
	for _, banned := range []string{"grave", "F4"} {
		if strings.Contains(got, banned) {
			t.Fatalf("仍含快捷键 %q:\n%s", banned, got)
		}
	}
	if !strings.Contains(got, "hotkeys: []") {
		t.Fatalf("hotkeys 未清空:\n%s", got)
	}
	// 后续块保留。
	if !strings.Contains(got, "save_options:") || !strings.Contains(got, "- ascii_mode") {
		t.Fatalf("hotkeys 之后的块丢失:\n%s", got)
	}
	if !strings.Contains(got, "caption: 「方案选单」") {
		t.Fatalf("switcher 其他键丢失:\n%s", got)
	}

	// 幂等：再跑一遍不再变化。
	if err := patchSwitcherHotkey(dir); err != nil {
		t.Fatal(err)
	}
	body2, _ := os.ReadFile(filepath.Join(dir, "default.yaml"))
	if string(body2) != got {
		t.Fatalf("patch 不幂等:\n---- 第一遍 ----\n%s\n---- 第二遍 ----\n%s", got, string(body2))
	}
}

// hotkeys 已是空列表（已打过补丁或用户自定义）时是 no-op。
func TestPatchSwitcherHotkeyIdempotentOnEmpty(t *testing.T) {
	empty := `switcher:
  caption: 「方案选单」
  hotkeys: []  # 不提供全局快捷键
  save_options:
    - ascii_mode
`
	dir := writeTempDefaultYaml(t, empty)
	if err := patchSwitcherHotkey(dir); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "default.yaml"))
	if string(body) != empty {
		t.Fatalf("空列表被改动:\n%s", string(body))
	}
}

// 无 switcher 块的 yaml 是 no-op。
func TestPatchSwitcherHotkeyNoSwitcher(t *testing.T) {
	other := `menu:
  page_size: 9
`
	dir := writeTempDefaultYaml(t, other)
	if err := patchSwitcherHotkey(dir); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "default.yaml"))
	if string(body) != other {
		t.Fatalf("无 switcher 块被改动:\n%s", string(body))
	}
}
