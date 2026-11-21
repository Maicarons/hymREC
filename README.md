# hymREC

面向虚拟主播直播切片场景的 **B 站直播关键词触发录制系统**（Go）。

监控目标直播间，将直播流持续分段缓冲；当语音识别文本命中关键词（或弹幕密度达标）时，自动产出包含关键词前 90 秒上下文的视频切片，供后续 FFmpeg 剪辑（加片头、断章取义）使用。

关键词策略参考**一些切片创作者**的"量大管饱也有爆点"风格：触发条件宽松、覆盖"其他主播提及相关话题"的外溢内容。

> ⚠️ 仅供学习交流使用。请在征得对方许可的情况下录制，请勿用于商业用途或大规模录制。详见 [法律声明](https://Maicarons.github.io/hymREC/guide/legal.html)。

## ✨ 特性

- **层级关键词引擎**：T1 精确 / T2 模糊（相似度阈值）/ T3 梗词联合 / T4 否定屏蔽
- **视频回溯切片**：ffmpeg 按 10 秒分段持续缓冲，触发时回溯前 90 秒并 concat 合并输出 TS/MP4
- **弹幕辅助触发**：WebSocket 实时监听弹幕密度（30 秒窗口 ≥ 5 条触发，延迟确认防误触）
- **流式 ASR**：内置 FunASR WebSocket 客户端与热词注入，识别命中关键词即触发
- **断流自愈**：音频采集自动重建，视频缓冲看门狗检测卡死后刷新流地址重启；主播下播后自动回到等待开播
- **内置监控面板**：单二进制自带网页控制台，实时状态、事件流、切片下载、日志与在线设置

## 🚀 快速开始

环境要求：Go 1.25+、[ffmpeg](https://ffmpeg.org/)（加入 PATH）。

```bash
git clone https://github.com/Maicarons/hymREC.git
cd hymREC

make build
cp config.example.yaml config.yaml   # 修改 room_id
./bin/hymREC -c config.yaml
```

不想编译？直接从 [Releases](https://github.com/Maicarons/hymREC/releases) 下载预编译二进制 `hymREC_<版本>_<平台>_<架构>`（Windows 带 `.exe`），跳过构建步骤。运行前需自行安装 [ffmpeg](https://ffmpeg.org/) 并加入 PATH。

输出：

```
recordings/    ← 触发录制产出的视频切片（.ts / .mp4）
video_buffer/  ← 持续分段缓冲（自动清理）
```

启动后打开 <http://127.0.0.1:8080> 即可看到监控面板；不需要面板时加 `-no-web`，换端口用 `-web-addr 127.0.0.1:9000`。

## 🖥️ 监控面板

面板资源编进二进制（`go:embed`），无需 Node 或额外部署。

- **状态**：直播间与开播状态、运行/会话时长、视频缓冲分段与卡死检测、录制中/触发来源、ASR 最后识别文本、弹幕连接与窗口命中数、切片数量与总大小；
- **事件流**：会话开始/结束、触发、切片产出、流地址刷新、缓冲重启、错误，SSE 实时推送；
- **切片**：按触发时间倒序，显示来源/大小，点击下载；
- **日志**：与终端一致的结构化日志，可级别过滤、跟随滚动；
- **控制**：启动/停止、手动切片、结束录制、应用并重启（切换直播间）、在线调整关键词 T1-T4 与触发节奏、弹幕密度参数。

所有能力都有 HTTP 接口，可脚本化；详见[监控面板文档](docs/guide/dashboard.md)。

## 🏗️ 架构

```
B站流 → 视频环形缓冲(ffmpeg TS分段) ──触发时回溯前90秒──▶ 录制控制器 ──▶ 切片输出
  └──▶ 音频采集(ffmpeg PCM) → VAD → 环形缓冲 → ASR → 关键词检测 ──触发──┘
  └──▶ 弹幕监听(WebSocket) → 密度统计 ──延迟确认──触发──────────────────┘
```

详见 [系统架构文档](docs/guide/architecture.md)。

## 🧪 测试

```bash
make test        # 单元 + 集成测试
make test-race   # 含竞态检测
```

- **`TestPipelineEndToEnd`**（默认运行）：ffmpeg 用 lavfi 实时合成 HLS 直播流，配合模拟 B 站 API 与脚本化 ASR，验证"触发 → 回溯 → 合并 → 切片输出"全链路，并校验下播后会话正确结束；无 ffmpeg/ffprobe 时自动跳过。
- **`TestRealLiveEndToEnd`**（`-tags reallive`）：真实直播间 + 真实 FunASR 服务 + 真实弹幕服务器，需要目标主播正在直播。

```bash
go test -tags reallive -run TestRealLiveEndToEnd ./internal/pipeline/
```

## 📚 文档

文档由 [VitePress](https://vitepress.dev) 管理（`docs/` 目录）：

```bash
cd docs && npm install && npm run dev   # http://localhost:5173
```

- [快速开始](docs/guide/quickstart.md)
- [关键词策略](docs/guide/keywords.md)
- [配置说明](docs/guide/configuration.md)
- [ASR 接入](docs/guide/asr.md)
- [参数调优](docs/guide/tuning.md)
- [常见问题](docs/guide/faq.md)

## 📁 项目结构

```
cmd/hymREC          主入口
internal/
  app/              服务生命周期、切片索引、事件广播
  asr/              ASR 引擎（none / FunASR-WS）
  bili/             B 站直播 API
  capture/          音频采集（ffmpeg 管道）
  config/           配置加载
  danmaku/          弹幕 WebSocket 协议与客户端
  keyword/          T1-T4 层级关键词检测
  pipeline/         主流水线
  recorder/         视频分段缓冲 + 录制控制器
  ringbuf/          音频环形缓冲
  web/              监控面板（HTTP API + SSE + 内嵌前端）
docs/               VitePress 文档
```

## 🤝 参考

- [keuin/slbr](https://github.com/keuin/slbr) — Bilibili livestream recorder（Go）
- [SocialSisterYi/bilibili-API-collect](https://github.com/SocialSisterYi/bilibili-API-collect) — B 站 API 整理
- [modelscope/FunASR](https://github.com/modelscope/FunASR) — 语音识别服务

## 📄 许可证

[AGPL-3.0](LICENSE) © hymREC contributors
