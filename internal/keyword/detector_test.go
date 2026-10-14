package keyword

import (
	"testing"
	"time"
)

// testOptions 带紧凑参数的默认配置（便于快速测试）。
func testOptions() Options {
	return Options{
		T1:              []string{"星瞳", "XT"},
		T2:              []string{"瞳子", "瞳宝"},
		T3:              []string{"腾讯大小姐", "CDD"},
		T4:              []string{"不是星瞳"},
		StopDelay:       120 * time.Second,
		Cooldown:        5 * time.Second,
		T2Threshold:     0.75,
		T3CooccurWindow: 30 * time.Second,
	}
}

func TestT1ExactTrigger(t *testing.T) {
	d := New(testOptions())
	now := time.Now()
	triggered, shouldStop := d.Feed("主播提到了星瞳这个名字", now)
	if !triggered {
		t.Fatal("T1 精确命中应触发")
	}
	if shouldStop {
		t.Fatal("触发时不应同时停止")
	}
	if !d.IsActive() {
		t.Fatal("触发后应处于激活状态")
	}
}

func TestT1CaseInsensitive(t *testing.T) {
	d := New(testOptions())
	triggered, _ := d.Feed("说到 xt 我很喜欢", time.Now())
	if !triggered {
		t.Fatal("T1 大小写关键词应触发")
	}
}

func TestT2FuzzyMatch(t *testing.T) {
	d := New(testOptions())
	// "瞳子" 完整出现在文本中（T2 词不在 T1 列表），应触发
	triggered, _ := d.Feed("大家都叫她瞳子", time.Now())
	if !triggered {
		t.Fatal("T2 精确命中应触发")
	}
}

func TestT2ThresholdRejectsPartialMatch(t *testing.T) {
	d := New(testOptions())
	// "瞳字" 与 "瞳子" 相似度 0.5 < 0.75，不应触发（验证阈值生效）
	triggered, _ := d.Feed("大家都叫她瞳字", time.Now())
	if triggered {
		t.Fatal("相似度低于阈值的词不应触发")
	}
}

func TestT2NoMatch(t *testing.T) {
	d := New(testOptions())
	triggered, _ := d.Feed("今天的天气真不错呀", time.Now())
	if triggered {
		t.Fatal("无关文本不应触发")
	}
}

func TestT3AloneNoTrigger(t *testing.T) {
	d := New(testOptions())
	// T3 单独出现（无近期 T1/T2 命中）不触发
	triggered, _ := d.Feed("腾讯大小姐今天也很可爱", time.Now())
	if triggered {
		t.Fatal("T3 梗词单独出现不应触发")
	}
}

func TestT3WithRecentT1ExtendsSession(t *testing.T) {
	d := New(testOptions())
	now := time.Now()
	// 先 T1 命中触发
	d.Feed("星瞳今天开播了", now)
	if !d.IsActive() {
		t.Fatal("T1 命中后应处于激活状态")
	}
	// 窗口内（30s）出现 T3：已在录制中，不重复触发，但保持激活
	triggered, shouldStop := d.Feed("还聊到了腾讯大小姐", now.Add(10*time.Second))
	if triggered {
		t.Fatal("激活期间 T3 命中不应重复触发")
	}
	if shouldStop {
		t.Fatal("激活期间不应停止")
	}
	if !d.IsActive() {
		t.Fatal("T3 命中应延续激活状态")
	}
}

func TestT3AfterStopRetrigger(t *testing.T) {
	opts := testOptions()
	opts.StopDelay = 10 * time.Second
	d := New(opts)
	now := time.Now()
	// T1 触发后停止（窗口残留需先冲掉）
	d.Feed("星瞳", now)
	for i := 0; i < 3; i++ {
		d.Feed("今天天气不错适合直播和大家聊天", now.Add(time.Duration(i+1)*time.Second))
	}
	// 距最后命中超过 10 秒 -> 停止
	_, shouldStop := d.Feed("嗯", now.Add(12*time.Second))
	if !shouldStop {
		t.Fatal("超过停止延迟应返回停止")
	}
	if d.IsActive() {
		t.Fatal("停止后不应激活")
	}
	// 停止后 30 秒联合窗口内，T3 单独出现应重新触发
	triggered, _ := d.Feed("腾讯大小姐今天状态不错", now.Add(15*time.Second))
	if !triggered {
		t.Fatal("停止后 T3 与近期 T1 联合应重新触发")
	}
}

