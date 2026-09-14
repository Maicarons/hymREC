# 面向 B站虚拟主播“恨也迷人”的直播关键词触发录制方案

## 一、目标场景与风格分析

本方案面向的目标是：**监控B站虚拟主播直播间，以恨也迷人（星瞳首席切片员）的切片风格为目标，自动录制含关键词触发片段的直播视频素材。**

恨也迷人是星瞳的知名切片人，主打“量大管饱也有爆点”，核心技术为任何直播间有关星瞳的都会切，风格上采用“洗脑式片头”和“断章取义”的剪辑手法，多次获得星瞳本人的认知和回应。恨也迷人通过高强度切片，甚至别的V提一嘴星瞳都会被切的程度，让V圈的人多多少少认识他。

这意味着，本系统的关键词策略需要比前版通用方案**更加激进**——“量大管饱”意味着触发条件要宽松，“有爆点”意味着关键词覆盖面要广，不能只盯着一个精确词。


## 二、系统架构适配

整体架构沿用前版方案的多线程流水线，但针对B站虚拟主播场景做以下针对性调整：

```
┌──────────────────────────────────────────────────────────────────┐
│                    恨也迷人风格录制系统                            │
│                                                                   │
│  ┌─────────────┐    ┌──────────────┐    ┌─────────────────────┐  │
│  │ B站流获取    │───▶│  环形缓冲区   │    │  ASR 处理线程        │  │
│  │ (playUrl→   │    │ (deque实现)  │───▶│ (Paraformer-zh-     │  │
│  │  m3u8/FLV)  │    │ 保留最近90秒  │    │  streaming)         │  │
│  └─────────────┘    └──────┬───────┘    └──────────┬──────────┘  │
│                            │                       │              │
│                            │  触发时回溯           │ 增量文本      │
│                            ▼                       ▼              │
│                     ┌─────────────┐    ┌─────────────────────┐    │
│                     │  录制控制器  │◀───│  关键词检测引擎      │    │
│                     │ (状态机)    │    │ (层级关键词+模糊匹配)│    │
│                     └──────┬──────┘    └─────────────────────┘    │
│                            │                                      │
│                            ▼                                      │
│                     ┌─────────────┐    ┌─────────────────────┐    │
│                     │ 视频素材输出 │───▶│ 后续切片/剪辑流程   │    │
│                     │ (含前90秒)  │    │ (FFmpeg + 弹幕渲染) │    │
│                     └─────────────┘    └─────────────────────┘    │
└──────────────────────────────────────────────────────────────────┘
```

**关键调整点**：

1. 回溯窗口从60秒**扩展到90秒**——恨也迷人的切片风格是“断章取义”式的，需要在关键词出现前捕获更多上下文，以便后续剪辑时选择性地截取有“断章取义”效果的片段。
2. 停止延迟**缩短到120秒**——恨也迷人主打“量大管饱”，这意味着关键词密度高，同一话题通常在短时间内反复出现，不需要长时间等待。
3. 录制输出从WAV**升级为FLV/MP4**——因为需要与视频流对齐，以便后续渲染弹幕和进行FFmpeg剪辑。


## 三、B站直播流拉取

### 3.1 获取直播流地址

B站直播流需要通过官方API获取，核心流程为：**短号 → 真实RoomID → playUrl接口 → m3u8/FLV地址**。

关键点在于请求流地址接口时**强制指定参数 `platform=h5`**，这样B站后端会返回兼容性极佳的HLS（.m3u8）格式。

