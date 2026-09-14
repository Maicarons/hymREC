package bili

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// wbiMixinKeyEncTab B 站 wbi 签名打乱表（固定）。
var wbiMixinKeyEncTab = []int{
	46, 47, 18, 2, 53, 8, 23, 32, 15, 50, 10, 31, 58, 3, 45, 35, 27, 43, 5, 49,
	33, 9, 42, 19, 29, 28, 14, 39, 12, 38, 41, 13, 37, 48, 7, 16, 24, 55, 40,
	61, 26, 17, 0, 1, 60, 51, 30, 4, 22, 25, 54, 21, 56, 59, 6, 63, 57, 62, 11,
	36, 20, 34, 44, 52,
}

// wbiKeys 缓存的 wbi key（img_key + sub_key）。
type wbiKeys struct {
	imgKey string
	subKey string
	at     time.Time
}

func mixinKey(orig string) string {
	var b strings.Builder
	for _, i := range wbiMixinKeyEncTab {
		b.WriteByte(orig[i])
	}
	return b.String()[:32]
}

// getWbiKeys 获取 wbi 签名所需的 img/sub key（通过主站 nav 接口）。
// 注意：未登录时 nav 返回 -101，但 data.wbi_img 仍然存在，可直接解析。
func (c *Client) getWbiKeys() (string, string, error) {
	r, err := c.get("https://api.bilibili.com/x/web-interface/nav", nil)
	if err != nil {
		return "", "", err
	}
	var data struct {
		WbiImg struct {
			ImgURL string `json:"img_url"`
			SubURL string `json:"sub_url"`
		} `json:"wbi_img"`
	}
	if err := json.Unmarshal(r.Data, &data); err != nil {
		return "", "", fmt.Errorf("解析 nav 响应失败: %w", err)
	}
	imgKey := fileKey(data.WbiImg.ImgURL)
	subKey := fileKey(data.WbiImg.SubURL)
	if imgKey == "" || subKey == "" {
		return "", "", fmt.Errorf("nav 接口未返回 wbi key")
	}
	return imgKey, subKey, nil
}

// fileKey 从 URL 中取文件名并去掉扩展名。
func fileKey(rawurl string) string {
	idx := strings.LastIndex(rawurl, "/")
	if idx < 0 {
		return ""
	}
	name := rawurl[idx+1:]
	if dot := strings.LastIndex(name, "."); dot >= 0 {
		name = name[:dot]
	}
	return name
}

// signWbi 对查询参数做 wbi 签名，返回带 wts/w_rid 的参数。
func (c *Client) signWbi(params url.Values) (url.Values, error) {
	imgKey, subKey, err := c.getWbiKeys()
	if err != nil {
		return nil, err
	}
	mixin := mixinKey(imgKey + subKey)

	params.Set("wts", fmt.Sprint(time.Now().Unix()))
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var qs strings.Builder
	for i, k := range keys {
		if i > 0 {
			qs.WriteByte('&')
		}
		qs.WriteString(k)
		qs.WriteByte('=')
		qs.WriteString(params.Get(k))
	}
	sum := md5.Sum([]byte(qs.String() + mixin))
	params.Set("w_rid", hex.EncodeToString(sum[:]))
	return params, nil
}

// DanmakuHost 一路弹幕服务器地址。
type DanmakuHost struct {
	Host    string `json:"host"`
	Port    int    `json:"port"`
	WSSPort int    `json:"wss_port"`
	WSPort  int    `json:"ws_port"`
}

// DanmakuServerInfo 弹幕服务器信息（token + 服务器列表）。
type DanmakuServerInfo struct {
	Token    string        `json:"token"`
	HostList []DanmakuHost `json:"host_list"`
	MaxDelay int           `json:"max_delay"`
	UID      int           `json:"uid"`
}

// GetDanmuInfo 获取房间的弹幕服务器地址与认证 token（需要 wbi 签名与 buvid3 cookie）。
// 返回后优先使用 WSS 连接（wss://host:wss_port/sub）。
func (c *Client) GetDanmuInfo(roomID int) (*DanmakuServerInfo, error) {
	params, err := c.signWbi(url.Values{"id": {fmt.Sprint(roomID)}, "type": {"0"}})
	if err != nil {
		return nil, fmt.Errorf("wbi 签名失败: %w", err)
	}
	r, err := c.get(c.apiURL("/xlive/web-room/v1/index/getDanmuInfo"), params)
	if err != nil {
		return nil, err
	}
	if err := c.checkCode(r); err != nil {
		return nil, err
	}
	var info DanmakuServerInfo
	if err := json.Unmarshal(r.Data, &info); err != nil {
		return nil, fmt.Errorf("解析 getDanmuInfo 响应失败: %w", err)
	}
	if info.Token == "" || len(info.HostList) == 0 {
		return nil, fmt.Errorf("getDanmuInfo 未返回可用的弹幕服务器（房间号 %d）", roomID)
	}
	return &info, nil
}

// EnsureBuvid3 访问 B 站首页获取 buvid3 等风控 cookie。
// buvid3 的 Set-Cookie 域为 www.bilibili.com，这里同时复制到
// .bilibili.com 公共域，使 api.bilibili.com / api.live.bilibili.com 均能携带。
func (c *Client) EnsureBuvid3() error {
	req, err := http.NewRequest(http.MethodGet, "https://www.bilibili.com/", nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", c.userAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("获取 buvid3 失败: %w", err)
	}
	_ = resp.Body.Close()
	if c.http.Jar != nil {
		www := &url.URL{Scheme: "https", Host: "www.bilibili.com"}
		cookies := c.http.Jar.Cookies(www)
		shared := &url.URL{Scheme: "https", Host: ".bilibili.com"}
		c.http.Jar.SetCookies(shared, cookies)
	}
	return nil
}
