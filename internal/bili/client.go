// Package bili 封装 B 站直播相关 API：
// 短号 -> 真实房间号、开播状态查询、直播流地址获取。
package bili

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// UserAgent 模拟移动端浏览器 UA（B 站对默认 Go UA 可能返回风控响应）。
const UserAgent = "Mozilla/5.0 (iPhone; CPU iPhone OS 13_2_3 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/13.0.3 Mobile/15E148 Safari/604.1"

const apiHost = "https://api.live.bilibili.com"

const (
	apiRoomInit    = "/room/v1/Room/room_init"
	apiRoomInfoOld = "/room/v1/Room/getRoomInfoOld"
	apiPlayURL     = "/room/v1/Room/playUrl"
)

// Client B 站 API 客户端。
type Client struct {
	http      *http.Client
	userAgent string
	baseURL   string // 覆盖 API 域名（测试用）
}

// NewClient 创建客户端；httpClient 为空时使用默认 15 秒超时的客户端。
func NewClient(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{http: httpClient, userAgent: UserAgent}
}

// apiURL 拼接 API 地址（支持 baseURL 覆盖）。
func (c *Client) apiURL(path string) string {
	if c.baseURL != "" {
		return c.baseURL + path
	}
	return apiHost + path
}

// SetBaseURL 覆盖 API 域名，供测试接入 mock 服务器。
func (c *Client) SetBaseURL(base string) { c.baseURL = base }

// RoomStatus 房间状态。
type RoomStatus struct {
	RoomID     int    `json:"roomid"`
	ShortID    int    `json:"short_id"`
	UID        int    `json:"uid"`
	LiveStatus int    `json:"liveStatus"` // 0=未开播 1=直播中 2=轮播
	Title      string `json:"title"`
	LiveTime   string `json:"live_time"`
	Online     int    `json:"online"`
}

// apiResponse B 站 API 通用响应包装。
type apiResponse struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// get 发起 GET 请求并解析为标准响应包装。
func (c *Client) get(rawurl string, params url.Values) (*apiResponse, error) {
	if params != nil {
		if idx := strings.IndexByte(rawurl, '?'); idx >= 0 {
			rawurl = rawurl[:idx+1] + params.Encode()
		} else {
			rawurl = rawurl + "?" + params.Encode()
		}
	}
	req, err := http.NewRequest(http.MethodGet, rawurl, nil)
	if err != nil {
		return nil, fmt.Errorf("构造请求失败: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Referer", "https://live.bilibili.com/")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 %s 失败: %w", rawurl, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("请求 %s 返回非 200 状态: %d", rawurl, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}
	var apiResp apiResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("解析响应 JSON 失败: %w", err)
	}
	return &apiResp, nil
}

func (c *Client) checkCode(r *apiResponse) error {
	if r.Code != 0 {
		return fmt.Errorf("B站 API 返回错误码 %d: %s", r.Code, r.Message)
	}
	return nil
}

// GetRealRoomID 短号 -> 真实房间号（短号等于真实房间号时原样返回）。
func (c *Client) GetRealRoomID(shortID int) (int, error) {
	r, err := c.get(c.apiURL(apiRoomInit), url.Values{"id": {fmt.Sprint(shortID)}})
	if err != nil {
		return 0, err
	}
	if err := c.checkCode(r); err != nil {
		return 0, err
	}
	var data struct {
		RoomID int `json:"room_id"`
	}
	if err := json.Unmarshal(r.Data, &data); err != nil {
		return 0, fmt.Errorf("解析 room_init 响应失败: %w", err)
	}
	if data.RoomID == 0 {
		return 0, fmt.Errorf("未找到房间 %d 对应的真实房间号", shortID)
	}
	return data.RoomID, nil
}

// GetRoomStatus 查询房间开播状态与标题。
func (c *Client) GetRoomStatus(roomID int) (*RoomStatus, error) {
	r, err := c.get(c.apiURL(apiRoomInfoOld), url.Values{"mid": {fmt.Sprint(roomID)}})
	if err != nil {
		return nil, err
	}
	if err := c.checkCode(r); err != nil {
		return nil, err
	}
	var st RoomStatus
	if err := json.Unmarshal(r.Data, &st); err != nil {
		return nil, fmt.Errorf("解析 getRoomInfoOld 响应失败: %w", err)
	}
	return &st, nil
}

// IsLive 判断房间是否正在直播。
func (c *Client) IsLive(roomID int) (bool, error) {
	st, err := c.GetRoomStatus(roomID)
	if err != nil {
		return false, err
	}
	return st.LiveStatus == 1, nil
}

// StreamURL 一路直播流地址。
type StreamURL struct {
	URL    string `json:"url"`
	Host   string `json:"host"`
	Format string `json:"format"`
	QN     int    `json:"qn"`
	Codec  int    `json:"codec"`
}

// GetStreamURL 获取直播流地址。
//
// qn 为画质（10000=原画）。platform 决定返回格式：
//   - "web"：FLV（HTTP-FLV 可直接流式写入文件）
//   - "h5"：HLS（.m3u8，需 ffmpeg 或 HLS 客户端处理）
//
// 返回第一个可用地址；主播未开播时返回 ErrNotLive。
func (c *Client) GetStreamURL(roomID, qn int, platform string) (*StreamURL, error) {
	realID, err := c.GetRealRoomID(roomID)
	if err != nil {
		return nil, err
	}
	params := url.Values{
		"cid":      {fmt.Sprint(realID)},
		"otype":    {"json"},
		"qn":       {fmt.Sprint(qn)},
		"platform": {platform},
	}
	r, err := c.get(c.apiURL(apiPlayURL), params)
	if err != nil {
		return nil, err
	}
	if err := c.checkCode(r); err != nil {
		return nil, err
	}
	var data struct {
		Durl []StreamURL `json:"durl"`
	}
	if err := json.Unmarshal(r.Data, &data); err != nil {
		return nil, fmt.Errorf("解析 playUrl 响应失败: %w", err)
	}
	for i := range data.Durl {
		u := &data.Durl[i]
		if u.URL != "" {
			return u, nil
		}
	}
	return nil, ErrNotLive
}

// ErrNotLive 表示直播间未开播或没有可用流。
var ErrNotLive = errors.New("主播未开播或暂无可用流地址")