```python
# bili_stream.py
"""B站直播流地址获取"""

import requests
import re

HEADERS = {
    "User-Agent": (
        "Mozilla/5.0 (iPhone; CPU iPhone OS 13_2_3 like Mac OS X) "
        "AppleWebKit/605.1.15 (KHTML, like Gecko) Version/13.0.3 "
        "Mobile/15E148 Safari/604.1"
    ),
    "Referer": "https://live.bilibili.com/",
}


def get_real_room_id(short_id: int) -> int:
    """短号 → 真实房间号"""
    url = "https://api.live.bilibili.com/room/v1/Room/room_init"
    resp = requests.get(url, params={"id": short_id}, headers=HEADERS)
    data = resp.json()
    if data["code"] != 0:
        raise RuntimeError(f"获取房间ID失败: {data.get('message')}")
    return data["data"]["room_id"]


def get_stream_url(room_id: int, qn: int = 10000) -> str:
    """
    获取直播流地址。
    qn=10000 表示原画；指定 platform=h5 返回 .m3u8 格式。
    """
    real_id = get_real_room_id(room_id)
    url = "https://api.live.bilibili.com/room/v1/Room/playUrl"
    params = {
        "cid": real_id,
        "qn": qn,
        "platform": "h5",  # 关键：返回 m3u8 格式
    }
    resp = requests.get(url, params=params, headers=HEADERS)
    data = resp.json()
    if data["code"] != 0:
        raise RuntimeError(f"获取流地址失败: {data.get('message')}")

    durl = data["data"]["durl"]
    if not durl:
        raise RuntimeError("未获取到可用流地址（主播可能未开播）")
    return durl[0]["url"]


def check_live_status(room_id: int) -> dict:
    """检查直播间开播状态"""
    url = "https://api.live.bilibili.com/room/v1/Room/getRoomInfoOld"
    params = {"mid": room_id}
    resp = requests.get(url, params=params, headers=HEADERS)
    data = resp.json().get("data", {})
    return {
        "room_id": data.get("roomid", 0),
        "live_status": data.get("liveStatus", 0),  # 0=未开播, 1=直播中
        "title": data.get("title", ""),
    }
```

### 3.2 音频采集适配

针对B站虚拟主播场景，音频采集模块需要在PyAV基础上做两个适配：

1. **只提取音频流**，丢弃视频流，降低带宽和CPU占用
2. **同时输出音频块和视频流时间戳**，以便后续录制视频素材时对齐

```python
# bili_audio_source.py
"""B站直播音频采集（仅提取音频）"""

import av
import numpy as np
from config import SAMPLE_RATE, CHUNK_SAMPLES
import logging

logger = logging.getLogger(__name__)


class BiliAudioSource:
    """从B站直播流中提取16kHz单声道PCM音频"""

    def __init__(self, stream_url: str):
        self.stream_url = stream_url

    def iter_chunks(self):
        """生成器：每次产出9600采样点（600ms）音频块"""
        while True:
            try:
                container = av.open(
                    self.stream_url,
                    options={"rtsp_transport": "tcp", "stimeout": "5000000"},
                )
                audio_stream = container.streams.audio[0]
                resampler = av.AudioResampler(
                    format="s16", layout="mono", rate=SAMPLE_RATE
                )
                buffer = np.array([], dtype=np.int16)

                for frame in container.decode(audio_stream):
                    frame = resampler.resample(frame)
                    samples = frame.to_ndarray().flatten().astype(np.int16)
                    buffer = np.concatenate([buffer, samples])

                    while len(buffer) >= CHUNK_SAMPLES:
                        yield buffer[:CHUNK_SAMPLES].copy()
                        buffer = buffer[CHUNK_SAMPLES:]

            except Exception as e:
                logger.warning(f"B站流中断，5秒后重连: {e}")
                import time
                time.sleep(5)
                continue
```


## 四、恨也迷人风格的关键词策略

这是本方案与通用方案最大的区别所在。恨也迷人的切片风格决定了关键词不能是简单的精确匹配，而需要**层级化的关键词体系**。

### 4.1 关键词层级设计

基于恨也迷人“任何直播间有关xt的都会切”的风格，关键词分为三个层级：

| 层级 | 匹配类型 | 关键词示例 | 权重 | 说明 |
|------|----------|-----------|------|------|
| **T1 核心触发** | 精确匹配 | 星瞳、xingtong、XT | 最高 | 直接触发录制 |
| **T2 关联触发** | 模糊匹配（阈值0.75）| 瞳、小星星、瞳子、星瞳酱 | 高 | 可能触发录制 |
| **T3 梗/衍生触发** | 正则/列表 | 腾讯大小姐、CDD、虚无、云之彼端 | 中 | 联合T1/T2出现时触发 |
| **T4 否定词** | 排除 | 不是星瞳、星瞳的黑称 | 排除 | 避免误触发 |

### 4.2 为什么需要T3层级

恨也迷人的切片不仅切星瞳本人的直播，还切**其他V提及星瞳的片段**。这意味着系统需要监控的不仅是星瞳的直播间，还包括其他虚拟主播的直播间。当其他主播在直播中提到星瞳时，系统也应该触发录制。

T3层级的设计正是为了捕获这种情况——当“腾讯大小姐”“CDD”等与星瞳强相关的梗词出现时，即使没有直接提及“星瞳”二字，也可能成为有价值的切片素材。

### 4.3 关键词检测引擎（增强版）

