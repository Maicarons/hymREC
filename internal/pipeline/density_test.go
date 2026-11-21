package pipeline

import (
	"testing"
	"time"
)

func TestDensityCounterUsesConfiguredKeywords(t *testing.T) {
	// 关键词必须来自 danmaku.keywords；未配置的词不参与统计
	d := newDensityCounter(30, 3, []string{"星瞳", "XT"})
	now := time.Now()

	if counted, _ := d.countHit("主播好可爱", now); counted {
		t.Fatal("不含关键词的弹幕不应计入密度")
	}
	if counted, _ := d.countHit("小星星来了", now); counted {
		t.Fatal("未配置在 danmaku.keywords 中的词不应计入密度")
	}
	if counted, dense := d.countHit("xt今天状态真好", now); !counted || dense {
		t.Fatal("关键词匹配应大小写不敏感，单条命中不应达标")
	}
	if _, dense := d.countHit("星瞳开播了", now); dense {
		t.Fatal("第 2 条命中小于阈值，不应达标")
	}
	if _, dense := d.countHit("XT来了", now); !dense {
		t.Fatal("窗口内命中数达到阈值时应达标")
	}
}

func TestDensityCounterConsumeStopsRepeatTrigger(t *testing.T) {
	d := newDensityCounter(30, 2, []string{"关键词"})
	now := time.Now()
	d.countHit("关键词一", now)
	if _, dense := d.countHit("关键词二", now); !dense {
		t.Fatal("达到阈值时应达标")
	}
	if !d.stillDense() {
		t.Fatal("延迟确认期间窗口应仍达标（不因达标而清零）")
	}
	d.consume()
	if d.stillDense() {
		t.Fatal("consume 后窗口应清空，避免同一波弹幕反复触发")
	}
}

func TestDensityCounterWindowExpiry(t *testing.T) {
	d := newDensityCounter(10, 2, []string{"关键词"})
	base := time.Now()

	if _, dense := d.countHit("关键词一", base); dense {
		t.Fatal("单条弹幕不应达标")
	}
	// 超出窗口的命中被裁剪，不应参与计数
	if _, dense := d.countHit("关键词二", base.Add(20*time.Second)); dense {
		t.Fatal("窗口已过期的命中不应计入")
	}
	// 窗口内的第二条命中达标
	if _, dense := d.countHit("关键词三", base.Add(21*time.Second)); !dense {
		t.Fatal("窗口内命中数达到阈值时应达标")
	}
}

func TestDensityCounterTrimsOnClock(t *testing.T) {
	d := newDensityCounter(30, 2, []string{"关键词"})
	d.countHit("关键词一", time.Now().Add(-20*time.Second))
	d.countHit("关键词二", time.Now())
	if !d.stillDense() {
		t.Fatal("30 秒窗口内的两条命中应仍被视为达标")
	}

	stale := newDensityCounter(10, 2, []string{"关键词"})
	stale.countHit("关键词一", time.Now().Add(-20*time.Second))
	stale.countHit("关键词二", time.Now().Add(-15*time.Second))
	if stale.stillDense() {
		t.Fatal("超出窗口的命中不应被视为达标")
	}
}

func TestDensityCounterReset(t *testing.T) {
	d := newDensityCounter(30, 2, []string{"关键词"})
	d.countHit("关键词一", time.Now())
	d.reset()
	if d.stillDense() {
		t.Fatal("reset 后不应保留窗口计数")
	}
}

func TestDensityCounterDefaults(t *testing.T) {
	d := newDensityCounter(0, 0, nil)
	if d.window != 30*time.Second || d.threshold != 5 {
		t.Fatalf("非法参数应回落到默认值，实际 window=%v threshold=%d", d.window, d.threshold)
	}
	if counted, _ := d.countHit("任意弹幕", time.Now()); counted {
		t.Fatal("关键词为空时不应计入密度")
	}
}

func TestDensityCounterIgnoresBlankKeywords(t *testing.T) {
	d := newDensityCounter(30, 2, []string{" ", ""})
	if len(d.keywords) != 0 {
		t.Fatalf("空白关键词应被忽略，实际: %v", d.keywords)
	}
}

func TestIsSilence(t *testing.T) {
	if !isSilence(nil, 500) {
		t.Fatal("空音频应判为静音")
	}
	if !isSilence(make([]int16, 9600), 500) {
		t.Fatal("全零音频应判为静音")
	}
	loud := make([]int16, 9600)
	for i := range loud {
		loud[i] = 8000
	}
	if isSilence(loud, 500) {
		t.Fatal("高能量音频不应判为静音")
	}
}
