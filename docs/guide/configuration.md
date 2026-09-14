# 配置说明

配置文件为 YAML 格式（`config.example.yaml` 提供了完整示例），未配置的字段使用默认值。

## 基础配置

```yaml
room_id: 21452505             # 目标直播间号（支持短号，自动解析真实房间号）
live_check_interval: 60s      # 开播检测轮询间隔
quality: 10000                # 画质：10000 = 原画
platform: web                 # 拉流平台：web 返回 FLV（直写录制），h5 返回 m3u8
stream_retry_delay: 5s        # 流中断后的重连等待时间
```

## 音频与缓冲

```yaml
sample_rate: 16000            # ASR 采样率（Hz）
chunk_ms: 600                 # 每个音频块时长（毫秒）
ring_buffer_seconds: 90       # 音频环形缓冲回溯时长（秒），也是视频回溯的前滚秒数
```

## ASR

```yaml
asr:
  type: funasr-ws             # none | mock | funasr-ws
  endpoint: ws://127.0.0.1:10095
  mode: 2pass
  chunk_size: [0, 10, 5]      # [encoder, decoder, offset]
  wav_name: hymrec_live
  hotwords: { 星瞳: 5, 瞳子: 4 }
  mock_texts: ["星瞳今天状态很好", "随便聊点别的"]   # mock 模式轮询输出
  mock_interval: 8s
```

| 类型 | 说明 |
|------|------|
| `none` | 关闭语音识别，仅依赖弹幕触发 |
| `mock` | 模拟 ASR，按固定间隔输出预设文本，用于演示与测试 |
| `funasr-ws` | 对接 FunASR 流式服务（推荐，见 [ASR 接入](./asr)） |

## VAD 静音检测

```yaml
vad:
  enabled: true
  energy_threshold: 500.0     # RMS 能量阈值，低于该值视为静音跳过 ASR
```

## 关键词

见 [关键词策略](./keywords)：

```yaml
keyword:
  stop_delay_seconds: 120
  cooldown_seconds: 5
  t2_threshold: 0.75
  t3_cooccur_window: 30.0
  t1: [...]
  t2: [...]
  t3: [...]
  t4: [...]
```

## 弹幕

```yaml
danmaku:
  enabled: true
  keywords: ["星瞳", "瞳", "XT", "小星星"]  # 参与密度统计的关键词
  density_threshold: 5        # 窗口内含关键词弹幕条数达到该值即触发
  window_seconds: 30          # 密度统计滑动窗口
  confirm_delay: 2s           # 达标后的延迟确认，防误触
```

## 录制输出

```yaml
recorder:
  output_dir: ./recordings            # 切片输出目录
  video_buffer_dir: ./video_buffer    # 视频分段缓冲目录
  video_keep_minutes: 5               # 缓冲保留时长（分钟）
  video_segment_seconds: 10           # 每个 TS 分段时长（秒）
  merge_format: ts                    # 切片输出格式：ts（直接合并）| mp4（转封装）
  filename_prefix: hym_clip
  ffmpeg_path: ffmpeg                 # ffmpeg 可执行文件路径
```

## 日志

```yaml
log_level: info               # debug | info | warn | error
```

## 命令行覆盖

启动参数可覆盖配置文件（`hymrec -h` 查看全部）：

```bash
./hymrec -c config.yaml -room 21452505 -asr-type mock -platform web -out ./recordings
```