```python
# keyword_detector_v2.py
"""恨也迷人风格的关键词检测引擎"""

import re
from difflib import SequenceMatcher
from typing import List, Tuple, Optional, Dict
import time
import logging

logger = logging.getLogger(__name__)


class HYMKeywordDetector:
    """
    恨也迷人风格的层级关键词检测。
    
    核心策略：
    - T1精确匹配 → 立即触发
    - T2模糊匹配（阈值可调）→ 触发
    - T3梗词 → 需与T1/T2在时间窗口内联合出现才触发
    - T4否定词 → 屏蔽触发
    """

    # T1: 核心关键词（精确匹配即触发）
    T1_KEYWORDS = [
        "星瞳", "XingTong", "XT",
    ]

    # T2: 关联关键词（模糊匹配，阈值0.75）
    T2_KEYWORDS = [
        "小星星", "瞳子", "瞳宝", "星瞳酱",
    ]

    # T3: 梗/衍生词（需联合触发）
    T3_KEYWORDS = [
        "腾讯大小姐", "CDD", "虚无", "云之彼端",
        "深月的风吟鸟唱", "邻家女孩", "瞳工",
    ]

    # T4: 否定词（出现时屏蔽触发）
    T4_BLOCKERS = [
        "不是星瞳", "星瞳的黑称",
    ]

    def __init__(
        self,
        stop_delay_seconds: int = 120,
        cooldown_seconds: int = 5,
        t2_threshold: float = 0.75,
        t3_cooccur_window: float = 30.0,
    ):
        self.stop_delay_seconds = stop_delay_seconds
        self.cooldown_seconds = cooldown_seconds
        self.t2_threshold = t2_threshold
        self.t3_cooccur_window = t3_cooccur_window

        # 滑动窗口
        self._token_buffer: List[str] = []
        self._max_buffer = 40

        # T3 联合触发跟踪
        self._recent_t1t2_hits: List[Tuple[float, str]] = []

        # 状态
        self._last_hit_time: Optional[float] = None
        self._is_active = False
        self._last_trigger_time: Optional[float] = None

    def feed(self, text: str, timestamp: float) -> Tuple[bool, bool]:
        """
        输入ASR增量文本，返回 (是否触发, 是否停止)。
        """
        if text:
            self._token_buffer.extend(list(text))
            if len(self._token_buffer) > self._max_buffer:
                self._token_buffer = self._token_buffer[-self._max_buffer:]

        # 更新T1/T2命中记录（用于T3联合触发）
        self._recent_t1t2_hits = [
            (t, kw) for t, kw in self._recent_t1t2_hits
            if timestamp - t <= self.t3_cooccur_window
        ]

        hit = self._detect(timestamp)
        triggered = False
        should_stop = False

        if hit:
            self._last_hit_time = timestamp
            if not self._is_active:
                if (self._last_trigger_time is None or
                        timestamp - self._last_trigger_time >= self.cooldown_seconds):
                    self._is_active = True
                    self._last_trigger_time = timestamp
                    triggered = True
                    logger.info(
                        f"[触发] 关键词命中 | 时间: {timestamp:.1f}s"
                    )

        if self._is_active and self._last_hit_time is not None:
            if timestamp - self._last_hit_time >= self.stop_delay_seconds:
                self._is_active = False
                should_stop = True
                logger.info(
                    f"[停止] 关键词已 {self.stop_delay_seconds}s 未出现"
                )

        return triggered, should_stop

    def _detect(self, timestamp: float) -> bool:
        """执行层级关键词检测"""
        if not self._token_buffer:
            return False

        window_text = "".join(self._token_buffer)

        # T4: 否定词检查
        for blocker in self.T4_BLOCKERS:
            if blocker in window_text:
                return False

        # T1: 精确匹配
        for kw in self.T1_KEYWORDS:
            if kw in window_text:
                self._recent_t1t2_hits.append((timestamp, kw))
                return True

        # T2: 模糊匹配
        for kw in self.T2_KEYWORDS:
            if self._fuzzy_match(kw):
                self._recent_t1t2_hits.append((timestamp, kw))
                return True

        # T3: 梗词——需与T1/T2联合触发
        for kw in self.T3_KEYWORDS:
            if kw in window_text:
                # 检查近期是否有T1/T2命中
                if self._recent_t1t2_hits:
                    logger.debug(
                        f"T3联合触发: '{kw}' + "
                        f"{self._recent_t1t2_hits[-1][1]}"
                    )
                    return True
                # 无T1/T2时，T3单独出现不触发
                logger.debug(f"T3词 '{kw}' 单独出现，不触发")

        return False

    def _fuzzy_match(self, keyword: str) -> bool:
        """模糊匹配：滑动窗口中所有等长子串"""
        kw_len = len(keyword)
        if len(self._token_buffer) < kw_len:
            return False
        for i in range(len(self._token_buffer) - kw_len + 1):
            candidate = "".join(self._token_buffer[i:i + kw_len])
            ratio = SequenceMatcher(None, keyword, candidate).ratio()
            if ratio >= self.t2_threshold:
                return True
        return False

    @property
    def is_active(self) -> bool:
        return self._is_active
```

