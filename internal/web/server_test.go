package web

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Maicarons/hymREC/internal/app"
	"github.com/Maicarons/hymREC/internal/config"
	"github.com/Maicarons/hymREC/internal/pipeline"
)

// fakeBackend 用于测试 HTTP 层，避免依赖真实流水线。
type fakeBackend struct {
	mu sync.Mutex

	snapshot app.Snapshot
	clips    []app.Clip
	settings app.SettingsView

	startErr   error
	stopErr    error
	triggerErr error

	started    int
	stopped    int
	triggered  int
	stoppedRec int
	restartAt  int

	applied    []config.KeywordConfig
	clipDir    string
	events     chan pipeline.Event
	recent     []pipeline.Event
	eventClose func()
}

func newFakeBackend() *fakeBackend {
	ch := make(chan pipeline.Event, 8)
	return &fakeBackend{
		events: ch,
		snapshot: app.Snapshot{
			Service: app.ServiceStatus{Running: true, State: app.StateLive, StateText: "直播中", Version: "test"},
			Room:    app.RoomStatus{ID: 123, LiveStatus: 1, LiveText: "直播中"},
			Clips:   app.ClipsStatus{Count: 1, TotalBytes: 1024, Last: "clip.ts"},
		},
		settings: app.SettingsView{
			RoomID:  123,
			Keyword: config.KeywordConfig{StopDelaySeconds: 120, CooldownSeconds: 5, T2Threshold: 0.75, T3CooccurWindow: 30, T1: []string{"星瞳"}},
			Danmaku: app.DanmakuSettings{Enabled: true, Keywords: []string{"星瞳"}, DensityThreshold: 5, WindowSeconds: 30, ConfirmDelaySeconds: 2},
		},
		clips: []app.Clip{{
			Name: "hym_clip_asr_20260101_120000.ts", Size: 1024, Source: "asr",
			TriggerAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.Local),
			URL:       "/api/clips/hym_clip_asr_20260101_120000.ts",
		}},
	}
}

func (f *fakeBackend) Snapshot() app.Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snapshot
}

func (f *fakeBackend) Start() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return f.startErr
	}
	f.started++
	return nil
}

func (f *fakeBackend) Stop() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stopErr != nil {
		return f.stopErr
	}
	f.stopped++
	return nil
}

func (f *fakeBackend) Restart(roomID int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restartAt = roomID
	return nil
}

func (f *fakeBackend) Trigger() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.triggerErr != nil {
		return f.triggerErr
	}
	f.triggered++
	return nil
}

func (f *fakeBackend) StopRecording() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stoppedRec++
	return nil
}

func (f *fakeBackend) Settings() app.SettingsView {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.settings
}

func (f *fakeBackend) ApplySettings(kw config.KeywordConfig, dm config.DanmakuConfig) error {
	if dm.Enabled != f.settings.Danmaku.Enabled {
		return app.ErrRestartRequired
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applied = append(f.applied, kw)
	f.settings.Keyword = kw
	return nil
}

func (f *fakeBackend) Clips() []app.Clip {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]app.Clip(nil), f.clips...)
}

func (f *fakeBackend) ClipPath(name string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if name != filepath.Base(name) {
		return "", false
	}
	for _, c := range f.clips {
		if c.Name == name {
			return filepath.Join(f.clipDir, name), true
		}
	}
	return "", false
}

func (f *fakeBackend) SubscribeEvents() ([]pipeline.Event, <-chan pipeline.Event, func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]pipeline.Event(nil), f.recent...), f.events, func() {}
}

// newTestServer 创建指向假后端的测试服务。
func newTestServer(t *testing.T) (*fakeBackend, *httptest.Server, *LogStore) {
	t.Helper()
	backend := newFakeBackend()
	store := NewLogStore(50)
	store.add(LogEntry{Time: time.Now(), Level: "INFO", Message: "启动完成", Attrs: map[string]string{"room_id": "123"}})
	srv, err := New("127.0.0.1:0", backend, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("创建服务失败: %v", err)
	}
	ts := httptest.NewServer(srv.routes())
	t.Cleanup(ts.Close)
	return backend, ts, store
}

