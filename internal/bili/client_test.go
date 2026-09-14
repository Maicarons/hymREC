package bili

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// mockAPI 搭建模拟 B 站 API 服务器。
type mockAPI struct {
	t            *testing.T
	server       *httptest.Server
	live         bool
	streamURL    string
	streamFormat string
}

func newMockAPI(t *testing.T) *mockAPI {
	m := &mockAPI{t: t, live: true, streamURL: "http://example.com/live.flv", streamFormat: "flv"}
	mux := http.NewServeMux()

	mux.HandleFunc("/room/v1/Room/room_init", func(w http.ResponseWriter, r *http.Request) {
		status := 0
		if m.live {
			status = 1
		}
		m.write(w, map[string]any{
			"code": 0, "message": "ok",
			"data": map[string]any{
				"room_id":     21452505,
				"short_id":    m.queryInt(r, "id"),
				"live_status": status,
			},
		})
	})
	mux.HandleFunc("/room/v1/Room/getRoomInfoOld", func(w http.ResponseWriter, r *http.Request) {
		status := 0
		if m.live {
			status = 1
		}
		m.write(w, map[string]any{
			"code": 0, "message": "ok",
			"data": map[string]any{
				"roomid":     21452505,
				"liveStatus": status,
				"title":      "测试直播间",
			},
		})
	})
	mux.HandleFunc("/room/v1/Room/playUrl", func(w http.ResponseWriter, r *http.Request) {
		// B 站未开播时返回 code=0 但 durl 为空
		durl := []map[string]any{}
		if m.live {
			durl = []map[string]any{{
				"url":    m.streamURL,
				"format": m.streamFormat,
				"qn":     10000,
				"codec":  0,
			}}
		}
		m.write(w, map[string]any{
			"code": 0, "message": "ok",
			"data": map[string]any{"durl": durl},
		})
	})

	m.server = httptest.NewServer(mux)
	t.Cleanup(m.server.Close)
	return m
}

func (m *mockAPI) write(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		m.t.Errorf("mock 响应编码失败: %v", err)
	}
}

func (m *mockAPI) queryInt(r *http.Request, key string) int {
	v := r.URL.Query().Get(key)
	var n int
	_, _ = fmt.Sscanf(v, "%d", &n)
	return n
}

// client 创建指向 mock 服务器的客户端（替换 API 域名）。
func (m *mockAPI) client() *Client {
	c := NewClient(nil)
	c.baseURL = m.server.URL
	return c
}

func TestGetRealRoomID(t *testing.T) {
	m := newMockAPI(t)
	c := m.client()
	id, err := c.GetRealRoomID(12345)
	if err != nil {
		t.Fatalf("GetRealRoomID 失败: %v", err)
	}
	if id != 21452505 {
		t.Fatalf("真实房间号 = %d, 期望 21452505", id)
	}
}

func TestGetRoomStatusLive(t *testing.T) {
	m := newMockAPI(t)
	c := m.client()
	st, err := c.GetRoomStatus(21452505)
	if err != nil {
		t.Fatalf("GetRoomStatus 失败: %v", err)
	}
	if st.LiveStatus != 1 || st.Title != "测试直播间" {
		t.Fatalf("开播状态异常: %+v", st)
	}
	if live, _ := c.IsLive(21452505); !live {
		t.Fatal("IsLive 应为 true")
	}
}

func TestGetRoomStatusOffline(t *testing.T) {
	m := newMockAPI(t)
	m.live = false
	c := m.client()
	live, err := c.IsLive(21452505)
	if err != nil {
		t.Fatalf("IsLive 失败: %v", err)
	}
	if live {
		t.Fatal("IsLive 应为 false")
	}
}

func TestGetStreamURL(t *testing.T) {
	m := newMockAPI(t)
	c := m.client()
	u, err := c.GetStreamURL(21452505, 10000, "web")
	if err != nil {
		t.Fatalf("GetStreamURL 失败: %v", err)
	}
	if u.URL != m.streamURL || u.Format != "flv" {
		t.Fatalf("流地址异常: %+v", u)
	}
}

func TestGetStreamURLNotLive(t *testing.T) {
	m := newMockAPI(t)
	m.live = false
	c := m.client()
	_, err := c.GetStreamURL(21452505, 10000, "web")
	if !errors.Is(err, ErrNotLive) {
		t.Fatalf("未开播应返回 ErrNotLive, 实际: %v", err)
	}
}