### 4.4 热词注入策略

将T1和T2关键词注入Paraformer的热词表，**权重设为最高5**，以大幅提升ASR对这些词的识别准确率。T3梗词设为权重3-4。

```python
# 热词表配置
HYM_HOTWORDS = {
    # T1 核心（权重5）
    "星瞳": 5,
    "小星星": 5,
    # T2 关联（权重4）
    "瞳子": 4,
    "瞳宝": 4,
    "星瞳酱": 4,
    # T3 梗词（权重3）
    "腾讯大小姐": 3,
    "CDD": 3,
    "虚无": 3,
    "云之彼端": 3,
    "深月的风吟鸟唱": 3,
    "邻家女孩": 3,
}
```


## 五、弹幕辅助触发（推荐启用）

恨也迷人的切片风格“量大管饱”，仅靠ASR语音关键词可能遗漏部分素材。**弹幕密度**是另一个重要的触发信号——当弹幕中大量出现“星瞳”“瞳”等词时，即使ASR未能准确识别，也说明直播间正在讨论相关内容。

### 5.1 弹幕采集

B站弹幕通过WebSocket协议实时推送，接入方式如下：

```python
# danmaku_listener.py
"""B站直播弹幕实时监听"""

import asyncio
import websockets
import json
import zlib
import logging

logger = logging.getLogger(__name__)

DANMAKU_WS_URL = "wss://broadcastlv.chat.bilibili.com/sub"


class DanmakuListener:
    """监听B站直播间弹幕，统计关键词密度"""

    def __init__(self, room_id: int, keywords: list):
        self.room_id = room_id
        self.keywords = keywords
        # 滑动窗口：最近30秒的弹幕
        self._danmaku_window = []
        self._window_seconds = 30
        self._density_threshold = 5  # 30秒内出现5条含关键词的弹幕则触发

    async def connect(self, on_trigger):
        """连接弹幕服务器并持续监听"""
        async with websockets.connect(DANMAKU_WS_URL) as ws:
            # 发送认证包
            auth = {
                "uid": 0,
                "roomid": self.room_id,
                "protover": 2,
                "platform": "web",
                "type": 2,
            }
            await ws.send(self._encode_packet(auth, 7))
            # 心跳
            asyncio.create_task(self._heartbeat(ws))

            async for message in ws:
                await self._handle_message(message, on_trigger)

    async def _heartbeat(self, ws):
        while True:
            await asyncio.sleep(30)
            try:
                await ws.send(self._encode_packet({}, 2))
            except Exception:
                break

    async def _handle_message(self, data, on_trigger):
        try:
            body = self._decode_packet(data)
            for pkt in body:
                if pkt.get("cmd") == "DANMU_MSG":
                    text = pkt["info"][1]
                    self._check_danmaku(text, on_trigger)
        except Exception:
            pass

    def _check_danmaku(self, text: str, on_trigger):
        """检查弹幕是否包含关键词"""
        import time
        now = time.time()
        # 清理过期弹幕
        self._danmaku_window = [
            (t, txt) for t, txt in self._danmaku_window
            if now - t <= self._window_seconds
        ]
        # 检查关键词
        for kw in self.keywords:
            if kw in text:
                self._danmaku_window.append((now, text))
                break
        # 判断密度
        if len(self._danmaku_window) >= self._density_threshold:
            on_trigger("danmaku_density", now)

    @staticmethod
    def _encode_packet(payload: dict, op: int) -> bytes:
        body = json.dumps(payload).encode("utf-8")
        header = (
            len(body).to_bytes(4, "big") +
            (16).to_bytes(2, "big") +
            (1).to_bytes(2, "big") +
            op.to_bytes(4, "big") +
            (1).to_bytes(4, "big")
        )
        return header + body

    @staticmethod
    def _decode_packet(data: bytes):
        """解析B站弹幕协议包（简化版，仅处理JSON格式）"""
        if len(data) < 16:
            return []
        try:
            body = data[16:].decode("utf-8")
            return [json.loads(body)]
        except Exception:
            return []
```

