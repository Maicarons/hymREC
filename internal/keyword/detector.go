// Package keyword 实现面向切片场景的层级关键词检测引擎。
//
// 策略（对应 plan.md 第四节）：
//   - T1 核心触发：精确匹配即触发（如"星瞳"、"XT"）
//   - T2 关联触发：滑动窗口模糊匹配，阈值可调（如"瞳子"、"瞳宝"）
//   - T3 梗/衍生触发：需与 T1/T2 在时间窗口内联合出现才触发（如"腾讯大小姐"、"CDD"）
//   - T4 否定词：出现时屏蔽本次触发（如"不是星瞳"）
package keyword

import (
	"strings"
	"sync"
	"time"
)

// Level 关键词层级。
type Level int

const (
	LevelT1 Level = iota + 1 // 精确触发
	LevelT2                  // 模糊触发
	LevelT3                  // 联合触发
	LevelT4                  // 否定屏蔽
)

func (l Level) String() string {
	switch l {
	case LevelT1:
		return "T1"
	case LevelT2:
		return "T2"
	case LevelT3:
		return "T3"
	case LevelT4:
		return "T4"
	}
	return "?"
}

// Hit 一次关键词命中。
type Hit struct {
	Level   Level
	Keyword string
	At      time.Time
}

// Options 检测引擎参数。
type Options struct {
	T1 []string // 精确触发词
	T2 []string // 模糊触发词
	T3 []string // 联合触发词
	T4 []string // 否定词

	// StopDelay 关键词停止出现多久后结束录制。
	StopDelay time.Duration
	// Cooldown 两次触发的最小间隔（同一会话内）。
	Cooldown time.Duration
	// T2Threshold T2 模糊匹配相似度阈值 (0,1]。
	T2Threshold float64
	// T3CooccurWindow T3 词与 T1/T2 命中的联合时间窗口。
	T3CooccurWindow time.Duration
	// MaxBuffer 字符滑动窗口大小。
	MaxBuffer int
}

// Detector 层级关键词检测器。
//
// 并发约定：Feed 来自音频采集循环，Touch 来自弹幕回调 goroutine，
// Tick 来自停止判定定时器，故所有状态访问均由 mu 保护。
type Detector struct {
	mu   sync.Mutex
	opts Options

	buffer      []rune
	recentT1T2  []Hit
	lastHitTime time.Time
	lastTrigger time.Time
	isActive    bool
	hasHitEver  bool
}

// New 创建检测器。
func New(opts Options) *Detector {
	if opts.MaxBuffer <= 0 {
		opts.MaxBuffer = 40
	}
	if opts.StopDelay <= 0 {
		opts.StopDelay = 120 * time.Second
	}
	if opts.Cooldown <= 0 {
		opts.Cooldown = 5 * time.Second
	}
	if opts.T2Threshold <= 0 || opts.T2Threshold > 1 {
		opts.T2Threshold = 0.75
	}
	if opts.T3CooccurWindow <= 0 {
		opts.T3CooccurWindow = 30 * time.Second
	}
	return &Detector{opts: opts}
}

// Feed 输入一段增量识别文本，返回 (是否触发录制, 是否应停止录制)。
// at 为文本对应的墙钟时间戳。
func (d *Detector) Feed(text string, at time.Time) (triggered, shouldStop bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if text != "" {
		d.buffer = append(d.buffer, []rune(text)...)
		if len(d.buffer) > d.opts.MaxBuffer {
			d.buffer = d.buffer[len(d.buffer)-d.opts.MaxBuffer:]
		}
	}

	// 清理过期的 T1/T2 命中记录（用于 T3 联合触发）
	kept := d.recentT1T2[:0]
	for _, h := range d.recentT1T2 {
		if at.Sub(h.At) <= d.opts.T3CooccurWindow {
			kept = append(kept, h)
		}
	}
	d.recentT1T2 = kept

	if hit, ok := d.detect(at); ok {
		d.lastHitTime = at
		// 仅 T1/T2 命中进入联合触发记录（T3 命中不级联）
		if hit.Level == LevelT1 || hit.Level == LevelT2 {
			d.recentT1T2 = append(d.recentT1T2, hit)
		}
		if !d.isActive {
			if !d.hasHitEver || at.Sub(d.lastTrigger) >= d.opts.Cooldown {
				d.isActive = true
				d.hasHitEver = true
				d.lastTrigger = at
				return true, false
			}
		}
	}

	return false, d.tickLocked(at)
}

