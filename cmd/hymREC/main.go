// hymREC 是面向虚拟主播直播切片场景的关键词触发录制系统。
//
// 监控目标直播间，将直播流持续分段缓冲；当 ASR 识别文本命中关键词
// （或弹幕密度达标）时，自动产出包含关键词前 90 秒上下文的视频切片。
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/henyemiren/hymREC/internal/config"
	"github.com/henyemiren/hymREC/internal/pipeline"
)

var version = "dev"

func main() {
	fs := flag.NewFlagSet("hymREC", flag.ExitOnError)
	var (
		configPath = fs.String("c", "config.yaml", "配置文件路径（不存在时使用默认配置）")
		roomID     = fs.Int("room", 0, "目标直播间号（覆盖配置文件，支持短号）")
		platform   = fs.String("platform", "", "拉流平台：web(flv) / h5(m3u8)，覆盖配置文件")
		asrType    = fs.String("asr-type", "", "ASR 类型：none / mock / funasr-ws，覆盖配置文件")
		outputDir  = fs.String("out", "", "切片输出目录，覆盖配置文件")
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
	if err := cfg.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, "配置校验失败:", err)
		os.Exit(1)
	}

	logger := newLogger(cfg.LogLevel)
	logger.Info("关键词触发录制系统启动",
		"version", version,
		"room_id", cfg.RoomID,
		"platform", cfg.Platform,
		"asr", cfg.ASR.Type,
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	p, err := pipeline.New(cfg, logger)
	if err != nil {
		logger.Error("初始化流水线失败", "err", err)
		os.Exit(1)
	}

	if err := p.Run(ctx); err != nil {
		if ctx.Err() != nil {
			logger.Info("系统已停止")
			return
		}
		logger.Error("流水线异常退出", "err", err)
		os.Exit(1)
	}
	logger.Info("系统已停止")
}

// newLogger 按级别创建结构化日志器。
func newLogger(level string) *slog.Logger {
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
	h := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})
	return slog.New(h)
}
