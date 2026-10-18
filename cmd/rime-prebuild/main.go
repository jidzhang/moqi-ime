//go:build windows

// rime-prebuild 预编译 Rime 词库（消除用户首键等待）。
//
// 全新用户目录第一次按键时，librime 要现场编译全部 yaml 词库（约 10 分钟，
// 用户表现为"装完第一次按键卡几分钟"）。librime 的 PrebuiltDataDir 机制：
// <data>/build/ 里已有编译好的 .table.bin/.prism.bin 时，部署只做校验拷贝。
//
// 本工具在打包阶段把词库编译一遍，产物放回 <data>/build/ 随安装包分发：
//
//	go run ./cmd/rime-prebuild -data <打包后的 input_methods/rime/data>
//
// 目录布局要求：rime.dll 在 data 的上级目录（与 server 运行时一致）。
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/gaboolic/moqi-ime/input_methods/rime"
)

func main() {
	dataDir := flag.String("data", "", "packaged rime shared data dir (input_methods/rime/data)")
	flag.Parse()
	if *dataDir == "" {
		fmt.Fprintln(os.Stderr, "missing required flag: -data")
		os.Exit(2)
	}

	tmp, err := os.MkdirTemp("", "moqi-rime-prebuild-*")
	if err != nil {
		log.Fatalf("create temp user dir: %v", err)
	}
	defer os.RemoveAll(tmp)

	start := time.Now()
	if !rime.RimeInit(*dataDir, tmp, "MoqiIM", "prebuild", true) {
		log.Fatal("RimeInit failed")
	}
	rime.JoinMaintenanceThread()
	rime.Finalize()
	fmt.Printf("deploy done in %s\n", time.Since(start))

	src := filepath.Join(tmp, "build")
	dst := filepath.Join(*dataDir, "build")
	if err := copyTree(src, dst); err != nil {
		log.Fatalf("copy build artifacts: %v", err)
	}
	fmt.Printf("prebuilt artifacts installed to %s\n", dst)
}

func copyTree(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		body, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), body, 0o644); err != nil {
			return err
		}
	}
	return nil
}
