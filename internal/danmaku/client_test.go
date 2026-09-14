package danmaku

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// danmakuTestServer 模拟 B 站弹幕服务器。
func danmakuTestServer(t *testing.T, onDanmaku func(string)) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("升级 WebSocket 失败: %v", err)
			return
		}
		defer conn.Close()

		// 1. 等待并验证认证包
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Errorf("读取认证包失败: %v", err)
			return
		}
		packets, err := Decode(data)
		if err != nil || len(packets) == 0 {
			t.Errorf("解析认证包失败: %v", err)
			return
		}
		if packets[0].Operation != opAuth {
			t.Errorf("期望认证 op=7, 实际 %d", packets[0].Operation)
		}

		// 2. 回复认证成功
		authBody, _ := json.Marshal(map[string]any{"code": 0})
		if err := conn.WriteMessage(websocket.BinaryMessage, pack(opAuthReply, authBody)); err != nil {
			t.Errorf("发送认证回复失败: %v", err)
			return
		}

		// 3. 发送一条弹幕
		dm := plainPacket("DANMU_MSG", "星瞳今天状态很好")
		if err := conn.WriteMessage(websocket.BinaryMessage, dm); err != nil {
			t.Errorf("发送弹幕失败: %v", err)
			return
		}
		if onDanmaku != nil {
			onDanmaku("sent")
		}

		// 4. 持续读心跳并回复人气值
		hbBody := []byte{0, 0, 0, 88}

		deadline := time.After(3 * time.Second)
		for {
			conn.SetReadDeadline(time.Now().Add(3 * time.Second))
			_, _, err := conn.ReadMessage()
			if err != nil {
				return // 客户端断开或超时
			}
			_ = conn.WriteMessage(websocket.BinaryMessage, pack(opHeartbeatReply, hbBody))
			select {
			case <-deadline:
				return
			default:
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestClientAuthAndDanmaku(t *testing.T) {
	srv := danmakuTestServer(t, nil)
	msgCh := make(chan Message, 8)

	logger := slog.New(slog.NewTextHandler(&strings.Builder{}, nil))
	c := New(Options{
		URL:               "ws" + strings.TrimPrefix(srv.URL, "http"),
		RoomID:            21452505,
		HeartbeatInterval: 300 * time.Millisecond,
		Logger:            logger,
	}, func(m Message) { msgCh <- m })
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = c.Run(ctx) }()

	select {
	case msg := <-msgCh:
		if msg.Cmd != "DANMU_MSG" {
			t.Fatalf("期望 DANMU_MSG, 实际 %s", msg.Cmd)
		}
		if got := msg.DanmakuText(); got != "星瞳今天状态很好" {
			t.Fatalf("弹幕文本 = %q", got)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("等待弹幕超时")
	}
}
