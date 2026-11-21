package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Maicarons/hymREC/internal/app"
	"github.com/Maicarons/hymREC/internal/config"
	"github.com/Maicarons/hymREC/internal/pipeline"
)

//go:embed ui/index.html ui/style.css ui/app.js
var uiFiles embed.FS

// Backend 监控面板依赖的服务端能力。抽成接口以便用假实现测试 HTTP 层。
type Backend interface {
	Snapshot() app.Snapshot
	Start() error
	Stop() error
	Restart(roomID int) error
	Trigger() error
	StopRecording() error
	Settings() app.SettingsView
	ApplySettings(kw config.KeywordConfig, dm config.DanmakuConfig) error
	Clips() []app.Clip
	ClipPath(name string) (string, bool)
	SubscribeEvents() ([]pipeline.Event, <-chan pipeline.Event, func())
}

// Server 监控面板 HTTP 服务：REST API + SSE 实时推送 + 内嵌前端。
type Server struct {
	backend Backend
	logs    *LogStore
	logger  *slog.Logger
	addr    string
	httpSrv *http.Server
	index   []byte
}

// New 创建服务（不启动）。
func New(addr string, backend Backend, logs *LogStore, logger *slog.Logger) (*Server, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if logs == nil {
		logs = NewLogStore(0)
	}
	index, err := uiFiles.ReadFile("ui/index.html")
	if err != nil {
		return nil, fmt.Errorf("读取内嵌前端失败: %w", err)
	}
	return &Server{backend: backend, logs: logs, logger: logger, addr: addr, index: index}, nil
}

// Run 启动 HTTP 服务，直到 ctx 取消。监听失败直接返回错误。
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("监控面板监听 %s 失败: %w", s.addr, err)
	}
	s.httpSrv = &http.Server{Handler: s.routes(), ReadHeaderTimeout: 5 * time.Second}
	s.logger.Info("监控面板已启动", "url", "http://"+displayAddr(s.addr), "listen", ln.Addr().String())

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-runCtx.Done()
		shutdownCtx, done := context.WithTimeout(context.Background(), 3*time.Second)
		defer done()
		_ = s.httpSrv.Shutdown(shutdownCtx)
	}()

	if err := s.httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// routes 注册全部路由并套上通用防护头与跨站校验。
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("GET /api/logs", s.handleLogs)
	mux.HandleFunc("GET /api/clips", s.handleClips)
	mux.HandleFunc("GET /api/clips/{name}", s.handleClipDownload)
	mux.HandleFunc("GET /api/settings", s.handleGetSettings)
	mux.HandleFunc("POST /api/settings", s.handleApplySettings)
	mux.HandleFunc("POST /api/control/start", s.handleStart)
	mux.HandleFunc("POST /api/control/stop", s.handleStop)
	mux.HandleFunc("POST /api/control/restart", s.handleRestart)
	mux.HandleFunc("POST /api/control/trigger", s.handleTrigger)
	mux.HandleFunc("POST /api/control/stop-recording", s.handleStopRecording)
	mux.Handle("GET /assets/", s.assetHandler())
	mux.HandleFunc("GET /", s.handleIndex)
	return s.guard(mux)
}

// assetHandler 提供内嵌的静态资源。
func (s *Server) assetHandler() http.Handler {
	sub, err := fs.Sub(uiFiles, "ui")
	if err != nil {
		panic("web: 内嵌资源结构异常: " + err.Error())
	}
	return http.StripPrefix("/assets/", http.FileServer(http.FS(sub)))
}

// guard 添加安全响应头，并拒绝跨站的状态变更请求。
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; "+
				"script-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			// 面板由本服务自己提供，跨站 POST 一律拒绝（防 CSRF）
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				if err != nil || !strings.EqualFold(u.Host, r.Host) {
					writeError(w, http.StatusForbidden, "跨站请求被拒绝")
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// handleIndex 返回单页前端。
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(s.index)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.backend.Snapshot())
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	limit := 200
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	logs := s.logs.Recent(limit)
	if logs == nil {
		logs = []LogEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"logs": logs})
}

