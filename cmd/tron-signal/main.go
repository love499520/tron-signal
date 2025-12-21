package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"tron-signal/internal/api"
	"tron-signal/internal/app"
	"tron-signal/internal/config"
	"tron-signal/internal/logx"
	"tron-signal/internal/stream"
)

func main() {
	// Ensure dirs
	_ = os.MkdirAll("data", 0755)
	_ = os.MkdirAll("logs", 0755)
	_ = os.MkdirAll("docs", 0755)

	// Logging (daily rotate file)
	logx.Init(logx.TodayLogPath())
	logx.Info("SYSTEM_START")

	// Abnormal restart detection
	app.CheckAbnormalRestart()

	// Load config
	cfg, err := config.Load("data/config.json")
	if err != nil {
		logx.Error("CONFIG_LOAD_ERROR", err)
	}

	// Runtime state (hard reset on every start)
	rt := app.NewRuntime()
	rt.Reset()

	// Streams
	wsHub := stream.NewWSHub()
	sseHub := stream.NewSSEHub()

	// App context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start core loop (poll sources -> process blocks -> run machines -> broadcast)
	core := app.NewCore(ctx, cfg, rt, wsHub, sseHub)
	go core.Run()

	// HTTP Server
	mux := http.NewServeMux()
	api.RegisterRoutes(mux, cfg, rt, core, wsHub, sseHub)

	srv := &http.Server{
		Addr:              ":8080",
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		logx.Info("HTTP_LISTEN :8080")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logx.Error("SERVER_ERROR", err)
		}
	}()

	// Graceful shutdown
	sig := make(chan os.Signal, 2)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	logx.Info("SYSTEM_STOP")
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = srv.Shutdown(shutdownCtx)

	// Normal exit marker
	app.MarkNormalExit()
}
