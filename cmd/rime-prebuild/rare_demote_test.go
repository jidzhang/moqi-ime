//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const rareDemoteSchemaSample = `engine:
  filters:
    - lua_filter@*is_in_user_dict
    - uniquifier                                    # 去重
`

func TestInstallRareDemoteFilter(t *testing.T) {
	// 补丁源按仓库根相对路径读取（与其他打包补丁一致），测试进程切到仓库根
	oldWD, _ := os.Getwd()
	if err := os.Chdir(filepath.Join(oldWD, "..", "..")); err != nil {
		t.Skipf("cannot chdir to repo root: %v", err)
	}
	defer os.Chdir(oldWD)

	dir := t.TempDir()
	luaDir := filepath.Join(dir, "lua")
	if err := os.MkdirAll(luaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	schema := filepath.Join(dir, "rime_frost.schema.yaml")
	if err := os.WriteFile(schema, []byte(rareDemoteSchemaSample), 0o644); err != nil {
		t.Fatal(err)
	}
	// 无关 schema 不动
	other := filepath.Join(dir, "bopomofo.schema.yaml")
	if err := os.WriteFile(other, []byte(rareDemoteSchemaSample), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := installRareDemoteFilter(dir); err != nil {
		t.Fatalf("first install: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "lua", "moqi_rare_demote.lua"))
	if err != nil || len(body) == 0 {
		t.Fatalf("lua not installed: %v", err)
	}
	got, _ := os.ReadFile(schema)
	s := string(got)
	if !strings.Contains(s, "- lua_filter@*moqi_rare_demote") {
		t.Fatalf("filter not hooked:\n%s", s)
	}
	if strings.Index(s, "moqi_rare_demote") < strings.Index(s, "- uniquifier") {
		t.Fatalf("filter must come after uniquifier:\n%s", s)
	}
	otherBody, _ := os.ReadFile(other)
	if strings.Contains(string(otherBody), "moqi_rare_demote") {
		t.Fatalf("non rime_frost schema should not be patched")
	}

	// 幂等：第二次运行不重复挂载
	if err := installRareDemoteFilter(dir); err != nil {
		t.Fatalf("second install: %v", err)
	}
	got2, _ := os.ReadFile(schema)
	if n := strings.Count(string(got2), "lua_filter@*moqi_rare_demote"); n != 1 {
		t.Fatalf("expected 1 hook after rerun, got %d", n)
	}
}
