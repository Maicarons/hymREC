---
layout: home
hero:
  name: hymREC
  text: 面向虚拟主播直播切片的关键词触发录制
  tagline: 监控直播间，命中关键词或弹幕密度达标时，自动产出含前 90 秒上下文的视频切片
  actions:
    - theme: brand
      text: 快速开始
      link: /guide/quickstart
    - theme: alt
      text: 配置说明
      link: /guide/configuration
    - theme: alt
      text: GitHub
      link: https://github.com/Maicarons/hymREC
    - theme: alt
      text: 论文
      link: /paper

features:
  - icon: 🎯
    title: 层级关键词引擎
    details: T1 精确 / T2 模糊 / T3 梗词联合 / T4 否定屏蔽，匹配"量大管饱"的切片风格
  - icon: 📹
    title: 视频回溯切片
    details: ffmpeg 分段持续缓冲，触发时自动回溯前 90 秒上下文并合并输出 TS/MP4
  - icon: 💬
    title: 弹幕辅助触发
    details: 实时监听弹幕密度，弥补 ASR 漏识别；弹幕往往比语音早 1-3 秒反应
  - icon: 🗣️
    title: 流式 ASR 与热词
    details: 内置 FunASR WebSocket 客户端与分级热词注入，识别命中关键词即触发录制
  - icon: 🔁
    title: 断线自愈
    details: 视频缓冲与音频采集自动重连，流变化时重建采集器，弹幕指数退避重连
  - icon: ⏱️
    title: 触发即续期
    details: 录制期间再次命中即刷新停止倒计时，静默超过停止延迟才自动切片收尾
---

## 快速开始

环境要求：Go 1.21+、[ffmpeg](https://ffmpeg.org/)（加入 PATH）。

```bash
git clone https://github.com/Maicarons/hymREC.git
cd hymREC

make build
cp config.example.yaml config.yaml   # 修改 room_id
./bin/hymREC -c config.yaml
```

产物目录：`recordings/`（触发录制产出的切片）、`video_buffer/`（持续分段缓冲，自动清理）。

## 工作原理

```
B站流 → 视频环形缓冲(ffmpeg TS分段) ──触发时回溯前90秒──▶ 录制控制器 ──▶ 切片输出
  └──▶ 音频采集(ffmpeg PCM) → VAD → 环形缓冲 → ASR → 关键词检测 ──触发──┘
  └──▶ 弹幕监听(WebSocket) → 密度统计 ──延迟确认──触发──────────────────┘
```

| 环节 | 机制 |
| --- | --- |
| 触发 | ASR 命中 T1–T4 关键词，或弹幕密度 30 秒窗口 ≥ 5 条并通过延迟确认 |
| 回溯 | 触发时从视频缓冲取 `[触发前 90 秒, 停止时刻]` 的分段，ffmpeg concat 合并 |
| 续期 | 录制期间再次命中关键词（或弹幕）会刷新停止倒计时，继续录制 |
| 停止 | 最后一次触发后静默 `stop_delay_seconds`（默认 120 秒）自动合并输出切片 |

切片长度 = `ring_buffer_seconds` 回溯时长 + 最后一次触发后的持续时长，而非固定 90 秒切一段。

## 文档与论文

- [系统架构](/guide/architecture) · [关键词策略](/guide/keywords) · [参数调优](/guide/tuning) · [常见问题](/guide/faq)
- [论文：面向虚拟主播直播切片场景的关键词触发录制系统设计与实现](/paper)（9 页，含真实直播实验）
- 代码与 Issue：https://github.com/Maicarons/hymREC

::: tip 免责声明
本项目仅供学习交流使用。请在征得对方许可的情况下录制，勿用于商业用途或大规模录制，详见[法律声明](/guide/legal)。
:::
