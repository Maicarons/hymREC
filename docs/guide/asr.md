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

使用官方 [FunASR runtime](https://github.com/modelscope/FunASR) 部署流式服务（Paraformer-zh-streaming）：

```bash
# 示例（以官方镜像为准）
docker run -it --name funasr -p 10095:10095 \
  registry.cn-hangzhou.aliyuncs.com/modelscope-repo/modelscope:ubuntu20.04 \
  bash -c "cd /workspace/FunASR/runtime && \
  python funasr_wss_server.py --port 10095 --model-dir pretrained_models/speech_paraformer-large_asr_nat-zh-cn-16k-common-vocab8404-pytorch"
```

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

