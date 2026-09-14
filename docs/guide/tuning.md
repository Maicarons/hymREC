# 参数调优

| 参数 | 默认值 | 调优方向 | 说明 |
|------|--------|----------|------|
| `ring_buffer_seconds` | 90s | ↑ 需要更多"断章取义"上下文 | 音频缓冲内存 = 采样率 × 2B × 秒数 |
| `stop_delay_seconds` | 120s | ↓ 提高录制频率 | 越短越"量大管饱"，但片段碎片化 |
| `t2_threshold` | 0.75 | ↓ 提高召回率 | 越低越容易误触发 |
| `t3_cooccur_window` | 30s | ↑ 扩大联合触发范围 | T3 词与 T1/T2 间隔多久仍可联合触发 |
| `density_threshold` | 5 | ↓ 更敏感 | 30 秒内几条含关键词弹幕触发 |
| `video_keep_minutes` | 5min | 根据磁盘容量调整 | 低码率 TS 约 2MB/分钟 |
| `video_segment_seconds` | 10s | ↑ 减少分段数 | 段越长，回溯粒度越粗 |
| `energy_threshold` | 500 | ↑ 更激进跳过静音 | ASR 资源有限时提高 |

## 按场景推荐

### 量大管饱（恨也迷人风格）

```yaml
ring_buffer_seconds: 90
keyword:
  stop_delay_seconds: 120    # 同话题反复出现，无需长时间等待
  cooldown_seconds: 5        # 缩短冷却，提高触发频率
  t2_threshold: 0.75
  t3_cooccur_window: 30.0
danmaku:
  density_threshold: 5
```

### 高精度（少而精）

```yaml
ring_buffer_seconds: 60
keyword:
  stop_delay_seconds: 180
  cooldown_seconds: 15
  t2_threshold: 0.85          # 更严格的模糊匹配
  t3_cooccur_window: 15.0
danmaku:
  density_threshold: 10       # 需要更多弹幕才触发
  confirm_delay: 5s
```

## 注意事项

- **回溯窗口与停止延迟的平衡**：`ring_buffer_seconds` 决定切片最长能回溯多久；`stop_delay_seconds` 决定切片能持续多久。两者相加是典型切片长度（如 90s + 120s ≈ 3.5 分钟）；
- **模糊匹配阈值**：基于 LCS 的相似度对 2 字中文词只有 0 / 0.5 / 1 三个可能值，阈值 0.75 意味着 2 字词基本需要完全匹配；对 3 字以上词才有容错空间；
- **弹幕延迟确认**：`confirm_delay` 过短易误触发（弹幕峰值噪声），过长会错过触发时机；
- **磁盘**：视频缓冲按 `video_keep_minutes` 自动清理，但触发合并时仍会一次性占用回溯窗口对应的磁盘空间。
