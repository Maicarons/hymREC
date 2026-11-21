# 测试

```bash
make test          # 全部测试
make test-race     # 含竞态检测
make coverage      # 覆盖率报告
```

## 测试结构

| 包 | 测试内容 |
|----|----------|
| `internal/keyword` | T1-T4 触发逻辑、模糊匹配阈值、冷却、停止延迟、窗口残留 |
| `internal/ringbuf` | 环形覆盖、时间回溯、空缓冲 |
| `internal/bili` | HTTP 测试服务器下的房间信息、开播状态、流地址、未开播错误 |
| `internal/danmaku` | 协议编解码（明文/zlib/brotli/嵌套）、WebSocket 认证与弹幕回调 |
| `internal/asr` | FunASR 协议（WebSocket 模拟服务器） |
| `internal/config` | 默认值填充、YAML 覆盖、字段校验（含回溯窗口与缓冲保留时长的关系） |
| `internal/recorder` | 分段时间解析、时间范围筛选、录制状态机 |
| `internal/pipeline` | 弹幕密度窗口、VAD 静音判定、状态快照、在线设置、**端到端集成测试** |
| `internal/app` | 服务启停/重启、状态映射、切片索引与下载路径校验 |
| `internal/web` | HTTP API、SSE 推送、跨站防护、路径穿越拦截、日志环形缓冲 |

## 端到端集成测试

### TestPipelineEndToEnd（默认运行，无需外部服务）

1. ffmpeg 用 `lavfi` 实时合成 HLS 直播流（`testsrc` + `sine`），由本地 HTTP 服务提供；
2. `httptest` 模拟 B 站直播间接口（`room_init` / `getRoomInfoOld` / `playUrl`）；
3. 脚本化 ASR 引擎注入一次关键词文本，驱动"触发 → 回溯 → 合并 → 切片输出"；
4. 用 ffprobe 校验切片同时包含视频与音频流；
5. 把模拟开播状态置为未开播，验证会话结束（视频缓冲停止）且主循环继续等待，而不是退出或空转。

无 `ffmpeg` / `ffprobe` 时自动跳过。

### TestRealLiveEndToEnd（`-tags reallive`，真实内容）

连接真实 B 站直播间、真实弹幕服务器与真实 FunASR 服务，验证真实内容下的完整链路。要求目标主播正在直播。

- `HYMREC_TEST_ROOM`：目标直播间号，默认 `22886883`；
- `HYMREC_ASR_ENDPOINT`：FunASR 流式服务地址，默认 `ws://127.0.0.1:10095`。

```bash
# 启动 FunASR 服务后
go test -tags reallive -run TestRealLiveEndToEnd ./internal/pipeline/
```

## 新增测试约定

- 默认测试不依赖网络与真实服务：HTTP 类外部依赖（B 站 API、FunASR、弹幕服务器）用 `httptest` 模拟；
- 需要真实直播/ASR 的验证放在 `-tags reallive` 下，与默认测试隔离；
- 需要覆盖核心行为的边界：空输入、流中断、配置非法、并发访问。