### 5.2 弹幕与ASR的融合触发策略

| 触发来源 | 条件 | 动作 |
|----------|------|------|
| ASR关键词 | T1/T2命中 | 立即触发 |
| ASR关键词 | T3+T1/T2联合 | 立即触发 |
| 弹幕密度 | 30秒内≥5条含关键词弹幕 | 触发（延迟2秒确认） |
| 双重验证 | ASR和弹幕同时命中 | 触发并标记为高置信度素材 |

弹幕触发的优势在于：**弹幕反应通常比语音早1-3秒**（观众看到直播画面的反应速度），因此弹幕可以起到“预告”作用，让系统提前准备录制。


## 六、录制输出：从WAV到视频素材

### 6.1 输出格式调整

通用方案输出WAV音频文件。但面向“恨也迷人风格”的切片场景，系统需要输出**视频素材片段**，原因如下：

1. 恨也迷人的切片是**视频形式的**，需要保留B站原视频画面
2. 后续需要**渲染弹幕**到视频中
3. 需要FFmpeg进行**二次剪辑**（添加片头、断章取义效果）

因此，录制模块需要调整为：在音频检测触发后，从环形缓冲区回溯音频时间戳，然后**同步回溯视频流**，使用FFmpeg将指定时间段的视频流和音频流合并输出为FLV/MP4文件。

### 6.2 录制控制器（视频版）

```python
# recorder_v2.py
"""视频素材录制控制器"""

import subprocess
import time
import os
import threading
from enum import Enum
from ring_buffer import AudioRingBuffer
from config import OUTPUT_DIR, RING_BUFFER_SECONDS
import logging

logger = logging.getLogger(__name__)


class RecordState(Enum):
    IDLE = "idle"
    RECORDING = "recording"


class VideoRecordingController:
    """
    触发时：从环形缓冲区获取回溯时间戳，用FFmpeg拉取对应时间段的直播流。
    输出：FLV格式视频素材（含前90秒）。
    """

    def __init__(self, ring_buffer: AudioRingBuffer, stream_url: str):
        self.ring_buffer = ring_buffer
        self.stream_url = stream_url
        self.state = RecordState.IDLE
        self._process = None
        self._current_filename = None
        self._record_start_time = None
        self._lock = threading.Lock()

    def on_trigger(self, timestamp: float):
        """关键词触发 → 启动FFmpeg录制"""
        with self._lock:
            if self.state == RecordState.RECORDING:
                return

            self.state = RecordState.RECORDING
            self._record_start_time = timestamp

            os.makedirs(OUTPUT_DIR, exist_ok=True)
            self._current_filename = os.path.join(
                OUTPUT_DIR, f"hym_clip_{int(timestamp)}.flv"
            )

            # 计算回溯时间偏移
            oldest_ts = self.ring_buffer.get_oldest_timestamp()
            pre_roll_seconds = min(
                timestamp - oldest_ts, RING_BUFFER_SECONDS
            )

            # 使用FFmpeg拉流录制（含回溯）
            # -ss 参数指定从流中已过去的时间点开始录制
            # 注意：B站直播流不支持精确的 -ss 回溯，
            # 实际实现中需要先持续录制缓冲区，触发后裁剪
            cmd = [
                "ffmpeg",
                "-i", self.stream_url,
                "-c", "copy",              # 不转码，直接复制
                "-t", str(3600),            # 最长录制1小时
                "-y",
                self._current_filename,
            ]

            self._process = subprocess.Popen(
                cmd,
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
            )

            logger.info(
                f"[视频录制开始] 文件: {self._current_filename}, "
                f"回溯偏移: {pre_roll_seconds:.1f}s"
            )

    def on_stop(self):
        """停止录制"""
        with self._lock:
            if self.state != RecordState.RECORDING:
                return

            self.state = RecordState.IDLE
            if self._process:
                self._process.terminate()
                try:
                    self._process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    self._process.kill()

            logger.info(
                f"[视频录制停止] 文件: {self._current_filename}"
            )
            self._process = None
            self._current_filename = None

    def cleanup(self):
        if self._process:
            self._process.kill()
            self._process = None
```

### 6.3 关于回溯视频的技术说明

**重要限制**：B站直播流的 `-ss` 回溯参数在FLV/HTTP-FLV格式下**不保证精确可用**。因此实际实现建议采用以下策略：