func getJSON(t *testing.T, url string, out any) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s 失败: %v", url, err)
	}
	defer resp.Body.Close()
	if out != nil && resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("解析响应失败: %v", err)
		}
	}
	return resp.StatusCode
}

func post(t *testing.T, url, body string, headers map[string]string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s 失败: %v", url, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestIndexServed(t *testing.T) {
	_, ts, _ := newTestServer(t)
	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET / 失败: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q", ct)
	}
	if !strings.Contains(string(body), "hymREC") {
		t.Fatal("首页未包含页面内容")
	}
	if got := resp.Header.Get("Content-Security-Policy"); got == "" {
		t.Fatal("缺少 CSP 响应头")
	}
}

func TestStatusEndpoint(t *testing.T) {
	_, ts, _ := newTestServer(t)
	var snap app.Snapshot
	if code := getJSON(t, ts.URL+"/api/status", &snap); code != http.StatusOK {
		t.Fatalf("状态码 = %d", code)
	}
	if snap.Service.State != app.StateLive || snap.Room.ID != 123 {
		t.Fatalf("快照异常: %+v", snap)
	}
}

func TestUnknownAPIPath(t *testing.T) {
	_, ts, _ := newTestServer(t)
	resp, err := http.Get(ts.URL + "/api/unknown")
	if err != nil {
		t.Fatalf("GET 失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("未知 API 应返回 404，实际 %d（不能回落到前端首页）", resp.StatusCode)
	}
}

func TestControlEndpoints(t *testing.T) {
	backend, ts, _ := newTestServer(t)

	if code, _ := post(t, ts.URL+"/api/control/start", "", nil); code != http.StatusOK {
		t.Fatalf("start 状态码 = %d", code)
	}
	if code, _ := post(t, ts.URL+"/api/control/trigger", "", nil); code != http.StatusOK {
		t.Fatalf("trigger 状态码 = %d", code)
	}
	if code, _ := post(t, ts.URL+"/api/control/stop-recording", "", nil); code != http.StatusOK {
		t.Fatalf("stop-recording 状态码 = %d", code)
	}
	if code, _ := post(t, ts.URL+"/api/control/restart", `{"room_id":456}`, nil); code != http.StatusOK {
		t.Fatalf("restart 状态码 = %d", code)
	}

	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.started != 1 || backend.triggered != 1 || backend.stoppedRec != 1 || backend.restartAt != 456 {
		t.Fatalf("控制调用未按预期转发: %+v", backend)
	}
}

func TestControlErrorMapping(t *testing.T) {
	backend, ts, _ := newTestServer(t)
	backend.startErr = app.ErrAlreadyRunning

	code, body := post(t, ts.URL+"/api/control/start", "", nil)
	if code != http.StatusConflict {
		t.Fatalf("重复启动应返回 409，实际 %d", code)
	}
	if msg, _ := body["error"].(string); msg == "" {
		t.Fatal("错误响应应带可读信息")
	}

	backend.triggerErr = errors.New("需要直播进行中")
	if code, _ := post(t, ts.URL+"/api/control/trigger", "", nil); code != http.StatusBadRequest {
		t.Fatalf("业务错误应返回 400，实际 %d", code)
	}
}

func TestCrossSitePostRejected(t *testing.T) {
	backend, ts, _ := newTestServer(t)
	code, _ := post(t, ts.URL+"/api/control/start", "", map[string]string{"Origin": "http://evil.example"})
	if code != http.StatusForbidden {
		t.Fatalf("跨站 POST 应返回 403，实际 %d", code)
	}
	backend.mu.Lock()
	started := backend.started
	backend.mu.Unlock()
	if started != 0 {
		t.Fatal("跨站请求不应触达服务")
	}

	// 同源请求放行
	if code, _ := post(t, ts.URL+"/api/control/start", "", map[string]string{"Origin": ts.URL}); code != http.StatusOK {
		t.Fatalf("同源 POST 应放行，实际 %d", code)
	}
}

func TestSettingsEndpoints(t *testing.T) {
	backend, ts, _ := newTestServer(t)

	var view app.SettingsView
	if code := getJSON(t, ts.URL+"/api/settings", &view); code != http.StatusOK {
		t.Fatalf("状态码 = %d", code)
	}
	if view.RoomID != 123 || len(view.Keyword.T1) != 1 {
		t.Fatalf("设置快照异常: %+v", view)
	}

	patch := `{"keyword":{"t1":[" 星瞳 ","","星瞳","新词"],"t2":[],"t3":[],"t4":[],
		"stop_delay_seconds":90,"cooldown_seconds":5,"t2_threshold":0.8,"t3_cooccur_window":20},
		"danmaku":{"enabled":true,"keywords":["星瞳 "],"density_threshold":6,"window_seconds":30,"confirm_delay_seconds":2}}`
	code, body := post(t, ts.URL+"/api/settings", patch, nil)
	if code != http.StatusOK {
		t.Fatalf("保存设置状态码 = %d（%v）", code, body)
	}

	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.applied) != 1 {
		t.Fatalf("应转发一次设置，实际 %d 次", len(backend.applied))
	}
	kw := backend.applied[0]
	if len(kw.T1) != 2 || kw.T1[0] != "星瞳" || kw.T1[1] != "新词" {
		t.Fatalf("关键词未去重或去空白: %v", kw.T1)
	}
	if kw.StopDelaySeconds != 90 || kw.T2Threshold != 0.8 {
		t.Fatalf("阈值未生效: %+v", kw)
	}
}

