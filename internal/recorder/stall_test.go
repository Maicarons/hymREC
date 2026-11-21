package recorder

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStallDuration(t *testing.T) {
	dir := t.TempDir()
	vb, err := NewVideoBuffer(VideoBufferConfig{Dir: dir, SegmentSeconds: 10})
	if err != nil {
		t.Fatalf("NewVideoBuffer 失败: %v", err)
	}

	// 未运行时不计卡死
	if got := vb.StallDuration(); got != 0 {
		t.Fatalf("未运行时应返回 0，实际 %v", got)
	}

	// 运行中且长时间没有分段 → 卡死
	vb.mu.Lock()
	vb.running = true
	vb.startedAt = time.Now().Add(-2 * time.Minute)
	vb.mu.Unlock()
	if got := vb.StallDuration(); got < 90*time.Second {
		t.Fatalf("无新分段时应累计到启动时刻，实际 %v", got)
	}

	// 出现新分段后卡死计时归零
	name := filepath.Join(dir, "seg_"+time.Now().Format("20060102_150405")+".ts")
	if err := os.WriteFile(name, []byte("fake"), 0o644); err != nil {
		t.Fatalf("写入分段失败: %v", err)
	}
	if got := vb.StallDuration(); got > 5*time.Second {
		t.Fatalf("有新分段时卡死计时应归零，实际 %v", got)
	}

	// 停止后不再判定
	vb.mu.Lock()
	vb.running = false
	vb.mu.Unlock()
	if got := vb.StallDuration(); got != 0 {
		t.Fatalf("停止后应返回 0，实际 %v", got)
	}
}
