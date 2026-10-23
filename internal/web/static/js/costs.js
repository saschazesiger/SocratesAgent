// The costs page: what every conversation on this machine cost.
//
// The server sends the whole ledger once - every conversation, and its spend
// by UTC hour and model (internal/server/costs.go) - and everything on the
// page is summed here. That is what puts a day where this browser's clock
// says it is, and what lets a filter answer at once rather than after a
// round trip: a year of hourly rows is a few thousand arrays.
//
// The ledger lives in the bucket like the sessions do, so the numbers survive
// a restore on another machine; the transcripts they were read from do not.

import { api, el, errorMessage, onWake } from './api.js';

const $ = (id) => document.getElementById(id);

// The categorical slots, in their validated order (dataviz palette, light):
// a model keeps its slot by its all-time rank, so a filter never repaints it.
// Everything past the fourth is one quiet "Other".
const SERIES = ['#2a78d6', '#eb6834', '#1baf7a', '#eda100'];
const OTHER = '#c4c6cc';

const HARNESS = { claude: 'Claude Code', codex: 'Codex', opencode: 'OpenCode' };
const MORE = '#more';

const RANGES = [
  { id: '7d', label: '7 days' },
  { id: '30d', label: '30 days' },
  { id: '90d', label: '90 days' },
  { id: 'month', label: 'This month' },
  { id: 'lastmonth', label: 'Last month' },
  { id: 'year', label: 'This year' },
  { id: 'all', label: 'All' },
];

const PREFS = 'costs.view';
const POLL_SCANNING = 4000;
const POLL_IDLE = 120000;

const state = {
  data: null,
  rows: [], // decoded rows, see decode()
  range: '30d',
  filters: { harness: '', profile: '', profileSet: false, project: '', model: '' },
  slot: new Map(), // model -> series slot, by all-time cost
  timer: null,
  loading: false,
  expanded: new Set(),
};

// ------------------------------------------------------------------ formats

const usd = new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD', minimumFractionDigits: 2, maximumFractionDigits: 2 });
const usdWhole = new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD', maximumFractionDigits: 0 });

function money(v) {
  if (v > 0 && v < 0.005) return '<$0.01';
  return Math.abs(v) >= 10000 ? usdWhole.format(v) : usd.format(v);
}

// moneyAxis is the compact form ticks and tiles' small print use.
function moneyAxis(v) {
  if (v >= 1000) return '$' + (v / 1000).toFixed(v >= 10000 ? 0 : 1).replace(/\.0$/, '') + 'K';
  if (v >= 10 || v === 0) return '$' + Math.round(v);
  return '$' + v.toFixed(v < 1 ? 2 : 1).replace(/\.0+$/, '');
}

// tokens is the compact count: 950, 12.3K, 4.1M, 13.6B.
function tokens(n) {
  const v = Number(n) || 0;
  for (const [size, unit] of [[1e9, 'B'], [1e6, 'M'], [1e3, 'K']]) {
    if (v >= size) {
      const x = v / size;
      return (x < 10 ? x.toFixed(1) : String(Math.round(x))).replace(/\.0$/, '') + unit;
    }
  }
  return String(Math.round(v));
}

function pct(v) {
  if (v <= 0) return '0%';
  if (v < 0.001) return '<0.1%';
  return (v * 100).toFixed(v < 0.1 ? 1 : 0) + '%';
}

