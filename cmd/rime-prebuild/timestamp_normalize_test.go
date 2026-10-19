//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 打包机奇数秒 mtime 经 Inno 2 秒粒度取整后必与产物记录对不上，
// 首切逐个重编。normalizeYamlMtimes 必须把 yaml 截到偶数秒，
// 且不动 build/ 产物与非 yaml 文件。
func TestNormalizeYamlMtimes(t *testing.T) {
	dir := t.TempDir()
	odd := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC) // 奇数秒

	yamlPath := filepath.Join(dir, "a.yaml")
	if err := os.WriteFile(yamlPath, []byte("a: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(yamlPath, odd, odd); err != nil {
		t.Fatal(err)
	}

	buildDir := filepath.Join(dir, "build")
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		t.Fatal(err)
	}
	builtPath := filepath.Join(buildDir, "b.yaml")
	if err := os.WriteFile(builtPath, []byte("b: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(builtPath, odd, odd); err != nil {
		t.Fatal(err)
	}

	txtPath := filepath.Join(dir, "c.txt")
	if err := os.WriteFile(txtPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(txtPath, odd, odd); err != nil {
		t.Fatal(err)
	}

	if err := normalizeYamlMtimes(dir); err != nil {
		t.Fatal(err)
	}

	got, err := os.Stat(yamlPath)
	if err != nil {
		t.Fatal(err)
	}
	want := odd.Truncate(2 * time.Second).Add(500 * time.Millisecond)
	if !got.ModTime().Equal(want) {
		t.Fatalf("yaml mtime = %v, want %v", got.ModTime(), want)
	}
	floor2 := got.ModTime().Truncate(2 * time.Second)
	if !floor2.Add(500 * time.Millisecond).Equal(got.ModTime()) {
		t.Fatalf("yaml mtime 未落在偶数+0.5s: %v", got.ModTime())
	}

	gotB, err := os.Stat(builtPath)
	if err != nil {
		t.Fatal(err)
	}
	if !gotB.ModTime().Equal(odd) {
		t.Fatalf("build/ 产物被误动: %v", gotB.ModTime())
	}

	gotT, err := os.Stat(txtPath)
	if err != nil {
		t.Fatal(err)
	}
	if !gotT.ModTime().Equal(odd) {
		t.Fatalf("非 yaml 被误动: %v", gotT.ModTime())
	}
}
