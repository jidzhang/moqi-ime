//go:build windows

// 排障探针：复现装机首切场景——sharedData 自带 build/，userData 为全新空目录，
// fullcheck=true 跑一次部署并 Join。部署产物留在 userData 供逐 byte 对比，
// 回答"prebuilt prism 为何未被复用、每次 deploy 的 schema checksum 为何漂移"。
// 用法：deploy-probe.exe <sharedDataDir> <emptyUserDir>
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/gaboolic/moqi-ime/input_methods/rime"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Println("usage: deploy-probe <sharedDataDir> <emptyUserDir>")
		os.Exit(2)
	}
	shared, user := os.Args[1], os.Args[2]
	if err := os.MkdirAll(user, 0o700); err != nil {
		panic(err)
	}
	start := time.Now()
	if !rime.RimeInit(shared, user, "MoqiIM", "deploy-probe", true) {
		panic("RimeInit failed")
	}
	rime.JoinMaintenanceThread()
	fmt.Printf("deploy done in %s\n", time.Since(start))
	rime.Finalize()
}
