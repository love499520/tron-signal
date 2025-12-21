package main

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"tron-signal/backend/app"
	"tron-signal/backend/block"
	"tron-signal/backend/httpapi"
	"tron-signal/backend/judge"
	"tron-signal/backend/machine"
	"tron-signal/backend/source"
	"tron-signal/backend/ws"

	// 下面两个模块我后续会继续给你代码
	"tron-signal/backend/auth"
	"tron-signal/backend/config"
)

func mustMkdir(p string) {
	_ = os.MkdirAll(p, 0755)
}

func main() {
	// ===== dirs =====
	root := "."
	dataDir := filepath.Join(root, "data")
	logDir := filepath.Join(root, "logs")
	webDir := filepath.Join(root, "web")
	docsDir := filepath.Join(root, "api", "docs")

	mustMkdir(dataDir)
	mustMkdir(logDir)

	// ===== config load =====
	// config.Store 会实现 httpapi.ConfigReader 接口（后续文件会给出）
	cfgPath := filepath.Join(dataDir, "config.json")
	cfg := config.MustLoad(cfgPath)

	// ===== core deps =====
	judgeEngine := judge.NewJudge(cfg.GetJudgeRule())
	mgr := machine.NewManager(cfg.GetMachines())

	ring := block.NewRingBuffer(50)

	hub := ws.NewHub()

	// ✅ 这里是你要求我“直接修正的点”：参数顺序必须是 (judge, mgr, ring, hub)
	core := app.NewCore(judgeEngine, mgr, ring, hub)

	// Dispatcher：多数据源（后续我会给 backend/source/* 完整实现）
	dispatcher := source.NewDispatcher(cfg.GetSources(), cfg.GetPollConfig(), core)

	// AuthStore：管理登录 session（后续我会给 backend/auth/* 实现）
	authStore := auth.NewMemoryStore()

	// ===== http router =====
	router := httpapi.NewRouter(httpapi.RouterDeps{
		Core:       core,
		Ring:       ring,
		Mgr:        mgr,
		Dispatcher: dispatcher,
		Hub:        hub,

		AuthStore: authStore,
		Cfg:       cfg,

		WebDir:  webDir,
		DocsDir: docsDir,
		LogDir:  logDir,
	})

	// ===== run dispatcher =====
	go dispatcher.Run()

	// ===== http =====
	addr := ":8080"
	srv := &http.Server{
		Addr:              addr,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("HTTP_LISTEN %s\n", addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Printf("SERVER_ERROR: %v\n", err)
	}
}