func (s *Server) handleClips(w http.ResponseWriter, r *http.Request) {
	clips := s.backend.Clips()
	if clips == nil {
		clips = []app.Clip{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"clips": clips})
}

func (s *Server) handleClipDownload(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	path, ok := s.backend.ClipPath(name)
	if !ok {
		writeError(w, http.StatusNotFound, "切片不存在")
		return
	}
	w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(name))
	// 明确媒体类型：Windows 的 mime 映射会把 .ts 识别成 text/plain
	switch strings.ToLower(filepath.Ext(name)) {
	case ".ts":
		w.Header().Set("Content-Type", "video/mp2t")
	case ".mp4":
		w.Header().Set("Content-Type", "video/mp4")
	}
	http.ServeFile(w, r, path)
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.backend.Settings())
}

func (s *Server) handleApplySettings(w http.ResponseWriter, r *http.Request) {
	var patch app.SettingsPatch
	if err := decodeJSON(r, &patch); err != nil {
		writeError(w, http.StatusBadRequest, "请求体解析失败: "+err.Error())
		return
	}
	patch = app.SanitizePatch(patch)
	if err := s.backend.ApplySettings(patch.Keyword, patch.ToDanmakuConfig()); err != nil {
		writeError(w, statusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.backend.Settings())
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	if err := s.backend.Start(); err != nil {
		writeError(w, statusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	if err := s.backend.Stop(); err != nil {
		writeError(w, statusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RoomID int `json:"room_id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体解析失败: "+err.Error())
		return
	}
	if err := s.backend.Restart(req.RoomID); err != nil {
		writeError(w, statusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleTrigger(w http.ResponseWriter, r *http.Request) {
	if err := s.backend.Trigger(); err != nil {
		writeError(w, statusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleStopRecording(w http.ResponseWriter, r *http.Request) {
	if err := s.backend.StopRecording(); err != nil {
		writeError(w, statusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleEvents 通过 SSE 推送状态、事件与日志。
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "当前连接不支持流式推送")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	recent, eventCh, unsubscribe := s.backend.SubscribeEvents()
	defer unsubscribe()
	logCh, unsubscribeLogs := s.logs.Subscribe()
	defer unsubscribeLogs()

	send := func(name string, v any) bool {
		data, err := json.Marshal(v)
		if err != nil {
			return true
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, data); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	if !send("status", s.backend.Snapshot()) {
		return
	}
	for _, ev := range recent {
		if !send("event", ev) {
			return
		}
	}

	statusTicker := time.NewTicker(time.Second)
	defer statusTicker.Stop()
	pingTicker := time.NewTicker(20 * time.Second)
	defer pingTicker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-eventCh:
			if !send("event", ev) {
				return
			}
			// 事件通常伴随状态变化，立即补发一次快照
			if !send("status", s.backend.Snapshot()) {
				return
			}
		case entry := <-logCh:
			if !send("log", entry) {
				return
			}
		case <-statusTicker.C:
			if !send("status", s.backend.Snapshot()) {
				return
			}
		case <-pingTicker.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// writeJSON 输出 JSON 响应。
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError 输出统一错误结构。
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

// decodeJSON 解析请求体；空请求体视为零值。
func decodeJSON(r *http.Request, v any) error {
	if r.Body == nil {
		return nil
	}
	err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

// statusFor 把服务错误映射为 HTTP 状态码。
func statusFor(err error) int {
	switch {
	case errors.Is(err, app.ErrAlreadyRunning), errors.Is(err, app.ErrNotRunning), errors.Is(err, app.ErrRestartRequired):
		return http.StatusConflict
	case err == nil:
		return http.StatusOK
	default:
		return http.StatusBadRequest
	}
}

// displayAddr 把监听地址转换为浏览器可访问的地址。
func displayAddr(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "127.0.0.1" + addr
	}
	return addr
}
