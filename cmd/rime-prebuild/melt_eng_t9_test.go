//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const meltEngT9Sample = `# Rime schema
__include: melt_eng.schema.yaml:/

schema:
  schema_id: melt_eng_t9

translator:
  dictionary: melt_eng
  prism: melt_eng_t9
`

// 整树 include melt_eng.schema.yaml:/ 触发 librime 循环依赖误判、部署反复
// 重试（首切卡顿主体）。收窄到 /engine 断环，t9 自带其余覆盖段。
func TestPatchMeltEngT9Include(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "melt_eng_t9.schema.yaml")
	if err := os.WriteFile(path, []byte(meltEngT9Sample), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := patchMeltEngT9Include(dir); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(path)
	got := string(body)
	if strings.Contains(got, "melt_eng.schema.yaml:/\n") {
		t.Fatalf("整树 include 未收窄:\n%s", got)
	}
	if !strings.Contains(got, "__include: melt_eng.schema.yaml:/engine") {
		t.Fatalf("未改为 /engine:\n%s", got)
	}
	if !strings.Contains(got, "schema_id: melt_eng_t9") {
		t.Fatalf("其他内容被破坏:\n%s", got)
	}

	// 幂等。
	if err := patchMeltEngT9Include(dir); err != nil {
		t.Fatal(err)
	}
	body2, _ := os.ReadFile(path)
	if string(body2) != got {
		t.Fatalf("patch 不幂等")
	}
}

// 文件不存在时是 no-op（不报错）。
func TestPatchMeltEngT9IncludeMissingFile(t *testing.T) {
	dir := t.TempDir()
	if err := patchMeltEngT9Include(dir); err != nil {
		t.Fatalf("missing file should be no-op, got %v", err)
	}
}
