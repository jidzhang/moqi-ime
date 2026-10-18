//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const shiftRSample = `ascii_composer:
  good_old_caps_lock: true  # true | false
  switch_key:
    Caps_Lock: clear      # commit_code | commit_text | clear
    Shift_L: commit_code  # commit_code | commit_text | inline_ascii | clear | noop
    Shift_R: noop         # commit_code | commit_text | inline_ascii | clear | noop
    Control_L: noop       # commit_code | commit_text | inline_ascii | clear | noop
    Control_R: noop       # commit_code | commit_text | inline_ascii | clear | noop
`

func writeTempDefaultYaml(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "default.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// 打包时必须把上游 Shift_R: noop 对齐成与 Shift_L 一致的 commit_code，
// 否则右 Shift 无法切换中英（引擎按 release 键身份查 switch_key 表）。
func TestPatchDefaultShiftR(t *testing.T) {
	dir := writeTempDefaultYaml(t, shiftRSample)

	if err := patchDefaultShiftR(dir); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "default.yaml"))
	got := string(body)
	if strings.Contains(got, "Shift_R: noop") {
		t.Fatalf("Shift_R 仍为 noop:\n%s", got)
	}
	if !strings.Contains(got, "Shift_R: commit_code") {
		t.Fatalf("Shift_R 未改为 commit_code:\n%s", got)
	}
	if !strings.Contains(got, "Shift_L: commit_code") {
		t.Fatalf("Shift_L 被误改:\n%s", got)
	}
	if !strings.Contains(got, "Caps_Lock: clear") {
		t.Fatalf("Caps_Lock 被误改:\n%s", got)
	}

	// 幂等：再跑一遍不再变化。
	if err := patchDefaultShiftR(dir); err != nil {
		t.Fatal(err)
	}
	body2, _ := os.ReadFile(filepath.Join(dir, "default.yaml"))
	if string(body2) != got {
		t.Fatalf("patch 不幂等")
	}
}

// 已经是目标值的文件保持原样（用户手工改过的部署目录重打包场景）。
func TestPatchDefaultShiftRIdempotentOnPatched(t *testing.T) {
	dir := writeTempDefaultYaml(t, strings.Replace(shiftRSample, "Shift_R: noop", "Shift_R: commit_code", 1))

	if err := patchDefaultShiftR(dir); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "default.yaml"))
	if got := string(body); got != strings.Replace(shiftRSample, "Shift_R: noop", "Shift_R: commit_code", 1) {
		t.Fatalf("已打过补丁的文件被改动:\n%s", got)
	}
}
