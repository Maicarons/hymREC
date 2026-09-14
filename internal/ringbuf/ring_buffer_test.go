package ringbuf

import (
	"testing"
	"time"
)

func TestWriteAndRead(t *testing.T) {
	b := New(90, 16000, 9600) // 15 块容量
	base := time.Now()
	for i := 0; i < 10; i++ {
		b.Write(make([]int16, 9600), base.Add(time.Duration(i)*time.Second))
	}
	if got := b.Len(); got != 10 {
		t.Fatalf("Len = %d, 期望 10", got)
	}
	if got := b.Oldest(); !got.Equal(base) {
		t.Fatalf("Oldest = %v, 期望 %v", got, base)
	}
	if got := b.Newest(); !got.Equal(base.Add(9 * time.Second)) {
		t.Fatalf("Newest = %v, 期望 %v", got, base.Add(9*time.Second))
	}
	if d := b.Duration(); d < 9.0 || d > 9.1 {
		t.Fatalf("Duration = %v, 期望约 9", d)
	}
}

func TestRingOverwrite(t *testing.T) {
	b := New(90, 16000, 9600) // 容量 = 90*16000/9600 = 150 块
	base := time.Now()
	for i := 0; i < 200; i++ {
		b.Write(make([]int16, 9600), base.Add(time.Duration(i)*time.Second))
	}
	if got := b.Len(); got != 150 {
		t.Fatalf("写满后 Len = %d, 期望 150", got)
	}
	// 最旧应是最早仍保留的块（第 51 秒）
	if got := b.Oldest(); !got.Equal(base.Add(50 * time.Second)) {
		t.Fatalf("Oldest = %v, 期望 %v", got, base.Add(50*time.Second))
	}
	if got := b.Newest(); !got.Equal(base.Add(199 * time.Second)) {
		t.Fatalf("Newest = %v, 期望 %v", got, base.Add(199*time.Second))
	}
}

func TestReadFrom(t *testing.T) {
	b := New(90, 16000, 9600)
	base := time.Now()
	for i := 0; i < 10; i++ {
		b.Write(make([]int16, 9600), base.Add(time.Duration(i)*time.Second))
	}
	chunks := b.ReadFrom(base.Add(5 * time.Second))
	if len(chunks) != 5 {
		t.Fatalf("ReadFrom 返回 %d 块, 期望 5", len(chunks))
	}
	// 时间升序
	for i := 1; i < len(chunks); i++ {
		if !chunks[i].Timestamp.After(chunks[i-1].Timestamp) {
			t.Fatalf("ReadFrom 结果未按时间升序: %v", chunks)
		}
	}
}

func TestEmptyBuffer(t *testing.T) {
	b := New(90, 16000, 9600)
	if b.Len() != 0 {
		t.Fatal("空缓冲 Len 应为 0")
	}
	if !b.Oldest().IsZero() || !b.Newest().IsZero() {
		t.Fatal("空缓冲时间戳应为 zero")
	}
	if b.Duration() != 0 {
		t.Fatal("空缓冲 Duration 应为 0")
	}
	if chunks := b.ReadFrom(time.Now()); len(chunks) != 0 {
		t.Fatal("空缓冲 ReadFrom 应为空")
	}
}

func TestReset(t *testing.T) {
	b := New(90, 16000, 9600)
	base := time.Now()
	for i := 0; i < 5; i++ {
		b.Write(make([]int16, 9600), base.Add(time.Duration(i)*time.Second))
	}
	b.Reset()
	if b.Len() != 0 || !b.Oldest().IsZero() {
		t.Fatal("Reset 后应为空缓冲")
	}
}