**推荐方案：持续低码率录制 + 触发裁剪**

1. 系统启动后，立即以**低码率（如500kbps）持续录制**直播流到一个环形视频缓冲区（按时间戳分段的TS/FLV文件）
2. 关键词触发时，从环形视频缓冲区中提取前90秒的片段
3. 触发后的实时片段以**高码率**录制
4. 将低码率回溯片段和高码率实时片段用FFmpeg concat合并

```python
# video_ring_buffer.py
"""视频环形缓冲区：持续低码率录制，支持回溯"""

import subprocess
import os
import time
import threading
import glob

class VideoRingBuffer:
    """
    以TS分段持续录制直播流，保留最近N分钟。
    触发时可提取指定时间段的TS片段。
    """

    def __init__(self, stream_url: str, keep_minutes: int = 5,
                 segment_seconds: int = 10):
        self.stream_url = stream_url
        self.keep_seconds = keep_minutes * 60
        self.segment_seconds = segment_seconds
        self.buffer_dir = "./video_buffer"
        self._process = None
        self._running = False

    def start(self):
        """启动持续录制"""
        os.makedirs(self.buffer_dir, exist_ok=True)
        self._running = True
        cmd = [
            "ffmpeg",
            "-i", self.stream_url,
            "-c", "copy",
            "-f", "segment",
            "-segment_time", str(self.segment_seconds),
            "-segment_format", "mpegts",
            "-segment_wrap", "100",     # 循环覆盖
            "-reset_timestamps", "1",
            os.path.join(self.buffer_dir, "seg_%03d.ts"),
        ]
        self._process = subprocess.Popen(
            cmd, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL
        )
        # 启动清理线程
        threading.Thread(target=self._cleanup_loop, daemon=True).start()

    def extract_pre_roll(self, trigger_time: float,
                         pre_seconds: int = 90) -> list:
        """提取触发前N秒的TS片段路径列表"""
        start_time = trigger_time - pre_seconds
        segments = sorted(glob.glob(
            os.path.join(self.buffer_dir, "seg_*.ts")
        ))
        # 根据文件修改时间筛选
        result = []
        for seg in segments:
            mtime = os.path.getmtime(seg)
            if mtime >= start_time:
                result.append(seg)
        return result

    def _cleanup_loop(self):
        """定期清理超出保留时间的旧片段"""
        while self._running:
            time.sleep(30)
            segments = sorted(
                glob.glob(os.path.join(self.buffer_dir, "seg_*.ts")),
                key=os.path.getmtime,
            )
            cutoff = time.time() - self.keep_seconds
            for seg in segments:
                if os.path.getmtime(seg) < cutoff:
                    try:
                        os.remove(seg)
                    except OSError:
                        pass

    def stop(self):
        self._running = False
        if self._process:
            self._process.terminate()
            self._process = None
```


## 七、完整主控制器（适配版）

