package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	daednext "daed-next"
	"daed-next/internal/api"
	"daed-next/internal/daemon"
)

func main() {
	listen := flag.String("listen", ":2023", "HTTP listen address (default :2023, compatible with daed)")
	dataDir := flag.String("data", "/etc/dae/daed-next", "Directory for DaeStudio state & snapshots")
	configPath := flag.String("config", "/etc/dae/config.dae", "Target config.dae file path")
	daeBin := flag.String("dae-bin", "dae", "Path or binary name of dae kernel")
	mock := flag.Bool("mock", false, "Force Mock Kernel Mode for preview/testing without root or eBPF")
	bootstrapEnv := flag.Bool("bootstrap-env", false, "Ensure dae init scripts, official dae binary, and GeoData exist, then exit")
	showVersion := flag.Bool("version", false, "Print DaeStudio version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("daed-next version %s\n", daemon.PanelVersion)
		return
	}

	mgr := daemon.NewManager(*daeBin, *configPath, *mock)
	if *bootstrapEnv {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		if err := mgr.BootstrapEnvironment(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "[提示] 自动补齐运行环境未完全成功: %v\n", err)
			os.Exit(1)
		}
		return
	}

	statePath := filepath.Join(*dataDir, "state.json")
	srv := api.NewServer(statePath, *daeBin, *mock, mgr, daednext.WebFS)

	if err := srv.InitError(); err != nil {
		log.Printf("[ERROR] 状态加载失败，已进入 503 安全保护模式（原文件已保留未覆盖）: %v", err)
	}

	fmt.Printf("============================================================\n")
	fmt.Printf("  DaeStudio (daed-next) %s Ready\n", daemon.PanelVersion)
	fmt.Printf("  Listen URL  : http://localhost%s\n", *listen)
	fmt.Printf("  State File  : %s\n", statePath)
	fmt.Printf("  Config File : %s\n", *configPath)
	fmt.Printf("============================================================\n")

	httpServer := &http.Server{Addr: *listen, Handler: srv.Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	if err := httpServer.ListenAndServe(); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
