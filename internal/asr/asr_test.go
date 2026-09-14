package asr

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/henyemiren/hymREC/internal/config"
)

func TestNoneEngine(t *testing.T) {
	e, err := New(NewConfig("none", "", nil, "", nil))
	if err != nil {
		t.Fatalf("New(none) 失败: %v", err)
	}
	text, err := e.ProcessChunk(make([]int16, 9600))
	if err != nil || text != "" {
		t.Fatalf("none 引擎应返回空文本: %q, %v", text, err)
	}
}

// NewConfig 便捷构造 ASR 配置。
func NewConfig(typ, endpoint string, chunkSize []int, wavName string, hotwords map[string]int) config.ASRConfig {
	return config.ASRConfig{
		Type:      typ,
		Endpoint:  endpoint,
		ChunkSize: chunkSize,
		WavName:   wavName,
		Hotwords:  hotwords,
	}
}

// funasrTestServer 模拟 FunASR WebSocket 服务端。
func funasrTestServer(t *testing.T, wantText string) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("升级失败: %v", err)
			return
		}
		defer conn.Close()

		// 读 header（文本帧，含 hotwords）
		msgType, header, err := conn.ReadMessage()
		if err != nil {
			t.Errorf("读 header 失败: %v", err)
			return
		}
		if msgType != websocket.TextMessage {
			t.Errorf("header 应为文本帧, 实际 %d", msgType)
			return
		}
		var hdr map[string]any
		if err := json.Unmarshal(header, &hdr); err != nil {
			t.Errorf("header 非 JSON: %v", err)
			return
		}
		if hdr["mode"] != "2pass" {
			t.Errorf("mode = %v", hdr["mode"])
		}
		if _, ok := hdr["hotwords"]; !ok {
			t.Errorf("header 应包含 hotwords")
		}

		// 读音频二进制帧
		msgType, _, err = conn.ReadMessage()
		if err != nil || msgType != websocket.BinaryMessage {
			t.Errorf("读音频失败: type=%d err=%v", msgType, err)
			return
		}

		// 返回在线增量结果
		resp, _ := json.Marshal(map[string]any{
			"mode": "2pass-online", "text": wantText, "wav_name": "hymREC_live",
		})
		if err := conn.WriteMessage(websocket.TextMessage, resp); err != nil {
			t.Errorf("写结果失败: %v", err)
			return
		}

		// 读结束帧
		_, endHeader, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var end map[string]any
		_ = json.Unmarshal(endHeader, &end)
		if isSpeaking, _ := end["is_speaking"].(bool); isSpeaking {
			t.Errorf("结束帧 is_speaking 应为 false")
		}
		// 返回 offline 最终结果
		final, _ := json.Marshal(map[string]any{
			"mode": "2pass-offline", "text": "，", "wav_name": "hymREC_live", "is_final": true,
		})
		_ = conn.WriteMessage(websocket.TextMessage, final)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFunASRWSProcessAndFlush(t *testing.T) {
	srv := funasrTestServer(t, "星瞳")
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	e, err := NewFunASRWS(FunASROptions{
		Endpoint:  wsURL,
		Mode:      "2pass",
		ChunkSize: []int{0, 10, 5},
		WavName:   "hymREC_live",
		Hotwords:  map[string]int{"星瞳": 5},
	})
	if err != nil {
		t.Fatalf("NewFunASRWS 失败: %v", err)
	}
	defer e.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		text, err := e.ProcessChunk(make([]int16, 9600))
		if err != nil {
			t.Errorf("ProcessChunk 失败: %v", err)
			return
		}
		if text != "星瞳" {
			t.Errorf("ProcessChunk 文本 = %q, 期望 %q", text, "星瞳")
		}
		final, err := e.Flush()
		if err != nil {
			t.Errorf("Flush 失败: %v", err)
			return
		}
		if !strings.Contains(final, "，") {
			t.Errorf("Flush 应包含 offline 结果, 实际 %q", final)
		}
	}()

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("FunASR 交互超时")
	}
}
