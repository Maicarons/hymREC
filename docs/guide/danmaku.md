# 弹幕辅助触发

仅靠 ASR 语音关键词可能遗漏部分素材（识别错误、主播未出声但弹幕刷屏等）。弹幕密度是补充触发信号：当弹幕中大量出现关键词时，说明直播间正在讨论相关内容。

## 工作方式

1. `pipeline` 通过 `getDanmuInfo`（wbi 签名 + buvid3 cookie）获取房间**专属弹幕服务器地址与认证 token**；
2. `danmaku.Client` 通过 WebSocket 连接专属弹幕服务器（`wss://<host>:<wss_port>/sub`），携带 `key` 认证；
3. 完成认证（`protover=2`，zlib 压缩）后持续接收弹幕，每 30 秒发送心跳；
4. 每条 `DANMU_MSG` 弹幕文本与关键词列表比对；
5. **滑动窗口统计**：最近 `window_seconds`（默认 30 秒）内含关键词弹幕条数达到 `density_threshold`（默认 5）时触发；
6. **延迟确认**：达标后等待 `confirm_delay`（默认 2 秒）再次确认仍达标才触发，降低误触发。

> 说明：B 站已对公共弹幕服务器 `broadcastlv.chat.bilibili.com` 收紧风控，无 token 连接会被立即断开（1006）。系统会自动走 `getDanmuInfo` 获取专属服务器与 token，无需手动配置。

## 为什么弹幕有用

弹幕反应通常比语音早 **1-3 秒**——观众看到直播画面（动作、字幕、BGM）的反应速度快于主播说话。因此弹幕可以起到"预告"作用，让系统提前进入录制状态，配合 90 秒回溯覆盖更完整的上下文。

## 融合触发策略

| 触发来源 | 条件 | 动作 |
|----------|------|------|
| ASR 关键词 | T1/T2 命中 | 立即触发 |
| ASR 关键词 | T3 + T1/T2 联合 | 立即触发 |
| 弹幕密度 | 窗口内 ≥ 阈值条 | 延迟确认后触发 |
| 双重验证 | ASR 与弹幕同时命中 | 触发并标记高置信度素材 |

## 配置

```yaml
danmaku:
  enabled: true
  keywords: ["星瞳", "瞳", "XT", "小星星"]
  density_threshold: 5
  window_seconds: 30
  confirm_delay: 2s
```

## 协议实现

弹幕包协议（`internal/danmaku`）完整实现：

- 16 字节包头：`packetLen(4) + headerLen(2) + version(2) + op(4) + seq(4)`；
- 操作码：`2` 心跳、`3` 心跳回复、`5` 消息、`7` 认证、`8` 认证回复；
- 认证：`{"uid":0,"roomid":X,"protover":2,"platform":"web","type":2,"key":"<token>"}`；
- 压缩：`version=0` JSON 明文、`2` zlib、`3` brotli（自动解压并递归拆包）；
- 断线自动重连（指数退避，最长 30 秒）；
- 握手注意事项：需携带浏览器 `User-Agent` 与 `Origin: https://live.bilibili.com`，且**不要**声明 WebSocket 子协议（B 站服务器会拒绝）。

## wbi 签名

`getDanmuInfo` 需要 wbi 签名（`internal/bili/wbi.go`）：

1. 访问 `https://www.bilibili.com/` 获取 `buvid3` cookie（并复制到 `.bilibili.com` 公共域）；
2. 通过主站 `nav` 接口获取 `img_key`/`sub_key`（未登录返回 -101 但 `wbi_img` 仍有效）；
3. 按固定打乱表生成 `mixin_key`，参数排序 + 时间戳 + MD5 生成 `w_rid`；
4. 携带 `wts`/`w_rid` 调用 `getDanmuInfo` 获取 token 与专属服务器列表。
