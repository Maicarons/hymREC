# 项目结构

```
hymREC/
├── cmd/
│   └── hymREC/               # 主入口（flag 解析、信号处理）
├── internal/
│   ├── asr/                  # ASR 引擎接口 + None/FunASR-WS 实现
│   ├── bili/                 # B 站直播 API（房间信息、流地址）
│   ├── capture/              # 音频采集（ffmpeg 管道 -> 16kHz PCM）
│   ├── config/               # 配置加载与校验（YAML + 默认值）
│   ├── danmaku/              # 弹幕 WebSocket 协议与客户端
│   ├── keyword/              # T1-T4 层级关键词检测引擎
│   ├── pipeline/             # 主流水线（组装各组件）
│   ├── recorder/             # 视频分段缓冲 + 录制控制器（状态机）
│   └── ringbuf/              # 音频环形缓冲（90 秒回溯）
├── docs/                     # VitePress 文档
├── .github/workflows/        # CI
├── config.example.yaml       # 配置示例
├── Makefile
├── go.mod
└── LICENSE                   # AGPL-3.0
```

## 包依赖关系

```
cmd/hymREC
    └── pipeline
        ├── asr
        ├── bili
        ├── capture
        ├── danmaku
        ├── keyword
        ├── recorder
        └── ringbuf
config 被 pipeline 与 asr 引用
```

## 关键文件

| 文件 | 职责 |
|------|------|
| `internal/keyword/detector.go` | 层级关键词检测（T1-T4、冷却、停止延迟） |
| `internal/recorder/video_buffer.go` | ffmpeg TS 分段持续录制、按时间回溯 |
| `internal/recorder/controller.go` | 录制状态机、片段 concat 合并 |
| `internal/danmaku/protocol.go` | B 站弹幕二进制协议编解码 |
| `internal/pipeline/pipeline.go` | 流水线编排、弹幕密度统计、VAD |
| `internal/asr/funasr_ws.go` | FunASR WebSocket 流式客户端 |
