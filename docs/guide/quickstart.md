# 快速开始

## 环境要求

- **Go 1.21+**
- **ffmpeg**（需加入 PATH）：用于音频抽取、视频分段与切片合并
- **FunASR 服务**：流式语音识别服务（推荐，见 [ASR 接入](./asr)）

## 1. 克隆与构建

```bash
git clone https://github.com/henyemiren/hymREC.git
cd hymREC

# 构建
make build
# 或
go build -o hymREC ./cmd/hymREC
```

## 2. 准备配置文件

```bash
cp config.example.yaml config.yaml
```

编辑 `config.yaml`，至少修改：

```yaml
room_id: 21452505        # 目标直播间号（支持短号）
asr:
  type: funasr-ws        # none（仅弹幕）| funasr-ws（真实 FunASR）
```

完整配置项见 [配置说明](./configuration)。

## 3. 启动

```bash
# 使用配置文件
./hymREC -c config.yaml

# 或用命令行参数覆盖
./hymREC -room 21452505
```

启动后系统会：

1. 轮询直播间开播状态（默认 60 秒一次）；
2. 开播后获取直播流地址；
3. 启动视频分段缓冲（`video_buffer/`）与音频采集；
4. 监听弹幕（默认启用）；
5. 命中关键词时开始录制，关键词消失超过停止延迟后自动停止并合并切片。

## 4. 查看输出

```
recordings/    ← 触发录制产出的视频切片（.ts 或 .mp4）
video_buffer/  ← 持续分段录制的缓冲（自动清理旧段）
```

## 5. 接入 FunASR（可选）

部署 FunASR 流式服务后，修改配置：

```yaml
asr:
  type: funasr-ws
  endpoint: ws://127.0.0.1:10095   # FunASR runtime WebSocket 地址
  mode: 2pass
  chunk_size: [0, 10, 5]
  hotwords: { "星瞳": 5 }
```

详见 [ASR 接入](./asr)。

## 运行测试

```bash
make test          # 单元测试 + 集成测试
make test-race     # 含竞态检测
```

> 端到端集成测试需要 ffmpeg，未安装时会自动跳过。
