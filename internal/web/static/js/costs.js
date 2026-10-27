// The costs page: what every conversation on this machine cost.
//
// The server sends the whole ledger once - every conversation, and its spend
// by UTC hour and model (internal/server/costs.go) - and everything on the
// page is summed here. That is what puts a day where this browser's clock
// says it is, and what lets a filter answer at once rather than after a
// round trip: a year of hourly rows is a few thousand arrays.
//
// The ledger lives in the database like the sessions do, so the numbers survive
// a restore from the backup; and a CLI that prunes its transcripts does not
// take their cost with it.

import { api, el, errorMessage, onWake } from './api.js';
import { M, html, raw, icon, morph } from './ui.js';
import * as harnesses from './harnesses.js';

const $ = (id) => document.getElementById(id);

// The categorical slots, in their order (DS's chart roles --ds-chart-1 to
// --ds-chart-4): a model keeps its slot by its all-time rank, so a filter
// never repaints it. Everything past the fourth is one quiet "Other"
// (--ds-chart-neutral).
const SERIES = 4;

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
  names: new Map(), // model id -> how the page names it (modelNames)
  accounts: new Map(), // account id -> how the page names it (accountNames)
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

// axisMoney is a tick's label: below a dollar a step, as many decimals as
// the step needs, so that a chart whose biggest day is a cent does not say
// "$0.01" at three ticks (moneyAxis rounds to cents).
function axisMoney(v, step) {
  if (step >= 1 || v === 0) return moneyAxis(v);
  let digits = 2;
  while (digits < 6 && Math.abs(Math.round(step * 10 ** digits) - step * 10 ** digits) > 1e-6) digits += 1;
  return '$' + v.toFixed(digits).replace(/(\.\d\d\d*?)0+$/, '$1');
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

// profileLabel names what a ledger is filed under. A conversation's
// "profile", as the server names it, is the account it ran on: a chat's
// account, or for a conversation from before accounts the sign-in profile it
// ran in, which the migration made an account. The page says account, which
// is what a person has now.
function profileLabel(id) {
  const names = state.data ? state.data.profiles : {};
  if (names && names[id] !== undefined) return names[id];
  return id ? id + ' (deleted)' : 'Default';
}

// modelNames is how the page names each model: as the rest of the app does,
// by the harness catalogue's label ("Sonnet" for the claude-sonnet-… a
// transcript records), else by its id. Two ids that would read the same keep
// their ids beside the name. The id itself stays in the CSV and under the
// name in the By model table.
function modelNames(rows) {
  const harnessOf = new Map();
  for (const r of rows) if (!harnessOf.has(r.model)) harnessOf.set(r.model, r.conv.harness);
  const named = new Map();
  for (const [model, harness] of harnessOf) {
    let found = null;
    try { found = harnesses.modelOf(harness, model); } catch { /* no catalogue yet */ }
    named.set(model, found ? (found.label || found.id) : model);
  }
  const uses = new Map();
  for (const name of named.values()) uses.set(name, (uses.get(name) || 0) + 1);
  for (const [model, name] of named) if (uses.get(name) > 1 && name !== model) named.set(model, name + ' (' + model + ')');
  return named;
}

function modelName(model) {
  return state.names.get(model) || model;
}

// accountNames names each account with the program it belongs to ("Claude
// Code · Default"): every agent has a Default account, and three rows and
// three filter entries that all said "Default" could not be told apart. A
// key that spans programs - the conversations from before accounts, filed
// under the profile they ran in - keeps its plain name.
function accountNames(rows) {
  const agents = new Map();
  for (const r of rows) {
    if (!agents.has(r.conv.profile)) agents.set(r.conv.profile, new Set());
    agents.get(r.conv.profile).add(r.conv.harness);
  }
  const named = new Map();
  for (const [id, set] of agents) {
    const one = set.size === 1 ? [...set][0] : '';
    named.set(id, (one ? (HARNESS[one] || one) + ' · ' : '') + profileLabel(id));
  }
  return named;
}

function accountName(id) {
  return state.accounts.get(id) || profileLabel(id);
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
  state.slot = new Map(ranked.slice(0, SERIES).map((m, i) => [m, i]));
  return out;
}

function matches(r) {
  const f = state.filters;
  if (f.harness && r.conv.harness !== f.harness) return false;
  // The default account's id is empty, so "an account is chosen" is a flag
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

// drawRange puts the period's chips in once and then only says which one is
// chosen: radio chips trade the selection by themselves.
function drawRange() {
  const box = $('costsRange');
  if (!box.querySelector('input')) {
    morph(box, html`<legend class="ds-sr">Period</legend>${RANGES.map((r) => html`<label class="ds-chip"><input class="ds-chip__input" type="radio" name="costsRange" value="${r.id}"><span class="ds-chip__body">${raw(icon('check', 18, 'ds-chip__check'))}<span>${r.label}</span></span></label>`)}`);
    box.addEventListener('change', (ev) => {
      if (ev.target.name !== 'costsRange') return;
      state.range = ev.target.value;
      savePrefs();
      render();
    });
  }
  for (const input of box.querySelectorAll('input')) input.checked = input.value === state.range;
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
  // A long account or project is cut in the closed field: its tip says it.
  select.title = select.selectedOptions[0] ? select.selectedOptions[0].textContent : '';
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
    seen((r) => r.conv.profile).sort(byCost((r) => r.conv.profile)).map((p) => [p, accountName(p)]), 'All accounts');
  fillSelect($('fProject'), 'project',
    seen((r) => r.conv.project).sort(byCost((r) => r.conv.project)).map((p) => [p, projectLabel(p)]), 'All projects');
  fillSelect($('fModel'), 'model',
    seen((r) => r.model).sort(byCost((r) => r.model)).map((m) => [m, modelName(m)]), 'All models');
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
  // The view is the page's one scroller (DS App frame), not the window.
  $('main').scrollTo({ top: 0, behavior: 'smooth' });
}

// ------------------------------------------------------------------ tiles

// tileMoney is a tile's value: six glyphs at most, which is what a 320px
// phone's two tiles a row hold at DS's 26cqi without running out of the
// tile ("$1,234.56" did, and "$999.99" by a hair). Cents below $100, whole
// dollars to $10,000, then thousands and millions; the exact amount is the
// value's tip.
function tileMoney(v) {
  const a = Math.abs(v);
  if (a >= 999500) return '$' + (v / 1e6).toFixed(a >= 9.95e6 ? 0 : 1).replace(/\.0$/, '') + 'M';
  if (a >= 9999.5) return '$' + Math.round(v / 1000) + 'K';
  if (a >= 99.995) return usdWhole.format(v);
  return money(v);
}

// figure counts a tile's value on from the one it shows (DS countUp).
function figure(id, value) {
  const node = $(id);
  M.countUp(node, value, { format: tileMoney });
  node.title = money(value);
}

function drawTiles(filtered, days) {
  const total = filtered.reduce((s, r) => s + r.cost, 0);
  const requests = filtered.reduce((s, r) => s + r.requests, 0);
  const used = filtered.reduce((s, r) => s + r.input + r.output + r.cacheRead + r.cacheWrite, 0);
  const all = state.rows.filter((r) => matches(r));
  const now = new Date();
  const today = dayKey(now);
  const month = monthKey(now);
  const sumWhere = (fn) => all.reduce((s, r) => (fn(r) ? s + r.cost : s), 0);
  const todayCost = sumWhere((r) => r.day === today);
  const monthCost = sumWhere((r) => r.month === month);
  const daysInMonth = new Date(now.getFullYear(), now.getMonth() + 1, 0).getDate();
  const elapsed = now.getDate() - 1 + (now.getHours() * 60 + now.getMinutes()) / 1440;
  const pace = elapsed > 0.5 ? monthCost / elapsed * daysInMonth : 0;
  const rangeName = RANGES.find((r) => r.id === state.range).label;
  $('tRangeLabel').textContent = rangeName === 'All' ? 'All time' : rangeName;
  figure('tRange', total);
  $('tRangeNote').textContent = `${requests.toLocaleString()} ${requests === 1 ? 'request' : 'requests'} · ${tokens(used)} ${used === 1 ? 'token' : 'tokens'}`;
  figure('tPerDay', days ? total / days : 0);
  $('tPerDayNote').textContent = `average over ${days} ${days === 1 ? 'day' : 'days'}`;
  figure('tToday', todayCost);
  $('tTodayNote').textContent = dayShort.format(now);
  figure('tMonth', monthCost);
  // Under a dollar in cents, as the tiles say it: "on pace for $0" beside a
  // month of <$0.01 read as nothing spent.
  $('tMonthNote').textContent = pace ? 'on pace for ' + (pace < 1 ? money(pace) : moneyAxis(pace)) : monthLong.format(now);
}

// ------------------------------------------------------------------ chart

function seriesOf(model) {
  return state.slot.has(model) ? state.slot.get(model) : SERIES;
}

// seriesKey is what a mark carries in data-series, which picks its colour.
function seriesKey(slot) {
  return slot < SERIES ? String(slot + 1) : 'other';
}

// keyHtml is a series' key, DS's legend key in its chart role.
const KEY_TONE = ['', ' ds-legend__key--s2', ' ds-legend__key--s3', ' ds-legend__key--s4'];
function keyHtml(slot) {
  return html`<span class="${'ds-legend__key' + (slot < SERIES ? KEY_TONE[slot] : ' ds-legend__key--neutral')}"></span>`;
}

// px is a length token of the chart's box in pixels.
function px(box, token, fallback) {
  const value = parseFloat(getComputedStyle(box).getPropertyValue(token));
  return Number.isFinite(value) ? value : fallback;
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

// topRounded is a bar segment with rounded corners at its data end and a
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
    return slot < SERIES || models.length === 1 ? modelName(models[0]) : 'Other models';
  };

  const peak = list.reduce((m, b) => Math.max(m, b.total), 0);
  const total = list.reduce((s, b) => s + b.total, 0);
  const busiest = list.reduce((m, b) => (b.total > (m ? m.total : 0) ? b : m), null);
  $('chartSub').textContent = total > 0
    ? `${money(total)} in ${keys.length} ${unit}s · the most on ${busiest ? (monthly ? monthLong.format(dayFromKey(busiest.key)) : dayLong.format(dayFromKey(busiest.key))) : '—'} (${busiest ? money(busiest.total) : ''})`
    : 'Nothing was spent in this period.';

  // A legend for two series or more; one series is named by the title.
  morph($('chartLegend'), slots.length > 1
    ? html`${slots.map((slot) => html`<li>${keyHtml(slot)}<span>${slotName(slot)}</span></li>`)}`
    : '');

  // As wide as its card's content and no wider: a floor of 280 ran 24px past
  // the card at 320.
  const width = Math.max(160, box.clientWidth || 600);
  const height = px(box, '--so-chart-height', 240);
  const gapPx = px(box, '--so-chart-gap', 2);
  const radius = px(box, '--ds-radius-xs', 4);
  const { top, step } = niceTicks(peak);
  const ticks = [];
  for (let v = 0; v <= top + step / 2; v += step) ticks.push(v);
  // The labels' column is as wide as its longest label: a step of a
  // twentieth of a cent ("$0.0005") is wider than one of dollars.
  const widest = Math.max(...ticks.map((v) => axisMoney(v, step).length));
  const m = { top: 12, right: 8, bottom: 26, left: Math.max(52, 12 + Math.ceil(widest * 6.5)) };
  const plotW = width - m.left - m.right;
  const plotH = height - m.top - m.bottom;
  const y = (v) => m.top + plotH - (v / top) * plotH;
  const band = plotW / Math.max(1, list.length);
  const barW = Math.max(2, Math.min(24, band - 2));

  const root = svg('svg', { width, height, viewBox: `0 0 ${width} ${height}`, role: 'img',
    'aria-label': `${$('chartTitle').textContent}: ${$('chartSub').textContent}` });
  const hover = svg('rect', { x: 0, y: m.top, width: band, height: plotH, class: 'so-chart__hover', visibility: 'hidden' });
  root.append(hover);
  for (const v of ticks) {
    const yy = Math.round(y(v)) + 0.5;
    root.append(svg('line', { x1: m.left, x2: width - m.right, y1: yy, y2: yy, class: v === 0 ? 'so-chart__base' : 'so-chart__grid' }));
    const label = svg('text', { x: m.left - 8, y: yy + 4, 'text-anchor': 'end', class: 'so-chart__axis' });
    label.textContent = axisMoney(v, step);
    root.append(label);
  }

  // Sparse x labels: about one per 70px, always the first and the last.
  const every = Math.max(1, Math.ceil(70 / band));
  let labelled = -every;
  list.forEach((b, i) => {
    const cx = m.left + band * i + band / 2;
    if (b.total > 0) {
      let base = y(0);
      const present = slots.filter((s) => b.slots.get(s) > 0);
      present.forEach((slot, j) => {
        const h = (b.slots.get(slot) / top) * plotH;
        const gap = j > 0 ? gapPx : 0; // the surface gap between stacked segments
        const segH = Math.max(0, h - gap);
        const yTop = base - h;
        const last = j === present.length - 1;
        if (segH > 0) {
          const series = seriesKey(slot);
          root.append(last
            ? svg('path', { d: topRounded(cx - barW / 2, yTop, barW, segH, radius), class: 'so-chart__bar', 'data-series': series })
            : svg('rect', { x: cx - barW / 2, y: yTop, width: barW, height: segH, class: 'so-chart__bar', 'data-series': series }));
        }
        base = yTop;
      });
    }
    if (i % every === 0 || i === list.length - 1) {
      // The last one only where it does not run into the one before it.
      if (i % every !== 0 && i - labelled < every) return;
      labelled = i;
      const d = dayFromKey(b.key);
      const label = svg('text', { x: cx, y: height - 8, 'text-anchor': 'middle', class: 'so-chart__axis' });
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
    morph(tip, html`<p class="so-chart-tip__title">${title}</p><p class="so-chart-tip__total">${money(b.total)}</p>${
      lines.slice(0, 6).map(([model, cost]) => html`<p class="so-chart-tip__row">${keyHtml(seriesOf(model))}<span class="so-chart-tip__name">${modelName(model)}</span><span class="so-chart-tip__value">${money(cost)}</span></p>`)}${
      lines.length > 6 ? html`<p class="so-chart-tip__more">and ${lines.length - 6} more</p>` : ''}`);
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
    const hit = svg('rect', { x: m.left + band * i, y: m.top, width: band, height: plotH, class: 'so-chart__hit' });
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

// table draws one breakdown as a DS table. A row that filters (`onPick`) is
// an interactive row whose name is its button, pressed while that filter is
// on; a row with `href` links to its session.
function table(id, groups, { column, name, sub, onPick, picked, href, limit = LIMIT, sort = 'cost' }) {
  const box = $(id);
  const total = groups.reduce((s, g) => s + g.cost, 0);
  if (sort === 'cost') groups.sort((a, b) => b.cost - a.cost);
  else groups.sort(sort);
  if (!groups.length || total <= 0) {
    morph(box, html`<p class="ds-muted">Nothing in this period.</p>`);
    box.onclick = null;
    return;
  }
  const open = state.expanded.has(id);
  const shown = open ? groups : groups.slice(0, limit);
  const peak = groups.reduce((m, g) => Math.max(m, g.cost), 0);
  // A row that opens its chat is pressed whole, not by its 18px of text: the
  // link stretches over its row (ds-table__row-link) in DS's interactive
  // table, whose rows without a link - a session deleted, a conversation
  // from outside Socrates - stay plain.
  const linked = href ? shown.filter((g) => href(g)).length : 0;
  const rowsOpen = onPick || (linked > 0 && linked === shown.length);
  const rows = shown.map((g, i) => {
    const used = g.input + g.output + g.cacheRead + g.cacheWrite;
    const label = name(g);
    const detail = sub ? sub(g) : '';
    const target = href ? href(g) : '';
    const on = !!(picked && picked(g));
    const title = onPick ? (on ? 'Show everything again' : 'Show only this') : label;
    const lead = onPick
      ? html`<button class="ds-link-btn ds-table__row-link" type="button" data-pick="${i}" aria-pressed="${on ? 'true' : 'false'}" title="${title}">${on ? raw(icon('check', 16)) : ''}${label}</button>`
      : target
        ? html`<a class="ds-link-btn ds-table__row-link" href="${target}">${label}</a>`
        : html`<span>${label}</span>`;
    return html`<tr data-ds-key="${'k:' + g.key}"${on ? raw(' class="ds-table__row--selected"') : ''}>
      <td class="ds-col--text so-costs-name">${lead}${detail ? html`<small class="so-costs-sub">${detail}</small>` : ''}<span class="ds-meter so-costs-bar" aria-hidden="true" style="${'--ds-share: ' + (peak ? (g.cost / peak).toFixed(4) : 0)}"></span></td>
      <td class="ds-num"><strong>${money(g.cost)}</strong></td>
      <td class="ds-num ds-col--secondary">${pct(g.cost / total)}</td>
      <td class="ds-num ds-col--secondary" title="${`${g.input.toLocaleString()} in · ${g.output.toLocaleString()} out · ${g.cacheRead.toLocaleString()} cache read · ${g.cacheWrite.toLocaleString()} cache write`}">${tokens(used)}</td>
      <td class="ds-num ds-col--secondary">${g.requests.toLocaleString()}</td>
    </tr>`;
  });
  morph(box, html`<div class="ds-table-wrap"><table class="ds-table ds-table--dense so-costs-table${rowsOpen || linked ? ' ds-table--interactive' : ''}">
    <thead><tr>
      <th scope="col">${column}</th><th scope="col" class="ds-num">Cost</th><th scope="col" class="ds-num ds-col--secondary">Share</th>
      <th scope="col" class="ds-num ds-col--secondary">Tokens</th><th scope="col" class="ds-num ds-col--secondary">Requests</th>
    </tr></thead>
    <tbody>${rows}</tbody>
  </table></div>${groups.length > limit
    ? html`<div class="ds-more"><button class="ds-btn ds-btn--text" type="button" data-more>${open ? 'Show fewer' : `Show all ${groups.length}`}</button></div>`
    : ''}`);
  box.onclick = (ev) => {
    const pick = ev.target.closest('[data-pick]');
    if (pick && onPick) { onPick(shown[Number(pick.dataset.pick)]); return; }
    if (ev.target.closest('[data-more]')) {
      if (open) state.expanded.delete(id); else state.expanded.add(id);
      render();
    }
  };
}

function drawTables(filtered) {
  const f = state.filters;
  table('tProject', groupBy(filtered, (r) => r.conv.project), {
    column: 'Project',
    name: (g) => projectLabel(g.key),
    sub: (g) => (g.key === MORE ? 'folders Socrates made for one session' : g.key),
    onPick: (g) => toggleFilter('project', g.key), picked: (g) => f.project !== '' && f.project === g.key,
  });
  table('tModel', groupBy(filtered, (r) => r.model), {
    column: 'Model',
    name: (g) => modelName(g.key),
    sub: (g) => [modelName(g.key) !== g.key ? g.key : '',
      g.unpriced && g.cost === 0 ? `${g.unpriced.toLocaleString()} ${g.unpriced === 1 ? 'request' : 'requests'} without a known price` : '']
      .filter(Boolean).join(' · '),
    onPick: (g) => toggleFilter('model', g.key), picked: (g) => f.model !== '' && f.model === g.key,
  });
  table('tMonthly', groupBy(filtered, (r) => r.month), {
    column: 'Month',
    name: (g) => monthLong.format(dayFromKey(g.key)),
    sort: (a, b) => (a.key < b.key ? 1 : -1), limit: 12,
  });
  table('tDay', groupBy(filtered, (r) => r.day), {
    column: 'Day',
    name: (g) => dayLong.format(dayFromKey(g.key)),
    sort: (a, b) => (a.key < b.key ? 1 : -1), limit: 14,
  });
  table('tHarness', groupBy(filtered, (r) => r.conv.harness), {
    column: 'Program',
    name: (g) => HARNESS[g.key] || g.key,
    onPick: (g) => toggleFilter('harness', g.key), picked: (g) => f.harness !== '' && f.harness === g.key,
  });
  table('tProfile', groupBy(filtered, (r) => r.conv.profile), {
    column: 'Account',
    name: (g) => accountName(g.key),
    onPick: (g) => toggleFilter('profile', g.key), picked: (g) => f.profileSet && f.profile === g.key,
  });
  // A chat is one row, whatever accounts it ran on: its ledgers are kept
  // per account (the account in the key), and a chat that went on with
  // another one at a limit was two rows of the same name, date and link.
  // What each account spent is the Account table's.
  const chatOf = (r) => (r.conv.session_id ? r.conv.harness + '/' + r.conv.session_id : r.conv.key);
  const lastAt = new Map();
  for (const r of filtered) {
    const key = chatOf(r);
    lastAt.set(key, Math.max(lastAt.get(key) || 0, r.conv.last_at || 0));
  }
  table('tConv', groupBy(filtered, chatOf), {
    column: 'Conversation',
    name: (g) => convLabel(g.sample.conv),
    sub: (g) => {
      const c = g.sample.conv;
      const at = lastAt.get(g.key);
      const when = at ? dayShort.format(new Date(at)) : '';
      return [HARNESS[c.harness] || c.harness, projectLabel(c.project), when, c.live ? '' : (c.session_id ? 'session deleted' : 'not started from Socrates')]
        .filter(Boolean).join(' · ');
    },
    // #open=: a page that starts on a bare #<id> starts on Home (routeOf).
    href: (g) => (g.sample.conv.live ? '/#open=' + encodeURIComponent(g.sample.conv.session_id) : ''),
  });
}

// ------------------------------------------------------------------ page

function render() {
  if (!state.data) return;
  state.names = modelNames(state.rows);
  state.accounts = accountNames(state.rows);
  $('costsLoading').hidden = true;
  const none = state.rows.length === 0;
  $('costsNone').hidden = !none;
  $('costsBody').hidden = none;
  $('costsCsv').disabled = true;
  if (none) return;
  drawRange();
  drawFilters();
  const range = bounds(state.range, state.rows);
  const inRange = state.rows.filter((r) => r.day >= range.from && r.day <= range.to && matches(r));
  const days = daysBetween(range.from, range.to).length;
  drawTiles(inRange, days);
  const empty = inRange.length === 0 || inRange.every((r) => r.cost <= 0 && r.requests === 0);
  $('costsEmpty').hidden = !empty;
  $('costsData').hidden = empty;
  if (!empty) {
    drawChart(inRange, range);
    drawTables(inRange);
  } else {
    $('chartTip').hidden = true;
  }
  $('costsCsv').disabled = inRange.length === 0;
  $('costsCsv').onclick = () => downloadCsv(inRange, range);
}

function drawState() {
  const data = state.data;
  const line = $('costsState');
  if (!data) return;
  if (data.scanning) {
    morph(line, html`<span class="ds-status ds-status--running"><span class="ds-spinner ds-status__spinner" aria-hidden="true"></span><span>Reading transcripts</span></span>`);
  } else {
    morph(line, data.scanned_at ? html`Updated ${clock.format(new Date(data.scanned_at))}` : '');
  }
  $('costsNote').textContent = 'Estimated at the published API prices from the transcripts Claude Code, Codex and OpenCode keep '
    + '(OpenCode prices its answers itself). A subscription is billed differently: this is what the same work would cost on the API. '
    + 'Days are this device\'s days. The ledger is part of the data directory, and of its backup when one is set up.';
  $('costsNoneText').textContent = data.scanning
    ? 'Socrates is reading the transcripts on this machine; what they cost shows up here in a moment.'
    : 'They show up here once Claude Code, Codex or OpenCode has had a conversation on this machine.';
}

// notice says that the ledger could not be read, with a way to try again.
function notice(text) {
  if (!text) { morph($('notice'), ''); return; }
  morph($('notice'), html`<div class="ds-alert ds-alert--error" role="alert">${raw(icon('alertCircle', 20))}<span class="ds-alert__text">${text}</span><button class="ds-btn ds-btn--text ds-btn--sm" type="button" data-retry>Try again</button></div>`);
}

async function load() {
  if (state.loading) return;
  state.loading = true;
  clearTimeout(state.timer);
  try {
    const data = await api('/api/costs');
    state.data = data;
    state.rows = decode(data);
    notice('');
    drawState();
    render();
  } catch (err) {
    notice(errorMessage(err));
    if (!state.data) $('costsLoading').hidden = true;
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
  const lines = [['date', 'project', 'folder', 'program', 'account', 'model', 'conversation', 'cost_usd',
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
$('notice').addEventListener('click', (ev) => { if (ev.target.closest('[data-retry]')) load(); });
let resizeFrame = 0;
new ResizeObserver(() => {
  cancelAnimationFrame(resizeFrame);
  resizeFrame = requestAnimationFrame(() => { if (state.data) render(); });
}).observe($('chart'));
document.addEventListener('pointerdown', (ev) => {
  if (!ev.target.closest('#chart') && !ev.target.closest('#chartTip')) $('chartTip').hidden = true;
});
onWake(() => load());
// The catalogue names the models; the page is drawn again once it is in (it
// is usually there at once, from this browser's copy).
harnesses.load().then(() => render()).catch(() => { /* the ids stay */ });
document.addEventListener('visibilitychange', () => { if (!document.hidden) load(); });
load();
