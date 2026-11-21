/* hymREC 监控控制台：状态卡片、事件流、切片列表、日志与在线设置。 */
(() => {
  'use strict';

  const $ = (id) => document.getElementById(id);

  const state = {
    snapshot: null,
    events: [],
    logs: [],
    clips: [],
    settings: null,
    logFilter: 'all',
    follow: true,
    connected: false,
    lastStatusAt: 0,
  };

  const MAX_EVENTS = 200;
  const MAX_LOGS = 500;

  /* ---------- 格式化 ---------- */

  const pad = (n) => String(n).padStart(2, '0');

  function parseTime(value) {
    if (!value) return NaN;
    const text = String(value);
    const m = text.match(/^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(\.\d+)?(.*)$/);
    if (!m) return Date.parse(text);
    // Go 的时间戳可能带 9 位纳秒，截断到毫秒保证各浏览器一致
    const frac = m[2] ? m[2].slice(0, 4) : '';
    return Date.parse(m[1] + frac + m[3]);
  }

  const isRealTime = (t) => Number.isFinite(t) && new Date(t).getFullYear() > 2000;

  function fmtClock(value) {
    const t = parseTime(value);
    if (!isRealTime(t)) return '—';
    const d = new Date(t);
    return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
  }

  function fmtDateTime(value) {
    const t = parseTime(value);
    if (!isRealTime(t)) return '—';
    const d = new Date(t);
    return `${d.getMonth() + 1}/${d.getDate()} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
  }

  function fmtDuration(ms) {
    if (!Number.isFinite(ms) || ms < 0) return '0 秒';
    const total = Math.floor(ms / 1000);
    const h = Math.floor(total / 3600);
    const m = Math.floor((total % 3600) / 60);
    const s = total % 60;
    if (h > 0) return `${h} 小时 ${pad(m)} 分`;
    if (m > 0) return `${m} 分 ${pad(s)} 秒`;
    return `${s} 秒`;
  }

  function fmtBytes(n) {
    if (!Number.isFinite(n) || n <= 0) return '0 B';
    const units = ['B', 'KB', 'MB', 'GB', 'TB'];
    let value = n;
    let i = 0;
    while (value >= 1024 && i < units.length - 1) {
      value /= 1024;
      i += 1;
    }
    const digits = i === 0 ? 0 : value >= 100 ? 0 : 1;
    return `${value.toFixed(digits)} ${units[i]}`;
  }

  const sinceText = (ms) => (Number.isFinite(ms) ? `${Math.max(0, Math.round(ms / 1000))} 秒前` : '—');

  const el = (tag, cls, text) => {
    const node = document.createElement(tag);
    if (cls) node.className = cls;
    if (text != null) node.textContent = text;
    return node;
  };

  const SOURCE_LABEL = { asr: '语音', danmaku: '弹幕', manual: '手动' };
  const sourceLabel = (s) => SOURCE_LABEL[s] || s || '未知';

  const EVENT_LABEL = {
    service_start: '服务启动',
    service_stop: '服务停止',
    session_start: '会话开始',
    session_end: '会话结束',
    trigger: '触发',
    clip: '切片产出',
    stream_refresh: '刷新流地址',
    buffer_restart: '重启缓冲',
    error: '错误',
  };

  /* ---------- HTTP ---------- */

  async function api(path, options) {
    const res = await fetch(path, options);
    const text = await res.text();
    let data = null;
    if (text) {
      try { data = JSON.parse(text); } catch { data = null; }
    }
    if (!res.ok) {
      throw new Error((data && data.error) || `${res.status} ${res.statusText}`);
    }
    return data;
  }

  const post = (path, body) => api(path, {
    method: 'POST',
    headers: body ? { 'Content-Type': 'application/json' } : undefined,
    body: body ? JSON.stringify(body) : undefined,
  });

  function toast(message, tone = 'ok') {
    const box = $('toasts');
    const node = el('div', 'toast', message);
    node.dataset.tone = tone;
    box.append(node);
    setTimeout(() => node.remove(), 3800);
  }

  /* ---------- 状态渲染 ---------- */

  function setTag(id, text, tone) {
    const node = $(id);
    node.textContent = text;
    if (tone) node.dataset.tone = tone;
    else delete node.dataset.tone;
  }

  function renderStatus(snap) {
    if (!snap) return;
    state.snapshot = snap;
    state.lastStatusAt = Date.now();

    const svc = snap.service || {};
    const pill = $('status-pill');
    pill.dataset.state = svc.state || 'stopped';
    pill.textContent = svc.state_text || '已停止';
    $('version').textContent = svc.version ? `v${svc.version}` : '';

    $('btn-start').disabled = Boolean(svc.running);
    $('btn-stop').disabled = !svc.running;
    $('btn-trigger').disabled = !(svc.running && svc.state === 'live');
    $('btn-endrec').disabled = !(snap.recorder && snap.recorder.recording);

    const room = snap.room || {};
    $('room-id').textContent = room.id ? `#${room.id}` : '—';
    $('room-title').textContent = room.title || '未获取到直播信息';
    setTag('room-live', room.live_text || '未开播', room.live_status ? 'ok' : undefined);

    const buffer = snap.buffer || {};
    $('buffer-segments').textContent = `${buffer.segments || 0} 段`;
    $('buffer-sub').textContent = buffer.segments
      ? `覆盖 ${fmtDuration(buffer.span_ms)} · 最新分段 ${sinceText(buffer.stalled_ms)}`
      : '缓冲尚未产出分段';
    if (!buffer.segments) setTag('buffer-tag', '空', undefined);
    else if (buffer.stalled_ms >= 60000) setTag('buffer-tag', '停滞', 'bad');
    else if (buffer.stalled_ms >= 30000) setTag('buffer-tag', '延迟', 'warn');
    else setTag('buffer-tag', '正常', 'ok');

    const rec = snap.recorder || {};
    $('rec-sub').textContent = rec.recording
      ? `${sourceLabel(rec.source)}触发 · ${fmtClock(rec.trigger_at)} · 停止延迟 ${Math.round((rec.stop_delay_ms || 0) / 1000)} 秒`
      : `空闲 · 停止延迟 ${Math.round((rec.stop_delay_ms || 0) / 1000)} 秒`;
    setTag('rec-tag', rec.recording ? '录制中' : '空闲', rec.recording ? 'bad' : undefined);

    const asr = snap.asr || {};
    $('asr-type').textContent = asr.type || '—';
    $('asr-text').textContent = asr.last_text || '暂无识别结果';
    $('asr-text').title = asr.last_text || '';
    setTag('asr-tag', asr.last_at ? fmtClock(asr.last_at) : '—', asr.last_text ? 'info' : undefined);

    const dm = snap.danmaku || {};
    $('dm-hits').textContent = `${dm.hits || 0} / ${dm.threshold || 0} 条`;
    $('dm-sub').textContent = dm.enabled
      ? `统计窗口 ${Math.round((dm.window_ms || 0) / 1000)} 秒 · 密度达标即触发`
      : '弹幕触发已关闭';
    if (!dm.enabled) setTag('dm-tag', '已关闭', undefined);
    else if (dm.connected) setTag('dm-tag', '已连接', 'ok');
    else setTag('dm-tag', '未连接', 'warn');

    const clips = snap.clips || {};
    $('clips-count').textContent = `${clips.count || 0} 个`;
    $('clips-sub').textContent = clips.count
      ? `共 ${fmtBytes(clips.total_bytes)} · 最近 ${clips.last || '—'}`
      : '尚未产出切片';
    setTag('clips-tag', clips.count ? '本地' : '—', clips.count ? 'info' : undefined);
    $('clips-summary').textContent = clips.count ? `${clips.count} 个 · ${fmtBytes(clips.total_bytes)}` : '';

    updateDurations(snap);
  }

  /* 时长类字段每秒刷新，不依赖 SSE 推送频率 */
  function updateDurations(snap) {
    const svc = snap.service || {};
    if (svc.running) {
      const ms = Date.now() - parseTime(svc.since);
      $('run-duration').textContent = fmtDuration(ms);
      $('run-since').textContent = `开始于 ${fmtDateTime(svc.since)} · ${svc.state_text || ''}`;
      setTag('run-tag', '运行中', 'ok');
    } else {
      $('run-duration').textContent = '—';
      $('run-since').textContent = svc.last_error ? `上次异常：${svc.last_error}` : '服务未运行';
      setTag('run-tag', '已停止', undefined);
    }

    const rec = snap.recorder || {};
    if (rec.recording) {
      const elapsed = Date.now() - parseTime(rec.trigger_at);
      $('rec-elapsed').textContent = fmtDuration(elapsed);
    } else {
      $('rec-elapsed').textContent = '空闲';
    }

    const session = snap.session || {};
    if (session.active && isRealTime(parseTime(session.since))) {
      $('room-title').title = `会话开始 ${fmtDateTime(session.since)}`;
    }
  }

  /* ---------- 事件 ---------- */

  function renderEvents() {
    const list = $('events');
    list.textContent = '';
    if (!state.events.length) {
      const empty = el('li', 'empty', '暂无事件。触发、切片产出、会话开始/结束与自愈动作会显示在这里。');
      list.append(empty);
    } else {
      state.events.forEach((ev) => list.append(eventRow(ev)));
    }
    $('events-count').textContent = `${state.events.length}`;
  }

  function eventRow(ev) {
    const li = el('li');
    li.append(el('time', null, fmtClock(ev.at)));

    const badge = el('span', 'badge', EVENT_LABEL[ev.type] || ev.type);
    badge.dataset.kind = ev.type;
    li.append(badge);

    let message = ev.message || '';
    if (ev.type === 'trigger' || ev.type === 'clip') {
      const parts = [];
      if (ev.source) parts.push(sourceLabel(ev.source));
      if (message) parts.push(message);
      if (ev.file) parts.push(String(ev.file).split(/[\\/]/).pop());
      if (ev.size) parts.push(fmtBytes(ev.size));
      message = parts.join(' · ');
    }
    const msg = el('span', 'msg', message || '—');
    msg.title = message;
    li.append(msg);
    return li;
  }

  function addEvent(ev) {
    state.events.unshift(ev);
    if (state.events.length > MAX_EVENTS) state.events.length = MAX_EVENTS;
    renderEvents();
    if (ev.type === 'clip') refreshClips();
  }

  /* ---------- 日志 ---------- */

  function logMatches(entry) {
    switch (state.logFilter) {
      case 'error': return entry.level === 'ERROR';
      case 'warn': return entry.level === 'WARN' || entry.level === 'ERROR';
      case 'info': return entry.level !== 'DEBUG';
      default: return true;
    }
  }

  function logRow(entry) {
    const row = el('div', 'log-row');
    row.dataset.level = entry.level;
    row.append(el('time', null, fmtClock(entry.time)));
    row.append(el('span', 'lvl', entry.level));

    const msg = el('span', 'log-msg', entry.message || '');
    if (entry.attrs) {
      const attrs = el('span', 'log-attrs');
      Object.entries(entry.attrs).forEach(([key, value]) => {
        attrs.append(document.createTextNode(' '));
        attrs.append(el('b', null, `${key}=`));
        attrs.append(document.createTextNode(String(value)));
      });
      msg.append(attrs);
    }
    row.append(msg);
    return row;
  }

  function renderLogs() {
    const box = $('logs');
    box.textContent = '';
    const visible = state.logs.filter(logMatches);
    if (!visible.length) {
      box.append(el('div', 'empty', '暂无日志'));
      return;
    }
    visible.forEach((entry) => box.append(logRow(entry)));
    if (state.follow) box.scrollTop = box.scrollHeight;
  }

  function addLog(entry) {
    state.logs.push(entry);
    if (state.logs.length > MAX_LOGS) state.logs.shift();
    if (!logMatches(entry)) return;
    const box = $('logs');
    const empty = box.querySelector('.empty');
    if (empty) empty.remove();
    box.append(logRow(entry));
    while (box.childElementCount > MAX_LOGS) box.firstElementChild.remove();
    if (state.follow) box.scrollTop = box.scrollHeight;
  }

  /* ---------- 切片 ---------- */

  async function refreshClips() {
    try {
      const data = await api('/api/clips');
      state.clips = (data && data.clips) || [];
      renderClips();
    } catch (err) {
      toast(`切片列表刷新失败：${err.message}`, 'error');
    }
  }

  function renderClips() {
    const body = $('clips-body');
    body.textContent = '';
    if (!state.clips.length) {
      const tr = el('tr');
      const td = el('td', 'empty', '暂无切片');
      td.colSpan = 5;
      tr.append(td);
      body.append(tr);
      return;
    }
    state.clips.forEach((clip) => {
      const tr = el('tr');

      tr.append(el('td', 'mono', fmtClock(clip.trigger_at) === '—' ? fmtDateTime(clip.mod_time) : fmtClock(clip.trigger_at)));

      const srcTd = el('td');
      const badge = el('span', 'badge', sourceLabel(clip.source));
      badge.dataset.kind = clip.source === 'manual' ? 'session_start' : clip.source;
      srcTd.append(badge);
      tr.append(srcTd);

      tr.append(el('td', 'num', fmtBytes(clip.size)));

      const fileTd = el('td', 'file', clip.name);
      fileTd.title = clip.name;
      tr.append(fileTd);

      const actionTd = el('td');
      const link = el('a', 'dl', '下载');
      link.href = clip.url;
      link.setAttribute('download', clip.name);
      actionTd.append(link);
      tr.append(actionTd);

      body.append(tr);
    });
  }

  /* ---------- 设置 ---------- */

  function splitLines(text) {
    return String(text || '')
      .split(/\r?\n|,|，/)
      .map((s) => s.trim())
      .filter(Boolean);
  }

  function updateCounts() {
    const mapping = [
      ['c-t1', 'f-t1'], ['c-t2', 'f-t2'], ['c-t3', 'f-t3'], ['c-t4', 'f-t4'], ['c-dm', 'f-dm-keywords'],
    ];
    mapping.forEach(([countId, fieldId]) => {
      const n = splitLines($(fieldId).value).length;
      $(countId).textContent = n ? `${n} 个` : '空';
    });
  }

  async function openSettings() {
    try {
      const s = await api('/api/settings');
      state.settings = s;
      $('f-room').value = s.room_id || '';
      const kw = s.keyword || {};
      $('f-t1').value = (kw.t1 || []).join('\n');
      $('f-t2').value = (kw.t2 || []).join('\n');
      $('f-t3').value = (kw.t3 || []).join('\n');
      $('f-t4').value = (kw.t4 || []).join('\n');
      $('f-stop-delay').value = kw.stop_delay_seconds || 120;
      $('f-cooldown').value = kw.cooldown_seconds || 5;
      $('f-t2-threshold').value = kw.t2_threshold || 0.75;
      $('f-t3-window').value = kw.t3_cooccur_window || 30;

      const dm = s.danmaku || {};
      $('f-dm-keywords').value = (dm.keywords || []).join('\n');
      $('f-dm-threshold').value = dm.density_threshold || 5;
      $('f-dm-window').value = dm.window_seconds || 30;

      updateCounts();
      $('drawer').hidden = false;
      $('scrim').hidden = false;
      $('f-room').focus();
    } catch (err) {
      toast(`读取设置失败：${err.message}`, 'error');
    }
  }

  function closeSettings() {
    $('drawer').hidden = true;
    $('scrim').hidden = true;
  }

  function collectSettings() {
    const num = (id, fallback) => {
      const value = Number($(id).value);
      return Number.isFinite(value) && value > 0 ? value : fallback;
    };
    return {
      keyword: {
        t1: splitLines($('f-t1').value),
        t2: splitLines($('f-t2').value),
        t3: splitLines($('f-t3').value),
        t4: splitLines($('f-t4').value),
        stop_delay_seconds: num('f-stop-delay', 120),
        cooldown_seconds: num('f-cooldown', 5),
        t2_threshold: Math.min(1, Math.max(0.05, Number($('f-t2-threshold').value) || 0.75)),
        t3_cooccur_window: num('f-t3-window', 30),
      },
      danmaku: {
        enabled: Boolean(state.settings && state.settings.danmaku && state.settings.danmaku.enabled),
        keywords: splitLines($('f-dm-keywords').value),
        density_threshold: num('f-dm-threshold', 5),
        window_seconds: num('f-dm-window', 30),
        confirm_delay_seconds: (state.settings && state.settings.danmaku && state.settings.danmaku.confirm_delay_seconds) || 2,
      },
    };
  }

  async function applySettings({ restart = false } = {}) {
    const patch = collectSettings();
    try {
      await post('/api/settings', patch);
      if (restart) {
        const roomId = Number($('f-room').value);
        await post('/api/control/restart', roomId > 0 ? { room_id: roomId } : {});
        toast(roomId > 0 ? `已应用，正在重启并切换到 #${roomId}` : '已应用并重启服务');
      } else {
        toast('设置已应用');
      }
      state.settings = await api('/api/settings');
      updateCounts();
      if (restart) closeSettings();
    } catch (err) {
      toast(`保存失败：${err.message}`, 'error');
    }
  }

  /* ---------- 控制动作 ---------- */

  async function action(label, fn) {
    try {
      await fn();
      toast(`${label}成功`);
    } catch (err) {
      toast(`${label}失败：${err.message}`, 'error');
    }
  }

  function bindControls() {
    $('btn-start').addEventListener('click', () => action('启动', () => post('/api/control/start')));
    $('btn-stop').addEventListener('click', () => {
      if (!window.confirm('停止后未完成的录制会先合并成切片，然后服务退出监听。确定停止？')) return;
      action('停止', () => post('/api/control/stop'));
    });
    $('btn-trigger').addEventListener('click', () => action('手动触发', () => post('/api/control/trigger')));
    $('btn-endrec').addEventListener('click', () => action('结束录制', () => post('/api/control/stop-recording')));

    $('btn-settings').addEventListener('click', openSettings);
    $('btn-close-settings').addEventListener('click', closeSettings);
    $('scrim').addEventListener('click', closeSettings);
    document.addEventListener('keydown', (event) => {
      if (event.key === 'Escape' && !$('drawer').hidden) closeSettings();
    });
    $('settings-form').addEventListener('submit', (event) => event.preventDefault());
    $('btn-apply').addEventListener('click', () => applySettings({}));
    $('btn-restart').addEventListener('click', () => applySettings({ restart: true }));

    ['f-t1', 'f-t2', 'f-t3', 'f-t4', 'f-dm-keywords'].forEach((id) => {
      $(id).addEventListener('input', updateCounts);
    });

    $('btn-clear-events').addEventListener('click', () => {
      state.events = [];
      renderEvents();
    });
    $('btn-refresh-clips').addEventListener('click', refreshClips);
    $('btn-clear-logs').addEventListener('click', () => {
      state.logs = [];
      renderLogs();
    });
    $('log-level').addEventListener('change', (event) => {
      state.logFilter = event.target.value;
      renderLogs();
    });
    $('log-follow').addEventListener('change', (event) => {
      state.follow = event.target.checked;
      if (state.follow) $('logs').scrollTop = $('logs').scrollHeight;
    });

    $('btn-theme').addEventListener('click', () => {
      const next = document.documentElement.dataset.theme === 'light' ? 'dark' : 'light';
      document.documentElement.dataset.theme = next;
      localStorage.setItem('hymrec.theme', next);
    });
  }

  /* ---------- 实时推送 ---------- */

  function setConnected(on) {
    state.connected = on;
    const node = $('conn');
    node.dataset.state = on ? 'on' : 'off';
    node.title = on ? '实时连接正常' : '实时连接中断，正在重连';
  }

  function connect() {
    if (!window.EventSource) {
      setConnected(false);
      return;
    }
    const events = new EventSource('/api/events');

    events.addEventListener('open', () => setConnected(true));
    events.addEventListener('error', () => setConnected(false));
    events.addEventListener('status', (msg) => {
      setConnected(true);
      try { renderStatus(JSON.parse(msg.data)); } catch { /* 忽略坏帧 */ }
    });
    events.addEventListener('event', (msg) => {
      try { addEvent(JSON.parse(msg.data)); } catch { /* 忽略坏帧 */ }
    });
    events.addEventListener('log', (msg) => {
      try { addLog(JSON.parse(msg.data)); } catch { /* 忽略坏帧 */ }
    });
  }

  /* SSE 断开时用轮询兜底，避免界面停在旧状态 */
  function startFallbackPolling() {
    setInterval(async () => {
      if (state.connected && Date.now() - state.lastStatusAt < 4000) return;
      try {
        const snap = await api('/api/status');
        renderStatus(snap);
      } catch { /* 服务不可达时保持原状 */ }
    }, 3000);
  }

  /* ---------- 启动 ---------- */

  async function init() {
    const saved = localStorage.getItem('hymrec.theme');
    const prefersLight = window.matchMedia && window.matchMedia('(prefers-color-scheme: light)').matches;
    document.documentElement.dataset.theme = saved || (prefersLight ? 'light' : 'dark');

    bindControls();
    renderEvents();
    renderClips();

    try {
      const snapshot = await api('/api/status');
      renderStatus(snapshot);
    } catch (err) {
      toast(`读取状态失败：${err.message}`, 'error');
    }
    try {
      const data = await api('/api/logs?limit=200');
      state.logs = (data && data.logs) || [];
      renderLogs();
    } catch { /* 日志不可用不影响主面板 */ }

    await refreshClips();
    connect();
    startFallbackPolling();
    setInterval(() => {
      if (state.snapshot) updateDurations(state.snapshot);
    }, 1000);
  }

  document.addEventListener('DOMContentLoaded', init);
})();