func TestSettingsRestartRequired(t *testing.T) {
	_, ts, _ := newTestServer(t)
	patch := `{"keyword":{"t1":["a"],"t2":[],"t3":[],"t4":[],"stop_delay_seconds":120,
		"cooldown_seconds":5,"t2_threshold":0.75,"t3_cooccur_window":30},
		"danmaku":{"enabled":false,"keywords":["a"],"density_threshold":5,"window_seconds":30,"confirm_delay_seconds":2}}`
	code, body := post(t, ts.URL+"/api/settings", patch, nil)
	if code != http.StatusConflict {
		t.Fatalf("需要重启的设置应返回 409，实际 %d", code)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "重启") {
		t.Fatalf("错误信息应提示重启，实际: %v", msg)
	}
}

func TestSettingsBadJSON(t *testing.T) {
	_, ts, _ := newTestServer(t)
	if code, _ := post(t, ts.URL+"/api/settings", "{not json", nil); code != http.StatusBadRequest {
		t.Fatalf("非法 JSON 应返回 400，实际 %d", code)
	}
}

func TestClipsListAndDownload(t *testing.T) {
	backend, ts, _ := newTestServer(t)

	var list struct {
		Clips []app.Clip `json:"clips"`
	}
	if code := getJSON(t, ts.URL+"/api/clips", &list); code != http.StatusOK {
		t.Fatalf("状态码 = %d", code)
	}
	if len(list.Clips) != 1 || list.Clips[0].Source != "asr" {
		t.Fatalf("切片列表异常: %+v", list.Clips)
	}

	// 真实文件才能下载：把假后端的路径指到临时文件
	dir := t.TempDir()
	name := "hym_clip_asr_20260101_120000.ts"
	file := filepath.Join(dir, name)
	if err := os.WriteFile(file, []byte("fake-ts"), 0o644); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}
	backend.mu.Lock()
	backend.clips[0].Name = name
	backend.clipDir = dir
	backend.mu.Unlock()

	resp, err := http.Get(ts.URL + "/api/clips/" + name)
	if err != nil {
		t.Fatalf("下载失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("下载状态码 = %d", resp.StatusCode)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, "attachment") {
		t.Fatalf("下载应带 attachment，实际 %q", cd)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "video/mp2t" {
		t.Fatalf("TS 切片的 Content-Type = %q，期望 video/mp2t", ct)
	}
}

func TestClipDownloadRejectsUnknownAndTraversal(t *testing.T) {
	_, ts, _ := newTestServer(t)
	for _, path := range []string{
		"/api/clips/..%2F..%2Fconfig.yaml",
		"/api/clips/secret.ts",
		"/api/clips/hym_clip_asr_20260101_120000.ts", // 假后端有记录但文件不存在
	} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s 失败: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s 应返回 404，实际 %d", path, resp.StatusCode)
		}
	}
}

