// hymREC 是面向虚拟主播直播切片场景的关键词触发录制系统。
//
// 监控目标直播间，将直播流持续分段缓冲；当 ASR 识别文本命中关键词
// （或弹幕密度达标）时，自动产出包含关键词前 90 秒上下文的视频切片。
//
// 默认同时启动本地监控面板（http://127.0.0.1:8080），可查看状态、
// 事件、切片与日志，并可启停服务、手动触发切片。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Maicarons/hymREC/internal/app"
	"github.com/Maicarons/hymREC/internal/config"
	"github.com/Maicarons/hymREC/internal/web"
)

var version = "dev"

func main() {
	fs := flag.NewFlagSet("hymREC", flag.ExitOnError)
	var (
		configPath = fs.String("c", "config.yaml", "配置文件路径（不存在时使用默认配置）")
		roomID     = fs.Int("room", 0, "目标直播间号（覆盖配置文件，支持短号）")
		platform   = fs.String("platform", "", "拉流平台：web(flv) / h5(m3u8)，覆盖配置文件")
		asrType    = fs.String("asr-type", "", "ASR 类型：none / funasr-ws，覆盖配置文件")
		outputDir  = fs.String("out", "", "切片输出目录，覆盖配置文件")
		webAddr    = fs.String("web-addr", "", "监控面板监听地址（覆盖配置文件，如 127.0.0.1:8080）")
		noWeb      = fs.Bool("no-web", false, "关闭监控面板")
		showVer    = fs.Bool("version", false, "显示版本信息")
	)
	fs.Parse(os.Args[1:])

	if *showVer {
		fmt.Printf("hymREC %s\n", version)
		return
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "加载配置失败:", err)
		os.Exit(1)
	}
	// 命令行覆盖
	if *roomID > 0 {
		cfg.RoomID = *roomID
	}
	if *platform != "" {
		cfg.Platform = *platform
	}
	if *asrType != "" {
		cfg.ASR.Type = *asrType
	}
	if *outputDir != "" {
		cfg.Recorder.OutputDir = *outputDir
	}
	if *webAddr != "" {
		cfg.Web.Listen = *webAddr
	}
	if *noWeb {
		cfg.Web.Enabled = false
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, "配置校验失败:", err)
		os.Exit(1)
	}

	// 日志同时输出到终端与监控面板
	logStore := web.NewLogStore(800)
	logger := newLogger(cfg.LogLevel, logStore)
	logger.Info("关键词触发录制系统启动",
		"version", version,
		"room_id", cfg.RoomID,
		"platform", cfg.Platform,
		"asr", cfg.ASR.Type,
	)

	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	service, err := app.New(cfg, logger)
	if err != nil {
		logger.Error("初始化服务失败", "err", err)
		os.Exit(1)
	}
	service.SetVersion(version)

	if cfg.Web.Enabled {
		server, err := web.New(cfg.Web.Listen, service, logStore, logger)
		if err != nil {
			logger.Error("初始化监控面板失败，继续以无面板模式运行", "err", err)
		} else {
			go func() {
				if err := server.Run(ctx); err != nil && ctx.Err() == nil {
					logger.Error("监控面板退出，录制服务继续运行", "err", err)
				}
			}()
		}
	}

	if err := service.Start(); err != nil {
		logger.Error("启动录制服务失败", "err", err)
		os.Exit(1)
	}

	<-ctx.Done()
	logger.Info("正在停止，等待录制收尾...")
	if err := service.Stop(); err != nil && !errors.Is(err, app.ErrNotRunning) {
		logger.Warn("停止服务时出现问题", "err", err)
	}
	logger.Info("系统已停止")
}

// newLogger 按级别创建结构化日志器，并把日志同步到监控面板。
func newLogger(level string, store *web.LogStore) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	var handler slog.Handler = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})
	if store != nil {
		handler = store.Handler(handler)
	}
	return slog.New(handler)
}
