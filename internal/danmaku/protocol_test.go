package danmaku

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"encoding/json"
	"testing"

	"github.com/andybalholm/brotli"
)

func TestPackAuth(t *testing.T) {
	pkt := PackAuth(21452505)
	if len(pkt) < headerLen {
		t.Fatal("认证包过短")
	}
	total := binary.BigEndian.Uint32(pkt[0:4])
	if int(total) != len(pkt) {
		t.Fatalf("包长度字段 %d 与实际 %d 不符", total, len(pkt))
	}
	if binary.BigEndian.Uint16(pkt[4:6]) != headerLen {
		t.Fatal("头长度字段错误")
	}
	if binary.BigEndian.Uint32(pkt[8:12]) != opAuth {
		t.Fatal("op 应为 7（认证）")
	}
	var body map[string]any
	if err := json.Unmarshal(pkt[headerLen:], &body); err != nil {
		t.Fatalf("认证 body 非 JSON: %v", err)
	}
	if body["roomid"] != float64(21452505) {
		t.Fatalf("认证 roomid 错误: %v", body["roomid"])
	}
	if body["protover"] != float64(3) {
		t.Fatalf("protover 应为 3（brotli），实际 %v", body["protover"])
	}
}

func TestPackHeartbeat(t *testing.T) {
	pkt := PackHeartbeat()
	if binary.BigEndian.Uint32(pkt[8:12]) != opHeartbeat {
		t.Fatal("op 应为 2（心跳）")
	}
	if len(pkt) != headerLen {
		t.Fatal("心跳包应只有头部")
	}
}

// wrapZlib 将多个子包打包并 zlib 压缩。
func wrapZlib(t *testing.T, sub ...[]byte) []byte {
	t.Helper()
	var inner bytes.Buffer
	for _, s := range sub {
		inner.Write(s)
	}
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	_, _ = zw.Write(inner.Bytes())
	_ = zw.Close()

	body := buf.Bytes()
	out := make([]byte, headerLen+len(body))
	binary.BigEndian.PutUint32(out[0:4], uint32(headerLen+len(body)))
	binary.BigEndian.PutUint16(out[4:6], headerLen)
	binary.BigEndian.PutUint16(out[6:8], versionZlib)
	binary.BigEndian.PutUint32(out[8:12], opMessage)
	binary.BigEndian.PutUint32(out[12:16], 1)
	copy(out[headerLen:], body)
	return out
}

func wrapBrotli(t *testing.T, sub ...[]byte) []byte {
	t.Helper()
	var inner bytes.Buffer
	for _, s := range sub {
		inner.Write(s)
	}
	var buf bytes.Buffer
	bw := brotli.NewWriter(&buf)
	_, _ = bw.Write(inner.Bytes())
	_ = bw.Close()

	body := buf.Bytes()
	out := make([]byte, headerLen+len(body))
	binary.BigEndian.PutUint32(out[0:4], uint32(headerLen+len(body)))
	binary.BigEndian.PutUint16(out[4:6], headerLen)
	binary.BigEndian.PutUint16(out[6:8], versionBrotli)
	binary.BigEndian.PutUint32(out[8:12], opMessage)
	binary.BigEndian.PutUint32(out[12:16], 1)
	copy(out[headerLen:], body)
	return out
}

// plainPacket 构造一个明文消息包。
func plainPacket(cmd, text string) []byte {
	body, _ := json.Marshal(map[string]any{
		"cmd":  cmd,
		"info": []any{[]any{}, text},
	})
	out := make([]byte, headerLen+len(body))
	binary.BigEndian.PutUint32(out[0:4], uint32(headerLen+len(body)))
	binary.BigEndian.PutUint16(out[4:6], headerLen)
	binary.BigEndian.PutUint16(out[6:8], versionPlain)
	binary.BigEndian.PutUint32(out[8:12], opMessage)
	binary.BigEndian.PutUint32(out[12:16], 1)
	copy(out[headerLen:], body)
	return out
}

func TestDecodePlain(t *testing.T) {
	pkt := plainPacket("DANMU_MSG", "星瞳真可爱")
	packets, err := Decode(pkt)
	if err != nil {
		t.Fatalf("Decode 失败: %v", err)
	}
	if len(packets) != 1 {
		t.Fatalf("应有 1 个包，实际 %d", len(packets))
	}
	msg, err := ParseMessage(packets[0])
	if err != nil {
		t.Fatalf("ParseMessage 失败: %v", err)
	}
	if msg.Cmd != "DANMU_MSG" {
		t.Fatalf("cmd = %s", msg.Cmd)
	}
	if got := msg.DanmakuText(); got != "星瞳真可爱" {
		t.Fatalf("弹幕文本 = %q, 期望 %q", got, "星瞳真可爱")
	}
}

