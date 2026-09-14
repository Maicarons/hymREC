# ASR 接入

hymrec 通过 `asr.Engine` 接口解耦语音识别实现：

```go
type Engine interface {
    ProcessChunk(pcm []int16) (string, error) // 增量识别文本
    Flush() (string, error)                    // 结束本轮识别
    Close() error
}
```

## 对接 FunASR 流式服务

### 1. 部署 FunASR

**方式 A：pip 本地部署**（Windows/Linux 通用，CPU 可跑，已实测）

```bash
python -m venv funasr-env
funasr-env/Scripts/pip install torch torchaudio --index-url https://download.pytorch.org/whl/cpu
funasr-env/Scripts/pip install funasr modelscope

# 下载模型（2pass 需要：seaco 离线 + streaming 在线 + VAD + 标点）
funasr-env/Scripts/python -c "from modelscope.hub.snapshot_download import snapshot_download; \
  [snapshot_download(m) for m in ['iic/speech_seaco_paraformer_large_asr_nat-zh-cn-16k-common-vocab8404-pytorch', \
  'iic/speech_paraformer-large_asr_nat-zh-cn-16k-common-vocab8404-online', \
  'iic/speech_fsmn_vad_zh-cn-16k-common-pytorch', \
  'iic/punc_ct-transformer_zh-cn-common-vocab272727-pytorch']]"

# 下载官方 wss 服务端并适配 funasr 1.4（模型别名、禁用声纹、hotwords 字段）
# https://github.com/modelscope/FunASR/blob/main/runtime/python/websocket/funasr_wss_server.py
# 模型名改用 alias：paraformer-zh / paraformer-zh-streaming / fsmn-vad / ct-punc-c

funasr-env/Scripts/python funasr_wss_server.py --host 127.0.0.1 --port 10095 \
  --device cpu --ngpu 0 --certfile ""
```

**方式 B：官方 Docker**

```bash
# 以官方镜像为准
docker run -it --name funasr -p 10095:10095 \
  registry.cn-hangzhou.aliyuncs.com/modelscope-repo/modelscope:ubuntu20.04 \
  bash -c "cd /workspace/FunASR/runtime && \
  python funasr_wss_server.py --port 10095 --model-dir pretrained_models/speech_paraformer-large_asr_nat-zh-cn-16k-common-vocab8404-pytorch"
```

> 注意：funasr 1.4 的 `AutoModel` 仅识别注册表内的模型别名（`paraformer-zh` = seaco 热词增强版、`paraformer-zh-streaming` 等），需按方式 A 中的说明适配官方脚本。

### 2. 配置

```yaml
asr:
  type: funasr-ws
  endpoint: ws://127.0.0.1:10095
  mode: 2pass
  chunk_size: [0, 10, 5]
  wav_name: hymrec_live
  hotwords:
    星瞳: 5
    瞳子: 4
```

### 3. 协议说明

客户端实现遵循 FunASR 官方 `funasr_wss_client.py` 的协议：

1. **文本帧**发送 JSON header：
   ```json
   {"mode":"2pass","chunk_size":[0,10,5],"wav_name":"hymrec_live","is_speaking":true,"itn":true,"hotwords":"{\"星瞳\":5}"}
   ```
2. **二进制帧**发送 PCM s16le 音频字节（16kHz 单声道）；
3. 服务端文本帧返回 `{"mode":"2pass-online","text":"..."}`（增量）或 `{"mode":"2pass-offline","text":"...","is_final":true}`（最终）；
4. 结束时发送 `{"is_speaking":false}`，等待 offline 最终结果。

### 4. 热词

`hotwords` 会以 JSON 字符串形式注入 header，权重越高识别越倾向输出该词。建议：

- T1 核心词：权重 5
- T2 关联词：权重 4
- T3 梗词：权重 3

