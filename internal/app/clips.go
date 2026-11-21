package app

import (
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// maxClips 列表中保留的切片数量上限。
const maxClips = 200

// Clip 一个已产出的切片文件。
type Clip struct {
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	ModTime   time.Time `json:"mod_time"`
	Source    string    `json:"source,omitempty"`
	TriggerAt time.Time `json:"trigger_at,omitempty"`
	URL       string    `json:"url"`
}

// ClipFromFile 根据文件信息构造切片记录；来源与触发时刻从文件名解析。
func ClipFromFile(name string, info os.FileInfo) Clip {
	source, at := parseClipName(name)
	return Clip{
		Name:      name,
		Size:      info.Size(),
		ModTime:   info.ModTime(),
		Source:    source,
		TriggerAt: at,
		URL:       "/api/clips/" + url.PathEscape(name),
	}
}

// parseClipName 从 <prefix>_<source>_YYYYMMDD_HHMMSS.<ext> 解析触发来源与时刻。
// 文件名不符合该格式时返回空来源与零时刻。
func parseClipName(name string) (string, time.Time) {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	parts := strings.Split(base, "_")
	if len(parts) < 2 {
		return "", time.Time{}
	}
	date, clock := parts[len(parts)-2], parts[len(parts)-1]
	at, err := time.ParseInLocation("20060102_150405", date+"_"+clock, time.Local)
	if err != nil {
		return "", time.Time{}
	}
	source := ""
	if len(parts) >= 3 {
		source = parts[len(parts)-3]
	}
	return source, at
}

// scanClips 扫描输出目录中已存在的切片（重启后仍能看到历史产物）。
func scanClips(dir string) []Clip {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	clips := make([]Clip, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".ts", ".mp4":
		default:
			continue
		}
		if _, at := parseClipName(e.Name()); at.IsZero() {
			continue // 不是本工具产出的命名，忽略
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		clips = append(clips, ClipFromFile(e.Name(), info))
	}
	sort.SliceStable(clips, func(i, j int) bool {
		return clips[i].TriggerAt.After(clips[j].TriggerAt)
	})
	if len(clips) > maxClips {
		clips = clips[:maxClips]
	}
	return clips
}