func TestDecodeZlib(t *testing.T) {
	inner := plainPacket("DANMU_MSG", "zlib 压缩弹幕")
	pkt := wrapZlib(t, inner)
	packets, err := Decode(pkt)
	if err != nil {
		t.Fatalf("Decode zlib 失败: %v", err)
	}
	if len(packets) != 1 {
		t.Fatalf("应有 1 个包，实际 %d", len(packets))
	}
	msg, err := ParseMessage(packets[0])
	if err != nil {
		t.Fatalf("ParseMessage 失败: %v", err)
	}
	if got := msg.DanmakuText(); got != "zlib 压缩弹幕" {
		t.Fatalf("弹幕文本 = %q", got)
	}
}

func TestDecodeBrotli(t *testing.T) {
	inner := plainPacket("DANMU_MSG", "brotli 压缩弹幕")
	pkt := wrapBrotli(t, inner)
	packets, err := Decode(pkt)
	if err != nil {
		t.Fatalf("Decode brotli 失败: %v", err)
	}
	if len(packets) != 1 {
		t.Fatalf("应有 1 个包，实际 %d", len(packets))
	}
	msg, err := ParseMessage(packets[0])
	if err != nil {
		t.Fatalf("ParseMessage 失败: %v", err)
	}
	if got := msg.DanmakuText(); got != "brotli 压缩弹幕" {
		t.Fatalf("弹幕文本 = %q", got)
	}
}

func TestDecodeMultiplePackets(t *testing.T) {
	// 一个外层消息里塞两个明文包
	p1 := plainPacket("DANMU_MSG", "第一条")
	p2 := plainPacket("SEND_GIFT", "第二条")
	var data bytes.Buffer
	data.Write(p1)
	data.Write(p2)

	packets, err := Decode(data.Bytes())
	if err != nil {
		t.Fatalf("Decode 失败: %v", err)
	}
	if len(packets) != 2 {
		t.Fatalf("应有 2 个包，实际 %d", len(packets))
	}
}

func TestDecodeNestedCompressed(t *testing.T) {
	// 外层 zlib，内部再 brotli（极端但协议允许的嵌套）
	inner := wrapBrotli(t, plainPacket("DANMU_MSG", "嵌套压缩"))
	pkt := wrapZlib(t, inner)
	packets, err := Decode(pkt)
	if err != nil {
		t.Fatalf("Decode 嵌套失败: %v", err)
	}
	msg, err := ParseMessage(packets[0])
	if err != nil {
		t.Fatalf("ParseMessage 失败: %v", err)
	}
	if got := msg.DanmakuText(); got != "嵌套压缩" {
		t.Fatalf("弹幕文本 = %q", got)
	}
}

func TestDecodeHeartbeatReply(t *testing.T) {
	// 心跳回复：version=1，body 为 4 字节人气值
	body := make([]byte, 4)
	binary.BigEndian.PutUint32(body, 12345)
	out := make([]byte, headerLen+4)
	binary.BigEndian.PutUint32(out[0:4], uint32(headerLen+4))
	binary.BigEndian.PutUint16(out[4:6], headerLen)
	binary.BigEndian.PutUint16(out[6:8], versionHeart)
	binary.BigEndian.PutUint32(out[8:12], opHeartbeatReply)
	binary.BigEndian.PutUint32(out[12:16], 1)
	copy(out[headerLen:], body)

	packets, err := Decode(out)
	if err != nil {
		t.Fatalf("Decode 心跳回复失败: %v", err)
	}
	if packets[0].Operation != opHeartbeatReply {
		t.Fatalf("op = %d, 期望 3", packets[0].Operation)
	}
	if got := binary.BigEndian.Uint32(packets[0].Body); got != 12345 {
		t.Fatalf("人气值 = %d", got)
	}
}

func TestDecodeInvalid(t *testing.T) {
	// 头里声明 100 字节但实际只有 16 字节 -> 应报错
	out := make([]byte, headerLen)
	binary.BigEndian.PutUint32(out[0:4], 100)
	if _, err := Decode(out); err == nil {
		t.Fatal("非法长度应报错")
	}
	if _, err := Decode([]byte("too short")); err == nil {
		t.Fatal("过短数据应报错")
	}
}