```python
# main_hym.py
"""恨也迷人风格录制系统 - 主控制器"""

import threading
import queue
import time
import signal
import sys
import logging
import asyncio
import numpy as np

from config import (
    STOP_DELAY_SECONDS, COOLDOWN_SECONDS,
    RING_BUFFER_SECONDS, ENABLE_VAD,
)
from bili_stream import get_stream_url, check_live_status
from bili_audio_source import BiliAudioSource
from ring_buffer import AudioRingBuffer
from asr_engine import StreamingASREngine
from keyword_detector_v2 import HYMKeywordDetector
from recorder_v2 import VideoRecordingController
from video_ring_buffer import VideoRingBuffer
from danmaku_listener import DanmakuListener

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(name)s: %(message)s",
)
logger = logging.getLogger("main_hym")


class HYMRecorder:
    """恨也迷人风格直播关键词录制系统"""

    def __init__(self, room_id: int, enable_danmaku: bool = True):
        self.room_id = room_id
        self.enable_danmaku = enable_danmaku
        self.stream_url = None

        # 核心组件
        self.ring_buffer = AudioRingBuffer(
            sample_rate=16000, max_seconds=RING_BUFFER_SECONDS
        )
        self.asr = StreamingASREngine()
        self.detector = HYMKeywordDetector(
            stop_delay_seconds=STOP_DELAY_SECONDS,
            cooldown_seconds=COOLDOWN_SECONDS,
        )
        self.video_buffer = None
        self.recorder = None

        self._audio_queue = queue.Queue(maxsize=200)
        self._running = False
        self._threads = []

    def _wait_for_live(self):
        """等待主播开播"""
        while self._running:
            status = check_live_status(self.room_id)
            if status["live_status"] == 1:
                logger.info(f"检测到开播: {status['title']}")
                return True
            logger.info("未开播，60秒后重试...")
            time.sleep(60)
        return False

    def start(self):
        self._running = True

        # 等待开播
        if not self._wait_for_live():
            return

        # 获取流地址
        self.stream_url = get_stream_url(self.room_id)
        logger.info(f"流地址获取成功")

        # 初始化视频缓冲和录制器
        self.video_buffer = VideoRingBuffer(self.stream_url)
        self.recorder = VideoRecordingController(
            self.ring_buffer, self.stream_url
        )
        self.video_buffer.start()

        # 启动音频采集线程
        t1 = threading.Thread(
            target=self._capture_loop, name="capture", daemon=True
        )
        # 启动ASR处理线程
        t2 = threading.Thread(
            target=self._process_loop, name="process", daemon=True
        )
        self._threads = [t1, t2]
        for t in self._threads:
            t.start()

        # 弹幕监听（可选）
        if self.enable_danmaku:
            t3 = threading.Thread(
                target=self._danmaku_loop, name="danmaku", daemon=True
            )
            t3.start()
            self._threads.append(t3)

        logger.info(
            f"系统启动 | 房间: {self.room_id} | "
            f"停止延迟: {STOP_DELAY_SECONDS}s | "
            f"回溯: {RING_BUFFER_SECONDS}s | "
            f"弹幕: {'启用' if self.enable_danmaku else '禁用'}"
        )

        try:
            while self._running:
                time.sleep(1)
        except KeyboardInterrupt:
            self.stop()

    def _capture_loop(self):
        source = BiliAudioSource(self.stream_url)
        for chunk in source.iter_chunks():
            if not self._running:
                break
            ts = time.time()
            self.ring_buffer.write(chunk, ts)
            try:
                self._audio_queue.put_nowait((chunk, ts))
            except queue.Full:
                try:
                    self._audio_queue.get_nowait()
                    self._audio_queue.put_nowait((chunk, ts))
                except queue.Empty:
                    pass

    def _process_loop(self):
        while self._running:
            try:
                chunk, ts = self._audio_queue.get(timeout=1.0)
            except queue.Empty:
                continue

            if ENABLE_VAD and not self.asr.check_vad(chunk):
                continue

            text = self.asr.process_chunk(chunk)
            triggered, should_stop = self.detector.feed(text, ts)

            if triggered:
                self.recorder.on_trigger(ts)
            if should_stop:
                self.recorder.on_stop()

    def _danmaku_loop(self):
        """弹幕监听线程"""
        listener = DanmakuListener(
            self.room_id,
            keywords=["星瞳", "瞳", "XT", "小星星"],
        )

        def on_danmaku_trigger(source, ts):
            if self.recorder and self.recorder.state.value == "idle":
                logger.info(f"弹幕密度触发: {source}")
                self.recorder.on_trigger(ts)

        try:
            asyncio.run(listener.connect(on_danmaku_trigger))
        except Exception as e:
            logger.warning(f"弹幕监听异常: {e}")

    def stop(self):
        logger.info("正在停止系统...")
        self._running = False
        time.sleep(1.0)
        self.asr.flush()
        if self.recorder:
            self.recorder.on_stop()
        if self.video_buffer:
            self.video_buffer.stop()
        for t in self._threads:
            t.join(timeout=3.0)
        logger.info("系统已停止")


if __name__ == "__main__":
    if len(sys.argv) < 2:
        print("用法: python main_hym.py <B站直播间号>")
        print("示例: python main_hym.py 21452505")
        sys.exit(1)

    room_id = int(sys.argv[1])
    recorder = HYMRecorder(room_id, enable_danmaku=True)
    recorder.start()
```


## 八、配置汇总

