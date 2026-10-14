---
title: 论文
---

# 论文

《面向虚拟主播直播切片场景的关键词触发录制系统设计与实现》——中文论文（arXiv 格式，9 页），完整覆盖系统设计、层级关键词引擎、B 站接口适配、真实直播实验与局限讨论。

- 作者：Maicarons
- 许可：随项目以 [AGPL-3.0](https://github.com/Maicarons/hymREC/blob/main/LICENSE) 开源
- 源码：[`paper/main.tex`](https://github.com/Maicarons/hymREC/blob/main/paper/main.tex)

[打开 / 下载 PDF](/paper.pdf) · 编译方式：`xelatex main.tex`（需 ctex 与 TikZ）

## 在线预览

<iframe
  src="/hymREC/paper.pdf"
  title="论文预览"
  loading="lazy"
  style="width: 100%; height: 760px; border: 1px solid var(--vp-c-divider); border-radius: 8px;"
></iframe>

## 论文要点

| 章节 | 内容 |
| --- | --- |
| 相关工作 | 直播录制工具、流式 ASR、关键词检测、弹幕研究 |
| 系统设计 | 直播流获取（wbi 签名 / buvid3 / 弹幕 token）、四级关键词引擎 T1–T4、触发状态机 |
| 关键技术 | 分段缓冲与 90 秒回溯切片、弹幕密度辅助触发、FunASR 热词注入、延迟确认防误触 |
| 实验与验证 | 单元测试 + 真实直播端到端实验（ffprobe 校验切片有效性） |
| 讨论 | 局限性（依赖主播持续说话、ASR 漏识别）与未来工作 |

## 引用

```bibtex
@misc{hymrec2026,
  title  = {面向虚拟主播直播切片场景的关键词触发录制系统设计与实现},
  author = {Maicarons},
  year   = {2026},
  note   = {arXiv 格式中文稿},
  url    = {https://maicarons.github.io/hymREC/paper}
}
```