// Tick 执行停止超时判定，语义与 Feed 内的判定一致。
//
// 供 ASR 无输出时（VAD 判静音、识别结果为空、ASR 关闭）由调用方按固定
// 间隔驱动：停止判定若只依赖 Feed，主播长时间沉默时录制将无法结束。
// 非激活态返回 false；超时则转为非激活态并返回 true。
func (d *Detector) Tick(at time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.tickLocked(at)
}

// tickLocked 为 Tick 的无锁实现，调用方需持有 mu。
func (d *Detector) tickLocked(at time.Time) bool {
	if !d.isActive || d.lastHitTime.IsZero() {
		return false
	}
	if at.Sub(d.lastHitTime) >= d.opts.StopDelay {
		d.isActive = false
		return true
	}
	return false
}

// Touch 记录一次非 ASR 触发源的活跃事件（如弹幕命中关键词），刷新停止倒计时，
// 使弹幕触发的录制同样受 StopDelay 约束：持续有相关弹幕则继续录制，
// 静默超过停止延迟后由 Tick 结束。
//
// 仅应在录制会话进行中调用：非激活态下调用会占用激活态，抑制后续 ASR 触发。
func (d *Detector) Touch(at time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lastHitTime = at
	if !d.isActive {
		d.isActive = true
		d.hasHitEver = true
		d.lastTrigger = at
	}
}

// IsActive 返回当前是否处于录制激活状态。
func (d *Detector) IsActive() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.isActive
}

// Reset 清空内部状态（切换直播间时使用）。
func (d *Detector) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.buffer = nil
	d.recentT1T2 = nil
	d.lastHitTime = time.Time{}
	d.lastTrigger = time.Time{}
	d.isActive = false
	d.hasHitEver = false
}

// detect 执行层级检测，返回命中信息。
func (d *Detector) detect(at time.Time) (Hit, bool) {
	if len(d.buffer) == 0 {
		return Hit{}, false
	}
	window := string(d.buffer)

	// T4：否定词优先，命中则屏蔽本次检测
	for _, blocker := range d.opts.T4 {
		if strings.Contains(strings.ToLower(window), strings.ToLower(blocker)) {
			return Hit{}, false
		}
	}

	// T1：精确匹配（大小写不敏感）
	for _, kw := range d.opts.T1 {
		if strings.Contains(strings.ToLower(window), strings.ToLower(kw)) {
			return Hit{Level: LevelT1, Keyword: kw, At: at}, true
		}
	}

	// T2：模糊匹配（滑动窗口等长子串）
	for _, kw := range d.opts.T2 {
		if d.fuzzyMatch(kw) {
			return Hit{Level: LevelT2, Keyword: kw, At: at}, true
		}
	}

	// T3：梗词需与近期 T1/T2 联合出现
	for _, kw := range d.opts.T3 {
		if strings.Contains(strings.ToLower(window), strings.ToLower(kw)) && len(d.recentT1T2) > 0 {
			return Hit{Level: LevelT3, Keyword: kw, At: at}, true
		}
	}

	return Hit{}, false
}

// fuzzyMatch 在字符滑动窗口内寻找与关键词相似度 >= 阈值的等长子串。
// 比较前统一转小写，使英文关键词大小写不敏感。
func (d *Detector) fuzzyMatch(keyword string) bool {
	kw := lowerRunes(keyword)
	n := len(d.buffer)
	if n < len(kw) {
		return false
	}
	buf := lowerRunes(string(d.buffer))
	for i := 0; i <= n-len(kw); i++ {
		candidate := buf[i : i+len(kw)]
		if similarity(kw, candidate) >= d.opts.T2Threshold {
			return true
		}
	}
	return false
}

// lowerRunes 将字符串转为小写 rune 序列。
func lowerRunes(s string) []rune {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r >= 'A' && r <= 'Z' {
			out = append(out, r+('a'-'A'))
		} else {
			out = append(out, r)
		}
	}
	return out
}

// similarity 计算两个 rune 序列的相似度（0~1），
// 近似 Python difflib.SequenceMatcher.ratio()：2*LCS/(len(a)+len(b))。
func similarity(a, b []rune) float64 {
	m, n := len(a), len(b)
	if m == 0 && n == 0 {
		return 1
	}
	if m == 0 || n == 0 {
		return 0
	}
	// DP 求 LCS 长度
	prev := make([]int, n+1)
	cur := make([]int, n+1)
	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			if a[i-1] == b[j-1] {
				cur[j] = prev[j-1] + 1
			} else if prev[j] >= cur[j-1] {
				cur[j] = prev[j]
			} else {
				cur[j] = cur[j-1]
			}
		}
		prev, cur = cur, prev
	}
	return 2 * float64(prev[n]) / float64(m+n)
}
