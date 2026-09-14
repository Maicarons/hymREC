// Package danmaku 实现 B 站直播弹幕 WebSocket 协议：
// 连接、认证、心跳、消息解压（zlib/brotli）与弹幕提取。
package danmaku

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"

	"github.com/andybalholm/brotli"
)

// 协议常量（参考 B 站直播弹幕协议文档）。
const (
	headerLen     = 16
	versionPlain  = 0 // 纯 JSON
	versionHeart  = 1 // 心跳回复（4 字节 int32）
	versionZlib   = 2 // zlib 压缩
	versionBrotli = 3 // brotli 压缩

	opHeartbeat      = 2 // 客户端 -> 服务器：心跳
	opHeartbeatReply = 3 // 服务器 -> 客户端：心跳回复（人气值）
	opMessage        = 5 // 服务器 -> 客户端：普通消息
	opAuth           = 7 // 客户端 -> 服务器：认证
	opAuthReply      = 8 // 服务器 -> 客户端：认证回复
)

// Packet 一个协议包。
type Packet struct {
	Operation uint32
	Version   uint16
	Body      []byte
}

// pack 组装一个协议包（version 固定为 0，JSON 明文）。
func pack(op uint32, body []byte) []byte {
	total := headerLen + len(body)
	buf := make([]byte, total)
	binary.BigEndian.PutUint32(buf[0:4], uint32(total))
	binary.BigEndian.PutUint16(buf[4:6], headerLen)
	binary.BigEndian.PutUint16(buf[6:8], versionPlain)
	binary.BigEndian.PutUint32(buf[8:12], op)
	binary.BigEndian.PutUint32(buf[12:16], 1) // sequence
	copy(buf[headerLen:], body)
	return buf
}

// PackAuth 构造认证包。
func PackAuth(roomID int) []byte {
	body, _ := json.Marshal(map[string]any{
		"uid":      0,
		"roomid":   roomID,
		"protover": 3, // 请求 brotli 压缩（服务端不满足时退回 zlib）
		"platform": "web",
		"type":     2,
	})
	return pack(opAuth, body)
}

// PackHeartbeat 构造心跳包（空 body）。
func PackHeartbeat() []byte {
	return pack(opHeartbeat, nil)
}

// unpack 从一段字节中解析出所有完整包。
// B 站服务端可能在一个 WebSocket 消息里塞入多个包，
// 也可能先发一个外层包，body 里再嵌套解压后的多个包。
func unpack(data []byte) ([]Packet, error) {
	if len(data) > 0 && len(data) < headerLen {
		return nil, fmt.Errorf("数据长度 %d 不足协议头长度 %d", len(data), headerLen)
	}
	var packets []Packet
	for len(data) >= headerLen {
		total := int(binary.BigEndian.Uint32(data[0:4]))
		if total < headerLen || total > len(data) {
			return nil, fmt.Errorf("非法包长度 %d（剩余 %d 字节）", total, len(data))
		}
		p := Packet{
			Operation: binary.BigEndian.Uint32(data[8:12]),
			Version:   binary.BigEndian.Uint16(data[6:8]),
			Body:      data[headerLen:total],
		}
		data = data[total:]
		packets = append(packets, p)
	}
	return packets, nil
}

// decode 解析一个包的 body：解压嵌套数据并递归拆包。
func decode(p Packet) ([]Packet, error) {
	switch p.Version {
	case versionPlain:
		return []Packet{p}, nil
	case versionHeart:
		return []Packet{p}, nil
	case versionZlib:
		body, err := zlibDecompress(p.Body)
		if err != nil {
			return nil, fmt.Errorf("zlib 解压失败: %w", err)
		}
		inner, err := unpack(body)
		if err != nil {
			return nil, fmt.Errorf("解压后拆包失败: %w", err)
		}
		out := make([]Packet, 0, len(inner))
		for _, in := range inner {
			decoded, err := decode(in)
			if err != nil {
				return nil, err
			}
			out = append(out, decoded...)
		}
		return out, nil
	case versionBrotli:
		body, err := brotliDecompress(p.Body)
		if err != nil {
			return nil, fmt.Errorf("brotli 解压失败: %w", err)
		}
		inner, err := unpack(body)
		if err != nil {
			return nil, fmt.Errorf("解压后拆包失败: %w", err)
		}
		out := make([]Packet, 0, len(inner))
		for _, in := range inner {
			decoded, err := decode(in)
			if err != nil {
				return nil, err
			}
			out = append(out, decoded...)
		}
		return out, nil
	}
	return nil, fmt.Errorf("未知压缩版本 %d", p.Version)
}

// Decode 将一段 WebSocket 消息解析为多个协议包。
func Decode(data []byte) ([]Packet, error) {
	raw, err := unpack(data)
	if err != nil {
		return nil, err
	}
	out := make([]Packet, 0, len(raw))
	for _, p := range raw {
		decoded, err := decode(p)
		if err != nil {
			return nil, err
		}
		out = append(out, decoded...)
	}
	return out, nil
}

func zlibDecompress(data []byte) ([]byte, error) {
	r, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

func brotliDecompress(data []byte) ([]byte, error) {
	r := brotli.NewReader(bytes.NewReader(data))
	return io.ReadAll(r)
}

// Message 一条解析后的服务器消息。
type Message struct {
	Cmd  string          `json:"cmd"`
	Info json.RawMessage `json:"info"`
}

// DanmakuText 提取 DANMU_MSG 消息中的弹幕文本（info[1]）。
func (m Message) DanmakuText() string {
	if m.Cmd != "DANMU_MSG" {
		return ""
	}
	var info []json.RawMessage
	if err := json.Unmarshal(m.Info, &info); err != nil || len(info) < 2 {
		return ""
	}
	var text string
	if err := json.Unmarshal(info[1], &text); err != nil {
		return ""
	}
	return text
}

// ParseMessage 解析包 body 为服务器消息。
func ParseMessage(p Packet) (Message, error) {
	var msg Message
	if err := json.Unmarshal(p.Body, &msg); err != nil {
		return Message{}, fmt.Errorf("解析消息 JSON 失败: %w", err)
	}
	return msg, nil
}