```python
# config_hym.py
"""恨也迷人风格录制系统配置"""

# ==================== B站直播 ====================
BILI_ROOM_ID = 21452505          # 目标直播间号（示例）
BILI_PLATFORM = "h5"              # 返回m3u8格式
LIVE_CHECK_INTERVAL = 60          # 开播检测间隔（秒）

# ==================== 音频参数 ====================
SAMPLE_RATE = 16000
CHUNK_MS = 600
CHUNK_SAMPLES = int(SAMPLE_RATE * CHUNK_MS / 1000)  # 9600
RING_BUFFER_SECONDS = 90          # 扩展至90秒回溯

# ==================== ASR ====================
ASR_MODEL = "funasr/paraformer-zh-streaming"
ASR_DEVICE = "cuda"
CHUNK_SIZE = [0, 10, 5]
ENCODER_LOOK_BACK = 4
DECODER_LOOK_BACK = 1

# ==================== VAD ====================
ENABLE_VAD = True
VAD_MODEL = "funasr/fsmn-vad"
VAD_CHUNK_SIZE = 200

# ==================== 关键词 ====================
STOP_DELAY_SECONDS = 120          # 缩短至120秒（量大管饱风格）
COOLDOWN_SECONDS = 5              # 缩短冷却时间
T2_THRESHOLD = 0.75               # T2模糊匹配阈值
T3_COOCCUR_WINDOW = 30.0          # T3联合触发窗口（秒）

# ==================== 弹幕 ====================
ENABLE_DANMAKU = True
DANMAKU_DENSITY_THRESHOLD = 5     # 30秒内5条含关键词弹幕触发
DANMAKU_WINDOW_SECONDS = 30

# ==================== 热词 ====================
HYM_HOTWORDS = {
    "星瞳": 5, "小星星": 5,
    "瞳子": 4, "瞳宝": 4, "星瞳酱": 4,
    "腾讯大小姐": 3, "CDD": 3, "虚无": 3,
    "云之彼端": 3, "深月的风吟鸟唱": 3,
    "邻家女孩": 3, "瞳工": 3,
}

# ==================== 输出 ====================
OUTPUT_DIR = "./recordings"
VIDEO_BUFFER_DIR = "./video_buffer"
VIDEO_KEEP_MINUTES = 5            # 视频环形缓冲区保留时长
VIDEO_SEGMENT_SECONDS = 10        # TS分段长度
```


## 九、参数调优指南

| 参数 | 默认值 | 调优方向 | 说明 |
|------|--------|----------|------|
| `RING_BUFFER_SECONDS` | 90s | ↑ 需要更多上下文时增大 | 内存占用 = 采样率 × 2B × 秒数 |
| `STOP_DELAY_SECONDS` | 120s | ↓ 提高录制频率 | 越短越“量大管饱”，但碎片化 |
| `T2_THRESHOLD` | 0.75 | ↓ 提高召回率 | 越低越容易误触发 |
| `T3_COOCCUR_WINDOW` | 30s | ↑ 扩大联合触发范围 | T3词与T1/T2间隔多久仍可联合触发 |
| `DANMAKU_DENSITY_THRESHOLD` | 5 | ↓ 更敏感 | 30秒内几条弹幕触发 |
| `VIDEO_KEEP_MINUTES` | 5min | 根据磁盘容量调整 | 低码率TS约2MB/分钟 |


## 十、项目文件结构

```
hym-keyword-recorder/
├── config_hym.py           # 配置文件
├── main_hym.py             # 主入口
├── bili_stream.py          # B站直播流地址获取
├── bili_audio_source.py    # B站音频采集
├── ring_buffer.py          # 音频环形缓冲区（复用）
├── asr_engine.py           # FunASR封装（复用，需改热词表）
├── keyword_detector_v2.py  # 恨也迷人风格关键词检测
├── recorder_v2.py          # 视频录制控制器
├── video_ring_buffer.py    # 视频环形缓冲区
├── danmaku_listener.py     # B站弹幕监听
├── requirements.txt
├── recordings/             # 录制输出目录
└── video_buffer/           # 视频缓冲区目录
```

**requirements.txt**：

```
funasr>=1.3.0
torch
torchaudio
modelscope
av
numpy
soundfile
requests
websockets
```


## 十一、部署与使用

```bash
# 1. 安装依赖
pip install -r requirements.txt

# 2. 配置目标直播间
# 编辑 config_hym.py，修改 BILI_ROOM_ID

# 3. 启动系统
python main_hym.py 21452505

# 4. 输出说明
# recordings/   → 触发录制输出的视频素材文件
# video_buffer/ → 持续录制的TS分段（自动清理旧文件）
```

**注意事项**：本项目仅供学习交流使用，请在征得对方许可的情况下录制，请勿未经授权私自将内容用于商业用途，请勿用于大规模录制。

该方案的核心适配点在于：**层级化关键词策略**（T1精确/T2模糊/T3梗词联合/T4否定）匹配恨也迷人“量大管饱”的切片风格，**弹幕辅助触发**弥补ASR可能遗漏的素材，**视频环形缓冲区**替代纯音频回溯以支持后续FFmpeg剪辑流程。