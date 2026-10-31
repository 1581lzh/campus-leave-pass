// Command leavepass runs the campus leave-pass web service.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"leavepass/internal/app"
	"leavepass/internal/config"
	"leavepass/internal/jobs"
	"leavepass/internal/web"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel()}))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		logger.Error("配置无效", "err", err)
		os.Exit(1)
	}
	store, err := app.Open(cfg)
	if err != nil {
		logger.Error("启动失败", "err", err)
		os.Exit(1)
	}
	defer store.Close()

	srv, err := web.New(store)
	if err != nil {
		logger.Error("模板加载失败", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		jobs.Run(ctx, store)
	}()

	httpSrv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      90 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("服务已启动", "addr", cfg.ListenAddr, "base_url", cfg.BaseURL)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case <-ctx.Done():
		logger.Info("收到退出信号，正在停止服务")
	case err := <-serveErr:
		if err != nil {
			logger.Error("HTTP 服务异常退出", "err", err)
			stop()
			<-workerDone
			os.Exit(1)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		logger.Warn("优雅退出超时，强制关闭", "err", err)
		_ = httpSrv.Close()
	}
	stop()
	select {
	case <-workerDone:
	case <-time.After(5 * time.Second):
		logger.Warn("后台任务未在预期时间内退出")
	}
	logger.Info("服务已停止")
}

func logLevel() slog.Level {
	switch os.Getenv("LOG_LEVEL") {
	case "debug", "DEBUG":
		return slog.LevelDebug
	case "warn", "WARN":
		return slog.LevelWarn
	case "error", "ERROR":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
