package pipeline

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Maicarons/hymREC/internal/bili"
	"github.com/Maicarons/hymREC/internal/config"
)

func TestRefreshStreamRateLimited(t *testing.T) {
	var calls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/room/v1/Room/room_init", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":0,"data":{"room_id":123,"live_status":1}}`)
	})
	mux.HandleFunc("/room/v1/Room/playUrl", func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		fmt.Fprintf(w, `{"code":0,"data":{"durl":[{"url":%q,"format":"m3u8"}]}}`,
			fmt.Sprintf("http://example.com/%d.m3u8", n))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	bc := bili.NewClient(nil)
	bc.SetBaseURL(srv.URL)
	p := &Pipeline{
		cfg:    config.Default(),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		bili:   bc,
	}

	ctx := context.Background()
	if err := p.refreshStream(ctx); err != nil {
		t.Fatalf("首次刷新流地址失败: %v", err)
	}
	if got := p.currentStreamURL(); got != "http://example.com/1.m3u8" {
		t.Fatalf("流地址 = %q，期望首次获取的地址", got)
	}

	// 刷新间隔内的再次刷新应直接复用当前地址，避免高频请求 API
	if err := p.refreshStream(ctx); err != nil {
		t.Fatalf("受限刷新不应报错: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("刷新间隔内不应再次请求 API，实际请求 %d 次", got)
	}
	if got := p.currentStreamURL(); got != "http://example.com/1.m3u8" {
		t.Fatalf("流地址 = %q，应保持不变", got)
	}

	// 超过刷新间隔后允许重新获取
	p.mu.Lock()
	p.lastRefresh = time.Now().Add(-streamRefreshMinInterval - time.Second)
	p.mu.Unlock()
	if err := p.refreshStream(ctx); err != nil {
		t.Fatalf("超时后刷新失败: %v", err)
	}
	if got := p.currentStreamURL(); got != "http://example.com/2.m3u8" {
		t.Fatalf("流地址 = %q，期望刷新后的新地址", got)
	}
}

func TestRefreshStreamPropagatesAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":-412,"message":"请求被拦截"}`)
	}))
	defer srv.Close()

	bc := bili.NewClient(nil)
	bc.SetBaseURL(srv.URL)
	p := &Pipeline{
		cfg:    config.Default(),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		bili:   bc,
	}
	if err := p.refreshStream(context.Background()); err == nil {
		t.Fatal("API 返回错误码时应返回错误，交由调用方重试")
	}
}
