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
| `internal/bili` | mock HTTP 服务器下的房间信息、开播状态、流地址、未开播错误 |
| `internal/danmaku` | 协议编解码（明文/zlib/brotli/嵌套）、WebSocket 认证与弹幕回调 |
| `internal/asr` | Mock 引擎行为、FunASR 协议（WebSocket 模拟服务器） |
| `internal/recorder` | 分段时间解析、时间范围筛选、录制状态机 |
| `internal/pipeline` | **端到端集成测试** |

## 端到端集成测试

`TestPipelineEndToEnd` 验证完整链路：

1. 用 ffmpeg 实时生成 FLV 测试流（testsrc2 画面 + 正弦音），通过 HTTP 服务器对外提供（带环形缓存，新连接可重放开头）；
2. mock B 站 API（开播 + 流地址）；
3. mock ASR 每 3 秒输出含关键词文本；
4. 运行流水线，验证自动触发、回溯、合并并产出非空切片。

> 该测试需要 ffmpeg，未安装时自动跳过（`t.Skip`）。

## 新增测试约定

- 对外部服务（B 站 API、FunASR、弹幕服务器）一律使用 `httptest` 模拟，不依赖真实网络；
- 需要覆盖核心行为的边界：空输入、流中断、并发访问。