func TestT3WindowExpired(t *testing.T) {
	d := New(testOptions())
	now := time.Now()
	d.Feed("星瞳今天开播了", now)
	// 超过联合窗口后 T3 不再触发
	triggered, _ := d.Feed("聊到了腾讯大小姐", now.Add(60*time.Second))
	if triggered {
		t.Fatal("T1 命中超过联合窗口后 T3 不应触发")
	}
}

func TestT4Blocker(t *testing.T) {
	d := New(testOptions())
	triggered, _ := d.Feed("这个人不是星瞳啊", time.Now())
	if triggered {
		t.Fatal("T4 否定词应屏蔽触发")
	}
}

func TestCooldown(t *testing.T) {
	opts := testOptions()
	opts.StopDelay = 2 * time.Second
	opts.Cooldown = 8 * time.Second
	d := New(opts)
	now := time.Now()
	d.Feed("星瞳", now) // 触发，lastTrigger=now
	// 连续灌入 3 段 17 字无关文本，把"星瞳"挤出 40 字符窗口
	// 前两段命中残留（窗口仍含关键词），第三段后关键词滑出
	d.Feed("今天天气不错适合直播和大家聊天吧", now.Add(1*time.Second))
	d.Feed("我们来聊一些别的话题你看怎么样呀", now.Add(2*time.Second))
	d.Feed("跟刚才的事情没有任何关系的别的东西", now.Add(3*time.Second))
	// t=now+5s：距最后命中(3s) 2s >= StopDelay -> 停止
	if _, shouldStop := d.Feed("嗯", now.Add(5*time.Second)); !shouldStop {
		t.Fatal("应先停止")
	}
	// t=now+5.5s：距上次触发 5.5s < Cooldown 8s -> 冷却阻挡
	triggered, _ := d.Feed("星瞳", now.Add(5500*time.Millisecond))
	if triggered {
		t.Fatal("冷却期内不应触发")
	}
	// t=now+9s：距上次触发 9s >= Cooldown -> 再次触发
	triggered, _ = d.Feed("星瞳", now.Add(9*time.Second))
	if !triggered {
		t.Fatal("冷却期结束后应可再次触发")
	}
}

func TestStopDelay(t *testing.T) {
	opts := testOptions()
	opts.StopDelay = 10 * time.Second
	d := New(opts)
	now := time.Now()
	d.Feed("星瞳", now)
	if !d.IsActive() {
		t.Fatal("应处于激活状态")
	}
	// 灌入足够文本使关键词滑出 40 字符窗口
	for i := 0; i < 3; i++ {
		d.Feed("今天天气不错适合直播和大家聊天", now.Add(time.Duration(i+1)*time.Second))
	}
	// 距最后命中已超 10 秒 -> 应停止
	triggered, shouldStop := d.Feed("嗯", now.Add(12*time.Second))
	if triggered {
		t.Fatal("不应触发")
	}
	if !shouldStop {
		t.Fatal("超过停止延迟应返回停止")
	}
	if d.IsActive() {
		t.Fatal("停止后不应处于激活状态")
	}
}

func TestActiveContinuesOnHits(t *testing.T) {
	opts := testOptions()
	opts.StopDelay = 10 * time.Second
	d := New(opts)
	now := time.Now()
	d.Feed("星瞳", now)
	// 关键词持续出现，保持激活不停止
	for i := 0; i < 5; i++ {
		_, stop := d.Feed("星瞳", now.Add(time.Duration(i+1)*time.Second))
		if stop {
			t.Fatal("有命中的时候不应停止")
		}
	}
	if !d.IsActive() {
		t.Fatal("应保持激活")
	}
}

