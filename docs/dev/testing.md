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
| `internal/recorder` | 分段时间解析、时间范围筛选、录制状态机 |
| `internal/pipeline` | **端到端集成测试** |

## 端到端集成测试

`TestRealLiveEndToEnd`（`-tags reallive`）验证完整链路，全程使用**真实内容**：

1. 真实 B 站直播间（默认房间 545068，可用 `HYMREC_TEST_ROOM` 指定，须正在直播）；
2. 真实直播流 + 真实弹幕服务器（密度触发）；
3. 真实 FunASR 流式服务（`HYMREC_ASR_ENDPOINT`，默认 `ws://127.0.0.1:10095`），关键词为直播高频口语；
4. 运行流水线，验证自动触发、回溯、合并产出切片，并用 ffprobe 校验媒体流有效。

运行方式：

```bash
# 启动 FunASR 服务后
go test -tags reallive -run TestRealLiveEndToEnd ./internal/pipeline/
```

## 新增测试约定

- 单元测试对 HTTP 类外部依赖（B 站 API、FunASR、弹幕服务器）使用 `httptest` 模拟，隔离网络；
- 端到端验证必须使用真实内容（真实直播流、真实 ASR），不使用模拟数据；
- 需要覆盖核心行为的边界：空输入、流中断、并发访问。
