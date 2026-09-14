---
layout: home
hero:
  name: hymREC
  text: 恨也迷人风格的 B 站直播关键词触发录制
  tagline: 监控直播间，命中关键词自动产出含前 90 秒上下文的视频切片
  actions:
    - theme: brand
      text: 快速开始
      link: /guide/quickstart
    - theme: alt
      text: GitHub
      link: https://github.com/henyemiren/hymREC

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
    title: 流式 ASR
    details: 内置 FunASR WebSocket 客户端与热词注入，识别命中关键词即触发录制
---