func TestLogsEndpoint(t *testing.T) {
	_, ts, store := newTestServer(t)
	store.add(LogEntry{Time: time.Now(), Level: "WARN", Message: "测试警告"})

	var out struct {
		Logs []LogEntry `json:"logs"`
	}
	if code := getJSON(t, ts.URL+"/api/logs?limit=10", &out); code != http.StatusOK {
		t.Fatalf("状态码 = %d", code)
	}
	if len(out.Logs) != 2 {
		t.Fatalf("日志条数 = %d，期望 2", len(out.Logs))
	}
	if out.Logs[len(out.Logs)-1].Message != "测试警告" {
		t.Fatalf("最新日志不对: %+v", out.Logs)
	}
}

func TestSSEStreamsStatusAndEvents(t *testing.T) {
	backend, ts, _ := newTestServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/events", nil)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("连接 SSE 失败: %v", err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q", ct)
	}

	// 首帧为完整状态
	reader := bufio.NewReader(resp.Body)
	event, data := readSSE(t, reader)
	if event != "status" {
		t.Fatalf("首帧事件 = %q，期望 status", event)
	}
	var snap app.Snapshot
	if err := json.Unmarshal([]byte(data), &snap); err != nil {
		t.Fatalf("状态帧解析失败: %v", err)
	}
	if snap.Service.State != app.StateLive {
		t.Fatalf("状态帧内容异常: %+v", snap)
	}

	// 流水线事件应实时推送
	backend.events <- pipeline.Event{Type: pipeline.EventTrigger, Message: "关键词命中", Source: "asr"}
	event, data = readSSE(t, reader)
	for event != "event" {
		event, data = readSSE(t, reader)
	}
	var ev pipeline.Event
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		t.Fatalf("事件帧解析失败: %v", err)
	}
	if ev.Type != pipeline.EventTrigger || ev.Source != "asr" {
		t.Fatalf("事件内容异常: %+v", ev)
	}
}

// readSSE 读取一帧 SSE，返回事件名与数据。
func readSSE(t *testing.T, reader *bufio.Reader) (string, string) {
	t.Helper()
	var event, data string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("读取 SSE 失败: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data = strings.TrimPrefix(line, "data: ")
		case line == "" && data != "":
			return event, data
		}
	}
}

func TestLogStoreRingAndSubscribe(t *testing.T) {
	store := NewLogStore(3)
	for i := 0; i < 5; i++ {
		store.add(LogEntry{Level: "INFO", Message: "m"})
	}
	recent := store.Recent(10)
	if len(recent) != 3 {
		t.Fatalf("环形缓冲保留 %d 条，期望 3", len(recent))
	}
	if recent[2].Seq != 5 {
		t.Fatalf("最新序号 = %d，期望 5", recent[2].Seq)
	}

	ch, unsubscribe := store.Subscribe()
	defer unsubscribe()
	store.add(LogEntry{Level: "WARN", Message: "实时"})
	select {
	case entry := <-ch:
		if entry.Message != "实时" {
			t.Fatalf("订阅内容异常: %+v", entry)
		}
	case <-time.After(time.Second):
		t.Fatal("订阅者未收到日志")
	}
}