const pad = (n) => String(n).padStart(2, '0');
const dayKey = (d) => `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
const monthKey = (d) => `${d.getFullYear()}-${pad(d.getMonth() + 1)}`;

function dayFromKey(key) {
  const [y, m, d] = key.split('-').map(Number);
  return new Date(y, m - 1, d || 1);
}

const dayLong = new Intl.DateTimeFormat(undefined, { weekday: 'short', day: 'numeric', month: 'short', year: 'numeric' });
const dayShort = new Intl.DateTimeFormat(undefined, { day: 'numeric', month: 'short' });
const monthLong = new Intl.DateTimeFormat(undefined, { month: 'long', year: 'numeric' });
const monthShort = new Intl.DateTimeFormat(undefined, { month: 'short' });
const clock = new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit' });

function baseName(path) {
  if (!path) return '';
  const parts = path.replace(/\/+$/, '').split('/');
  return parts[parts.length - 1] || path;
}

function projectLabel(project) {
  if (project === MORE) return 'More Chats';
  if (!project) return 'Unknown folder';
  return baseName(project);
}

function profileLabel(id) {
  const names = state.data ? state.data.profiles : {};
  if (names && names[id] !== undefined) return names[id];
  return id ? id + ' (deleted)' : 'Default';
}

function convLabel(conv) {
  if (conv.title) return conv.title;
  return (HARNESS[conv.harness] || conv.harness) + ' in ' + projectLabel(conv.project);
}

// ------------------------------------------------------------------ data

function decode(data) {
  const out = [];
  for (const r of data.rows) {
    const conv = data.conversations[r[0]];
    if (!conv) continue;
    const at = new Date(r[1] * 3600000);
    out.push({
      conv, model: data.models[r[2]], at: at.getTime(), day: dayKey(at), month: monthKey(at),
      cost: r[3], input: r[4], output: r[5], cacheRead: r[6], cacheWrite: r[7], requests: r[8], unpriced: r[9],
    });
  }
  // The slots go to the models that cost the most over all time, so they do
  // not move when the period or a filter changes.
  const byModel = new Map();
  for (const r of out) byModel.set(r.model, (byModel.get(r.model) || 0) + r.cost);
  const ranked = [...byModel.entries()].sort((a, b) => b[1] - a[1]).map(([m]) => m);
  state.slot = new Map(ranked.slice(0, SERIES.length).map((m, i) => [m, i]));
  return out;
}

function matches(r) {
  const f = state.filters;
  if (f.harness && r.conv.harness !== f.harness) return false;
  // The default profile's id is empty, so "a profile is chosen" is a flag
  // of its own.
  if (f.profileSet && r.conv.profile !== f.profile) return false;
  if (f.project && r.conv.project !== f.project) return false;
  if (f.model && r.model !== f.model) return false;
  return true;
}

// bounds is the period as two local day keys, both included.
function bounds(id, rows) {
  const today = new Date();
  const start = new Date(today.getFullYear(), today.getMonth(), today.getDate());
  const back = (n) => new Date(start.getFullYear(), start.getMonth(), start.getDate() - n);
  switch (id) {
    case '7d': return { from: dayKey(back(6)), to: dayKey(start) };
    case '30d': return { from: dayKey(back(29)), to: dayKey(start) };
    case '90d': return { from: dayKey(back(89)), to: dayKey(start) };
    case 'month': return { from: dayKey(new Date(start.getFullYear(), start.getMonth(), 1)), to: dayKey(start) };
    case 'lastmonth': {
      const first = new Date(start.getFullYear(), start.getMonth() - 1, 1);
      const last = new Date(start.getFullYear(), start.getMonth(), 0);
      return { from: dayKey(first), to: dayKey(last) };
    }
    case 'year': return { from: dayKey(new Date(start.getFullYear(), 0, 1)), to: dayKey(start) };
    default: {
      let first = dayKey(start);
      for (const r of rows) if (r.day < first) first = r.day;
      return { from: first, to: dayKey(start) };
    }
  }
}

function daysBetween(from, to) {
  const out = [];
  const end = dayFromKey(to);
  for (let d = dayFromKey(from); d <= end; d = new Date(d.getFullYear(), d.getMonth(), d.getDate() + 1)) out.push(dayKey(d));
  return out;
}

function monthsBetween(from, to) {
  const out = [];
  const end = dayFromKey(to);
  for (let d = dayFromKey(from.slice(0, 7)); d <= end; d = new Date(d.getFullYear(), d.getMonth() + 1, 1)) out.push(monthKey(d));
  return out;
}

function blank() {
  return { cost: 0, input: 0, output: 0, cacheRead: 0, cacheWrite: 0, requests: 0, unpriced: 0 };
}

function addTo(sum, r) {
  sum.cost += r.cost;
  sum.input += r.input;
  sum.output += r.output;
  sum.cacheRead += r.cacheRead;
  sum.cacheWrite += r.cacheWrite;
  sum.requests += r.requests;
  sum.unpriced += r.unpriced;
  return sum;
}

function groupBy(rows, keyOf) {
  const groups = new Map();
  for (const r of rows) {
    const key = keyOf(r);
    let g = groups.get(key);
    if (!g) {
      g = { key, sample: r, ...blank() };
      groups.set(key, g);
    }
    addTo(g, r);
  }
  return [...groups.values()];
}

// ------------------------------------------------------------------ filters

function loadPrefs() {
  try {
    const saved = JSON.parse(localStorage.getItem(PREFS) || 'null');
    if (saved && RANGES.some((r) => r.id === saved.range)) state.range = saved.range;
    if (saved && saved.filters) Object.assign(state.filters, saved.filters);
  } catch { /* a private window */ }
}

function savePrefs() {
  try {
    localStorage.setItem(PREFS, JSON.stringify({ range: state.range, filters: state.filters }));
  } catch { /* a private window */ }
}

function drawRange() {
  const row = $('costsRange');
  row.replaceChildren(...RANGES.map((r) => el('button', {
    type: 'button', class: 'seg' + (r.id === state.range ? ' on' : ''), 'aria-pressed': r.id === state.range ? 'true' : 'false',
    onclick: () => { state.range = r.id; savePrefs(); render(); },
  }, el('span', { class: 'seg-label', text: r.label }))));
}

function fillSelect(select, field, options, allLabel) {
  const current = state.filters[field];
  select.replaceChildren(
    el('option', { value: '', text: allLabel }),
    ...options.map(([value, label]) => el('option', { value: '=' + value, text: label })),
  );
  const set = field === 'profile' ? state.filters.profileSet : current !== '';
  select.value = set ? '=' + current : '';
  if (select.value !== (set ? '=' + current : '')) select.value = '';
  select.onchange = () => {
    const v = select.value;
    if (field === 'profile') state.filters.profileSet = v !== '';
    state.filters[field] = v === '' ? '' : v.slice(1);
    savePrefs();
    render();
  };
}

function drawFilters() {
  const rows = state.rows;
  const seen = (fn) => [...new Set(rows.map(fn))];
  const byCost = (key) => {
    const totals = new Map();
    for (const r of rows) totals.set(key(r), (totals.get(key(r)) || 0) + r.cost);
    return (a, b) => (totals.get(b) || 0) - (totals.get(a) || 0);
  };
  fillSelect($('fHarness'), 'harness',
    seen((r) => r.conv.harness).sort(byCost((r) => r.conv.harness)).map((h) => [h, HARNESS[h] || h]), 'All programs');
  fillSelect($('fProfile'), 'profile',
    seen((r) => r.conv.profile).sort(byCost((r) => r.conv.profile)).map((p) => [p, profileLabel(p)]), 'All profiles');
  fillSelect($('fProject'), 'project',
    seen((r) => r.conv.project).sort(byCost((r) => r.conv.project)).map((p) => [p, projectLabel(p)]), 'All projects');
  fillSelect($('fModel'), 'model',
    seen((r) => r.model).sort(byCost((r) => r.model)).map((m) => [m, m]), 'All models');
  const f = state.filters;
  $('fClear').hidden = !(f.harness || f.profileSet || f.project || f.model);
}

function toggleFilter(field, value) {
  const f = state.filters;
  const on = field === 'profile' ? f.profileSet && f.profile === value : f[field] === value;
  if (field === 'profile') f.profileSet = !on;
  f[field] = on ? '' : value;
  savePrefs();
  render();
  window.scrollTo({ top: 0, behavior: 'smooth' });
}

// ------------------------------------------------------------------ tiles

function tile(label, value, sub, hero = false) {
  return el('div', { class: 'costs-tile' + (hero ? ' hero' : '') },
    el('div', { class: 'costs-tile-label', text: label }),
    el('div', { class: 'costs-tile-value', text: value }),
    sub ? el('div', { class: 'costs-tile-sub', text: sub }) : null);
}

function drawTiles(filtered, days) {
  const total = filtered.reduce((s, r) => s + r.cost, 0);
  const requests = filtered.reduce((s, r) => s + r.requests, 0);
  const used = filtered.reduce((s, r) => s + r.input + r.output + r.cacheRead + r.cacheWrite, 0);
  const all = state.rows.filter((r) => matches(r));
  const now = new Date();
  const today = dayKey(now);
  const month = monthKey(now);
  const lastMonth = monthKey(new Date(now.getFullYear(), now.getMonth() - 1, 1));
  const sumWhere = (fn) => all.reduce((s, r) => (fn(r) ? s + r.cost : s), 0);
  const todayCost = sumWhere((r) => r.day === today);
  const monthCost = sumWhere((r) => r.month === month);
  const allCost = sumWhere(() => true);
  const daysInMonth = new Date(now.getFullYear(), now.getMonth() + 1, 0).getDate();
  const elapsed = now.getDate() - 1 + (now.getHours() * 60 + now.getMinutes()) / 1440;
  const pace = elapsed > 0.5 ? monthCost / elapsed * daysInMonth : 0;
  const rangeName = RANGES.find((r) => r.id === state.range).label;
  $('costsTiles').replaceChildren(
    tile(rangeName === 'All' ? 'All time' : rangeName, money(total),
      `${requests.toLocaleString()} requests · ${tokens(used)} tokens`, true),
    tile('Per day', money(days ? total / days : 0), `average over ${days} ${days === 1 ? 'day' : 'days'}`),
    tile('Today', money(todayCost), dayShort.format(now)),
    tile('This month', money(monthCost), pace ? 'on pace for ' + moneyAxis(pace) : monthLong.format(now)),
    state.range === 'all'
      ? tile('Last month', money(sumWhere((r) => r.month === lastMonth)), monthLong.format(dayFromKey(lastMonth)))
      : tile('All time', money(allCost), allCost && all.length ? 'since ' + dayShort.format(dayFromKey(all.reduce((m, r) => (r.day < m ? r.day : m), today))) : ''),
  );
}

// ------------------------------------------------------------------ chart

function seriesOf(model) {
  return state.slot.has(model) ? state.slot.get(model) : SERIES.length;
}

function colorOf(slot) {
  return slot < SERIES.length ? SERIES[slot] : OTHER;
}

// niceMax rounds the axis top up to 1, 2, 2.5 or 5 times a power of ten.
function niceTicks(max) {
  if (max <= 0) return { top: 1, step: 0.25 };
  const raw = max / 4;
  const mag = 10 ** Math.floor(Math.log10(raw));
  const step = [1, 2, 2.5, 5, 10].map((m) => m * mag).find((s) => s >= raw);
  return { top: Math.ceil(max / step) * step, step };
}

const SVG = 'http://www.w3.org/2000/svg';
function svg(tag, attrs = {}) {
  const node = document.createElementNS(SVG, tag);
  for (const [k, v] of Object.entries(attrs)) node.setAttribute(k, v);
  return node;
}

// topRounded is a bar segment with 4px rounded corners at its data end and a
// square foot on the baseline.
function topRounded(x, y, w, h, r) {
  r = Math.min(r, w / 2, h);
  return `M${x},${y + h}V${y + r}Q${x},${y} ${x + r},${y}H${x + w - r}Q${x + w},${y} ${x + w},${y + r}V${y + h}Z`;
}

function drawChart(filtered, range) {
  const box = $('chart');
  const days = daysBetween(range.from, range.to);
  const monthly = days.length > 120;
  const keys = monthly ? monthsBetween(range.from, range.to) : days;
  const unit = monthly ? 'month' : 'day';
  $('chartTitle').textContent = monthly ? 'Cost per month' : 'Cost per day';

  // Stacks: one per bucket, one segment per series slot that has spend.
  const buckets = new Map(keys.map((k) => [k, { key: k, total: 0, slots: new Map(), models: new Map() }]));
  const present = new Map(); // slot -> models in it
  for (const r of filtered) {
    const b = buckets.get(monthly ? r.month : r.day);
    if (!b) continue;
    const slot = seriesOf(r.model);
    b.total += r.cost;
    b.slots.set(slot, (b.slots.get(slot) || 0) + r.cost);
    b.models.set(r.model, (b.models.get(r.model) || 0) + r.cost);
    if (!present.has(slot)) present.set(slot, new Set());
    present.get(slot).add(r.model);
  }
  const list = [...buckets.values()];
  const slots = [...present.keys()].sort((a, b) => a - b);
  const slotName = (slot) => {
    const models = [...present.get(slot)];
    return slot < SERIES.length || models.length === 1 ? models[0] : 'Other models';
  };

  const peak = list.reduce((m, b) => Math.max(m, b.total), 0);
  const total = list.reduce((s, b) => s + b.total, 0);
  const busiest = list.reduce((m, b) => (b.total > (m ? m.total : 0) ? b : m), null);
  $('chartSub').textContent = total > 0
    ? `${money(total)} in ${keys.length} ${unit}s · the most on ${busiest ? (monthly ? monthLong.format(dayFromKey(busiest.key)) : dayLong.format(dayFromKey(busiest.key))) : '—'} (${busiest ? money(busiest.total) : ''})`
    : 'Nothing was spent in this period.';

  // A legend for two series or more; one series is named by the title.
  $('chartLegend').replaceChildren(...(slots.length > 1 ? slots.map((slot) => el('span', { class: 'costs-key' },
    el('span', { class: 'costs-swatch', style: `background:${colorOf(slot)}` }),
    el('span', { text: slotName(slot) }))) : []));

  const width = Math.max(280, box.clientWidth || 600);
  const height = 240;
  const m = { top: 12, right: 8, bottom: 26, left: 52 };
  const plotW = width - m.left - m.right;
  const plotH = height - m.top - m.bottom;
  const { top, step } = niceTicks(peak);
  const y = (v) => m.top + plotH - (v / top) * plotH;
  const band = plotW / Math.max(1, list.length);
  const barW = Math.max(2, Math.min(24, band - 2));

  const root = svg('svg', { width, height, viewBox: `0 0 ${width} ${height}`, role: 'img',
    'aria-label': `${$('chartTitle').textContent}: ${$('chartSub').textContent}` });
  const hover = svg('rect', { x: 0, y: m.top, width: band, height: plotH, class: 'costs-hoverband', visibility: 'hidden' });
  root.append(hover);
  for (let v = 0; v <= top + step / 2; v += step) {
    const yy = Math.round(y(v)) + 0.5;
    root.append(svg('line', { x1: m.left, x2: width - m.right, y1: yy, y2: yy, class: v === 0 ? 'costs-base' : 'costs-grid' }));
    const label = svg('text', { x: m.left - 8, y: yy + 4, 'text-anchor': 'end', class: 'costs-axis' });
    label.textContent = moneyAxis(v);
    root.append(label);
  }

  // Sparse x labels: about one per 70px, always the first and the last.
  const every = Math.max(1, Math.ceil(70 / band));
  list.forEach((b, i) => {
    const cx = m.left + band * i + band / 2;
    if (b.total > 0) {
      let base = y(0);
      const present = slots.filter((s) => b.slots.get(s) > 0);
      present.forEach((slot, j) => {
        const h = (b.slots.get(slot) / top) * plotH;
        const gap = j > 0 ? 2 : 0; // the surface gap between stacked segments
        const segH = Math.max(0, h - gap);
        const yTop = base - h;
        const last = j === present.length - 1;
        if (segH > 0) {
          root.append(last
            ? svg('path', { d: topRounded(cx - barW / 2, yTop, barW, segH, 4), fill: colorOf(slot) })
            : svg('rect', { x: cx - barW / 2, y: yTop, width: barW, height: segH, fill: colorOf(slot) }));
        }
        base = yTop;
      });
    }
    if (i % every === 0 || i === list.length - 1) {
      if (i === list.length - 1 && i % every !== 0 && i % every < every / 2) return;
      const d = dayFromKey(b.key);
      const label = svg('text', { x: cx, y: height - 8, 'text-anchor': 'middle', class: 'costs-axis' });
      label.textContent = monthly ? (d.getMonth() === 0 || i === 0 ? `${monthShort.format(d)} ${d.getFullYear()}` : monthShort.format(d)) : dayShort.format(d);
      root.append(label);
    }
  });

  // One hit target per column, the full height of the plot: wider than any
  // bar, so a thin day is as easy to point at as a busy one.
  const tip = $('chartTip');
  const show = (b, i, ev) => {
    hover.setAttribute('x', m.left + band * i);
    hover.setAttribute('visibility', 'visible');
    const title = monthly ? monthLong.format(dayFromKey(b.key)) : dayLong.format(dayFromKey(b.key));
    const lines = [...b.models.entries()].sort((a, c) => c[1] - a[1]);
    tip.replaceChildren(...[
      el('div', { class: 'costs-tip-title', text: title }),
      el('div', { class: 'costs-tip-total', text: money(b.total) }),
      ...lines.slice(0, 6).map(([model, cost]) => el('div', { class: 'costs-tip-row' },
        el('span', { class: 'costs-swatch', style: `background:${colorOf(seriesOf(model))}` }),
        el('span', { class: 'costs-tip-name', text: model }),
        el('span', { class: 'costs-tip-value', text: money(cost) }))),
      lines.length > 6 ? el('div', { class: 'costs-tip-more', text: `and ${lines.length - 6} more` }) : null,
    ].filter(Boolean));
    tip.hidden = false;
    const rect = box.getBoundingClientRect();
    const tw = tip.offsetWidth;
    const th = tip.offsetHeight;
    let left = rect.left + m.left + band * i + band / 2 - tw / 2;
    left = Math.max(8, Math.min(window.innerWidth - tw - 8, left));
    let topPx = rect.top + m.top - th - 8;
    if (topPx < 8) topPx = (ev && ev.clientY ? ev.clientY : rect.top) + 16;
    tip.style.left = left + 'px';
    tip.style.top = topPx + 'px';
  };
  const hide = () => {
    hover.setAttribute('visibility', 'hidden');
    tip.hidden = true;
  };
  list.forEach((b, i) => {
    const hit = svg('rect', { x: m.left + band * i, y: m.top, width: band, height: plotH, fill: 'transparent', class: 'costs-hit' });
    hit.addEventListener('pointerenter', (ev) => show(b, i, ev));
    hit.addEventListener('pointerdown', (ev) => show(b, i, ev));
    hit.addEventListener('pointerleave', (ev) => { if (ev.pointerType === 'mouse') hide(); });
    root.append(hit);
  });
  root.addEventListener('pointerleave', hide);
  box.replaceChildren(root);
}

// ------------------------------------------------------------------ tables

const LIMIT = 10;

function table(id, groups, { name, sub, onPick, picked, href, limit = LIMIT, sort = 'cost' }) {
  const box = $(id);
  const total = groups.reduce((s, g) => s + g.cost, 0);
  if (sort === 'cost') groups.sort((a, b) => b.cost - a.cost);
  else groups.sort(sort);
  if (!groups.length || total <= 0) {
    box.replaceChildren(el('div', { class: 'costs-empty', text: 'Nothing in this period.' }));
    return;
  }
  const open = state.expanded.has(id);
  const shown = open ? groups : groups.slice(0, limit);
  const peak = groups.reduce((m, g) => Math.max(m, g.cost), 0);
  const head = el('div', { class: 'costs-row costs-head' },
    el('span', { class: 'costs-name', text: '' }),
    el('span', { class: 'costs-num', text: 'Cost' }),
    el('span', { class: 'costs-num costs-share', text: 'Share' }),
    el('span', { class: 'costs-num costs-wide', text: 'Tokens' }),
    el('span', { class: 'costs-num costs-wide', text: 'Requests' }));
  const rows = shown.map((g) => {
    const used = g.input + g.output + g.cacheRead + g.cacheWrite;
    const label = name(g);
    const detail = sub ? sub(g) : '';
    const target = href ? href(g) : '';
    const nameCell = el('span', { class: 'costs-name' },
      el(target ? 'a' : 'span', { class: 'costs-label', text: label, title: label, href: target || null }),
      detail ? el('span', { class: 'costs-sub', text: detail, title: detail }) : null,
      el('span', { class: 'costs-bar' }, el('span', { style: `width:${peak ? (g.cost / peak) * 100 : 0}%` })));
    const row = el(onPick ? 'button' : 'div', {
      class: 'costs-row' + (onPick ? ' pickable' : '') + (picked && picked(g) ? ' picked' : ''),
      type: onPick ? 'button' : null,
      title: onPick ? (picked && picked(g) ? 'Show everything again' : 'Show only this') : null,
      onclick: onPick ? () => onPick(g) : null,
    },
    nameCell,
    el('span', { class: 'costs-num costs-cost', text: money(g.cost) }),
    el('span', { class: 'costs-num costs-share', text: pct(g.cost / total) }),
    el('span', {
      class: 'costs-num costs-wide', text: tokens(used),
      title: `${g.input.toLocaleString()} in · ${g.output.toLocaleString()} out · ${g.cacheRead.toLocaleString()} cache read · ${g.cacheWrite.toLocaleString()} cache write`,
    }),
    el('span', { class: 'costs-num costs-wide', text: g.requests.toLocaleString() }));
    return row;
  });
  const more = groups.length > limit ? el('button', {
    class: 'ds-btn ds-btn--sm ds-btn--text costs-more', type: 'button',
    text: open ? 'Show fewer' : `Show all ${groups.length}`,
    onclick: () => { if (open) state.expanded.delete(id); else state.expanded.add(id); render(); },
  }) : null;
  box.replaceChildren(...[head, ...rows, more].filter(Boolean));
}

function drawTables(filtered) {
  const f = state.filters;
  table('tProject', groupBy(filtered, (r) => r.conv.project), {
    name: (g) => projectLabel(g.key),
    sub: (g) => (g.key === MORE ? 'folders Socrates made for one session' : g.key),
    onPick: (g) => toggleFilter('project', g.key), picked: (g) => f.project === g.key,
  });
  table('tModel', groupBy(filtered, (r) => r.model), {
    name: (g) => g.key,
    sub: (g) => (g.unpriced && g.cost === 0 ? `${g.unpriced.toLocaleString()} requests without a known price` : ''),
    onPick: (g) => toggleFilter('model', g.key), picked: (g) => f.model === g.key,
  });
  table('tMonth', groupBy(filtered, (r) => r.month), {
    name: (g) => monthLong.format(dayFromKey(g.key)),
    sort: (a, b) => (a.key < b.key ? 1 : -1), limit: 12,
  });
  table('tDay', groupBy(filtered, (r) => r.day), {
    name: (g) => dayLong.format(dayFromKey(g.key)),
    sort: (a, b) => (a.key < b.key ? 1 : -1), limit: 14,
  });
  table('tHarness', groupBy(filtered, (r) => r.conv.harness), {
    name: (g) => HARNESS[g.key] || g.key,
    onPick: (g) => toggleFilter('harness', g.key), picked: (g) => f.harness === g.key,
  });
  table('tProfile', groupBy(filtered, (r) => r.conv.profile), {
    name: (g) => profileLabel(g.key),
    onPick: (g) => toggleFilter('profile', g.key), picked: (g) => f.profileSet && f.profile === g.key,
  });
  table('tConv', groupBy(filtered, (r) => r.conv.key), {
    name: (g) => convLabel(g.sample.conv),
    sub: (g) => {
      const c = g.sample.conv;
      const when = c.last_at ? dayShort.format(new Date(c.last_at)) : '';
      return [HARNESS[c.harness] || c.harness, projectLabel(c.project), when, c.live ? '' : (c.session_id ? 'session deleted' : 'not started from Socrates')]
        .filter(Boolean).join(' · ');
    },
    href: (g) => (g.sample.conv.live ? '/#' + encodeURIComponent(g.sample.conv.session_id) : ''),
  });
}

// ------------------------------------------------------------------ page

function render() {
  if (!state.data) return;
  drawRange();
  drawFilters();
  const range = bounds(state.range, state.rows);
  const inRange = state.rows.filter((r) => r.day >= range.from && r.day <= range.to && matches(r));
  const days = daysBetween(range.from, range.to).length;
  drawTiles(inRange, days);
  drawChart(inRange, range);
  drawTables(inRange);
  $('costsCsv').disabled = inRange.length === 0;
  $('costsCsv').onclick = () => downloadCsv(inRange, range);
}

function drawState() {
  const data = state.data;
  const line = $('costsState');
  if (!data) return;
  if (data.scanning) line.textContent = 'Reading transcripts…';
  else if (data.scanned_at) line.textContent = 'Updated ' + clock.format(new Date(data.scanned_at));
  else line.textContent = '';
  const backup = !data.mirrored
    ? 'This Socrates has no bucket, so the ledger is kept on this machine only.'
    : data.pending
      ? `${data.pending} ${data.pending === 1 ? 'conversation is' : 'conversations are'} waiting to be backed up to the bucket.`
      : 'The ledger is backed up to the bucket and comes back with a restore.';
  $('costsNote').textContent = 'Estimated at the published API prices from the transcripts Claude Code, Codex and OpenCode keep '
    + '(OpenCode prices its answers itself). A subscription is billed differently: this is what the same work would cost on the API. '
    + 'Days are this device\'s days. ' + backup;
}

async function load() {
  if (state.loading) return;
  state.loading = true;
  clearTimeout(state.timer);
  try {
    const data = await api('/api/costs');
    state.data = data;
    state.rows = decode(data);
    $('notice').className = 'notice';
    $('notice').textContent = '';
    drawState();
    render();
  } catch (err) {
    $('notice').className = 'notice error';
    $('notice').textContent = errorMessage(err);
  } finally {
    state.loading = false;
    const scanning = state.data && state.data.scanning;
    state.timer = setTimeout(load, scanning ? POLL_SCANNING : POLL_IDLE);
  }
}

function csvCell(v) {
  const s = String(v ?? '');
  return /[",\n]/.test(s) ? '"' + s.replace(/"/g, '""') + '"' : s;
}

function downloadCsv(rows, range) {
  const groups = groupBy(rows, (r) => `${r.day}\u0000${r.conv.key}\u0000${r.model}`)
    .sort((a, b) => (a.sample.day === b.sample.day ? b.cost - a.cost : (a.sample.day < b.sample.day ? -1 : 1)));
  const lines = [['date', 'project', 'folder', 'program', 'profile', 'model', 'conversation', 'cost_usd',
    'input_tokens', 'output_tokens', 'cache_read_tokens', 'cache_write_tokens', 'requests'].join(',')];
  for (const g of groups) {
    const c = g.sample.conv;
    lines.push([g.sample.day, projectLabel(c.project), c.workdir, HARNESS[c.harness] || c.harness, profileLabel(c.profile),
      g.sample.model, convLabel(c), g.cost.toFixed(6), g.input, g.output, g.cacheRead, g.cacheWrite, g.requests]
      .map(csvCell).join(','));
  }
  const blob = new Blob([lines.join('\n') + '\n'], { type: 'text/csv' });
  const a = el('a', { href: URL.createObjectURL(blob), download: `socrates-costs-${range.from}-to-${range.to}.csv` });
  document.body.append(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(a.href), 1000);
}

loadPrefs();
$('fClear').addEventListener('click', () => {
  state.filters = { harness: '', profile: '', profileSet: false, project: '', model: '' };
  savePrefs();
  render();
});
let resizeFrame = 0;
new ResizeObserver(() => {
  cancelAnimationFrame(resizeFrame);
  resizeFrame = requestAnimationFrame(() => { if (state.data) render(); });
}).observe($('chart'));
document.addEventListener('pointerdown', (ev) => {
  if (!ev.target.closest('#chart') && !ev.target.closest('#chartTip')) $('chartTip').hidden = true;
});
onWake(() => load());
document.addEventListener('visibilitychange', () => { if (!document.hidden) load(); });
load();