// TestTickStopsWithoutFeed 验证 ASR 无输出（静音/空识别）时，
// 仅靠 Tick 也能到达停止延迟并结束录制。
func TestTickStopsWithoutFeed(t *testing.T) {
	opts := testOptions()
	opts.StopDelay = 10 * time.Second
	d := New(opts)
	now := time.Now()
	d.Feed("星瞳", now)
	if !d.IsActive() {
		t.Fatal("应处于激活状态")
	}
	// 不再喂任何文本，仅由定时器推进
	if d.Tick(now.Add(5 * time.Second)) {
		t.Fatal("未到停止延迟不应停止")
	}
	if !d.IsActive() {
		t.Fatal("未到停止延迟应保持激活")
	}
	if !d.Tick(now.Add(10 * time.Second)) {
		t.Fatal("到达停止延迟应返回停止")
	}
	if d.IsActive() {
		t.Fatal("停止后不应处于激活状态")
	}
	// 停止判定只需触发一次
	if d.Tick(now.Add(20 * time.Second)) {
		t.Fatal("已停止后不应重复返回停止")
	}
}

// TestTickRefreshedByHits 验证录制期间再次命中会刷新倒计时，Tick 不再停止。
func TestTickRefreshedByHits(t *testing.T) {
	opts := testOptions()
	opts.StopDelay = 10 * time.Second
	d := New(opts)
	now := time.Now()
	d.Feed("星瞳", now)
	// 录制中每隔 8 秒命中一次，Tick 每 9 秒检查一次，均不应停止
	for i := 1; i <= 3; i++ {
		at := now.Add(time.Duration(i) * 8 * time.Second)
		d.Feed("星瞳", at)
		if d.Tick(at.Add(time.Second)) {
			t.Fatalf("第 %d 轮：关键词仍在出现，不应停止", i)
		}
	}
}

// TestTouchActivatesForDanmaku 验证弹幕触发的会话同样受停止延迟约束，
// 且激活后不重复触发（录制已在进行）。
func TestTouchActivatesForDanmaku(t *testing.T) {
	opts := testOptions()
	opts.StopDelay = 10 * time.Second
	d := New(opts)
	now := time.Now()
	// 弹幕密度达标触发录制：检测器未经过 Feed，由 Touch 激活
	d.Touch(now)
	if !d.IsActive() {
		t.Fatal("Touch 后应处于激活状态")
	}
	if d.Tick(now.Add(5 * time.Second)) {
		t.Fatal("未到停止延迟不应停止")
	}
	// 持续有命中关键词的弹幕：倒计时不断刷新
	d.Touch(now.Add(8 * time.Second))
	if d.Tick(now.Add(15 * time.Second)) {
		t.Fatal("弹幕持续触发期间不应停止")
	}
	// 弹幕安静超过停止延迟后应停止
	if !d.Tick(now.Add(19 * time.Second)) {
		t.Fatal("弹幕停止超过停止延迟应返回停止")
	}
	// 会话已停止，后续 ASR 命中可重新触发
	triggered, _ := d.Feed("星瞳", now.Add(25*time.Second))
	if !triggered {
		t.Fatal("停止后 ASR 命中应可再次触发")
	}
}

func TestReset(t *testing.T) {
	d := New(testOptions())
	d.Feed("星瞳", time.Now())
	if !d.IsActive() {
		t.Fatal("应先激活")
	}
	d.Reset()
	if d.IsActive() {
		t.Fatal("Reset 后应回到未激活")
	}
	triggered, _ := d.Feed("星瞳", time.Now())
	if !triggered {
		t.Fatal("Reset 后重新触发不受冷却限制")
	}
}

func TestSimilarity(t *testing.T) {
	cases := []struct {
		a, b string
		want float64
	}{
		{"星瞳", "星瞳", 1.0},
		{"星瞳", "瞳子", 0.5},
		{"hello", "hallo", 0.8}, // LCS=4 -> 8/10
		{"abc", "def", 0.0},
	}
	for _, c := range cases {
		got := similarity([]rune(c.a), []rune(c.b))
		if got < c.want-0.001 || got > c.want+0.001 {
			t.Errorf("similarity(%q, %q) = %v, 期望 %v", c.a, c.b, got, c.want)
		}
	}
}
