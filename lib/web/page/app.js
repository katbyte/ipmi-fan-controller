// The page reads /api/state every few seconds, and /api/history for the rounds since, and draws them: a section for each BMC with its zones, its
// fans and the temperatures they follow, charted, one at a time, and the temperatures no zone follows in one of their own. A zone's curves are
// changed on its chart in a draft of its own, which the next read does not overwrite, and saved into the settings file with Save; holds and the
// whole settings file go straight to the program.
'use strict';

const $ = (sel, el = document) => el.querySelector(sel);
const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]);
const deg = (c) => (c == null ? '–' : c.toFixed(1));
const pct = (d) => (d == null ? '–' : d + '%');

// when is a time as the clock on the wall says it, with the day too when it is not today.
function when(t) {
  const d = new Date(t);
  const time = d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
  return d.toDateString() === new Date().toDateString() ? time : d.toLocaleDateString([], { month: 'short', day: 'numeric' }) + ' ' + time;
}
const clock = (t) => new Date(t).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });

let token = '';
try { token = localStorage.getItem('ipmi-fan-controller-token') || ''; } catch { /* the page works without remembering it */ }

// span is how many minutes back the charts go
let span = 60;
try { span = Number(localStorage.getItem('ipmi-fan-controller-span')) || 60; } catch { /* an hour, then */ }

// machine is the BMC whose section is shown, '' for the temperatures no zone follows, or null for the first
let machine = null;
try { machine = localStorage.getItem('ipmi-fan-controller-machine'); } catch { /* the first, then */ }

let state = null;
// hist is what the charts draw, as /api/history sends it: each round's time, and each series' value at it, null where it had none
let hist = { started: null, round: 0, at: [], series: {} };
// rows is each zone's row, kept so an edit under way survives the next read, and systems each BMC's section
const rows = new Map();
const systems = new Map();

async function api(method, path, body) {
  const opts = { method, headers: {} };
  if (body !== undefined) {
    opts.headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(body);
  }
  if (token) opts.headers.Authorization = 'Bearer ' + token;

  const r = await fetch(path, opts);
  const data = await r.json().catch(() => ({}));
  if (r.status === 401) $('#tokenbox').hidden = false;
  if (!r.ok) throw new Error((data.errors || [r.statusText]).join('\n'));

  return data;
}

// the theme, dark unless this browser was told otherwise

$('#theme').onclick = () => {
  const h = document.documentElement;
  h.dataset.theme = h.dataset.theme === 'dark' ? 'light' : 'dark';
  try { localStorage.setItem('ipmi-fan-controller-theme', h.dataset.theme); } catch { /* kept for this visit only */ }
};

// tabs: the overview, corrections and settings; the addresses of the overview's old parts lead to it

const tabs = ['overview', 'corrections', 'settings'];

function showTab() {
  let tab = (location.hash || '#overview').slice(1);
  if (!tabs.includes(tab)) tab = 'overview';
  for (const a of document.querySelectorAll('.tabs a')) a.classList.toggle('on', a.dataset.tab === tab);
  for (const id of tabs) $('#' + id).hidden = id !== tab;
  $('#span').hidden = tab !== 'overview';
  if (tab === 'settings') loadSettings();
}
window.addEventListener('hashchange', showTab);

function showSpan() {
  for (const b of document.querySelectorAll('#span button')) b.classList.toggle('on', Number(b.dataset.min) === span);
}
for (const b of document.querySelectorAll('#span button')) {
  b.onclick = () => {
    span = Number(b.dataset.min);
    try { localStorage.setItem('ipmi-fan-controller-span', String(span)); } catch { /* kept for this visit only */ }
    showSpan();
    if (state) drawOverview();
  };
}

// reading

async function refresh() {
  try {
    state = await api('GET', '/api/state');
    $('#trouble').hidden = true;
  } catch (e) {
    $('#trouble').textContent = 'The program is not answering: ' + e.message;
    $('#trouble').hidden = false;
    return;
  }
  try { await loadHistory(); } catch { /* the charts catch up on the next read */ }

  $('#dry').hidden = !state.dry_run;
  $('#updated').textContent = state.at ? 'round ' + state.rounds + ' at ' + when(state.at) : '';
  drawOverview();
  drawCorrections();
}

// loadHistory adds the rounds since the last it has, and starts again when the program has.
async function loadHistory() {
  if (hist.started !== state.started) hist = { started: state.started, round: 0, at: [], series: {} };

  const h = await api('GET', '/api/history?since=' + hist.round);
  const n = hist.at.length;
  for (const t of h.at) hist.at.push(t);
  for (const [name, values] of Object.entries(h.series || {})) {
    if (!hist.series[name]) hist.series[name] = new Array(n).fill(null);
    for (const v of values) hist.series[name].push(v);
  }
  for (const col of Object.values(hist.series)) while (col.length < hist.at.length) col.push(null);
  if (h.rounds.length) hist.round = h.rounds[h.rounds.length - 1];

  // six hours, as the program keeps
  let cut = 0;
  while (cut < hist.at.length && hist.at[cut] < Date.now() - 6 * 3600e3) cut++;
  if (cut) {
    hist.at.splice(0, cut);
    for (const col of Object.values(hist.series)) col.splice(0, cut);
  }
}

// charts

const palette = ['var(--s1)', 'var(--s2)', 'var(--s3)', 'var(--s4)', 'var(--s5)', 'var(--s6)', 'var(--s7)', 'var(--s8)'];
const colour = (i) => palette[i % palette.length];
const latest = (values) => { for (let i = (values?.length ?? 0) - 1; i >= 0; i--) if (values[i] != null) return values[i]; return null; };
const show = (v, unit) => (v == null ? '–' : unit === '°' ? v.toFixed(1) + '°' : Math.round(v) + unit);

// charts keeps what each time chart drew, by its key, for its tooltip
const charts = new Map();

// inSpan is the rounds in the charts' span, thinned to about one a pixel.
function inSpan(width) {
  const to = Date.now(), from = to - span * 60e3;
  let i = 0;
  while (i < hist.at.length && hist.at[i] < from) i++;

  const step = Math.max(1, Math.ceil((hist.at.length - i) / width));
  const idx = [];
  for (let j = i; j < hist.at.length; j += step) idx.push(j);
  if (idx.length && idx[idx.length - 1] !== hist.at.length - 1) idx.push(hist.at.length - 1);

  return { from, to, idx };
}

// timeChart draws lines over the charts' span, width wide: each {label, values, colour, dash, right}. left and right are the axes, {unit, min, max,
// step}, and right is left out for a chart with one. sort lists the lines hottest first in the tooltip.
function timeChart(key, lines, left, right, sort, width) {
  const W = Math.max(240, Math.round(width || 480)), H = 150, L = 36, R = right ? 36 : 8, T = 8, B = 18;
  const { from, to, idx } = inSpan(W - L - R);
  if (!idx.length) return '<p class="empty">Nothing yet: the charts fill in as rounds go by.</p>';

  const x = (t) => L + ((t - from) / (to - from)) * (W - L - R);
  const axis = (a, onRight) => {
    if (!a) return null;
    const vs = lines.filter((l) => !!l.right === onRight).flatMap((l) => idx.map((i) => l.values?.[i])).filter((v) => v != null);
    let lo = a.min ?? (vs.length ? Math.min(...vs) : 0);
    let hi = a.max ?? (vs.length ? Math.max(...vs) : 100);
    if (a.min == null) lo = Math.floor((lo - a.step / 4) / a.step) * a.step;
    if (a.max == null) hi = Math.ceil((hi + a.step / 4) / a.step) * a.step;
    if (hi <= lo) hi = lo + a.step;
    return { ...a, lo, hi, y: (v) => H - B - ((v - lo) / (hi - lo)) * (H - B - T) };
  };
  const la = axis(left, false), ra = axis(right, true);

  let grid = '';
  for (const f of [0, 0.5, 1]) {
    const v = la.lo + f * (la.hi - la.lo);
    grid += `<line class="grid" x1="${L}" x2="${W - R}" y1="${la.y(v)}" y2="${la.y(v)}"/><text x="${L - 4}" y="${la.y(v) + 3}" text-anchor="end">${Math.round(v)}${la.unit === ' rpm' ? '' : la.unit}</text>`;
    if (ra) {
      const w = ra.lo + f * (ra.hi - ra.lo);
      grid += `<text x="${W - R + 4}" y="${la.y(v) + 3}">${Math.round(w)}${ra.unit}</text>`;
    }
  }
  const every = { 15: 5, 60: 15, 180: 30, 360: 60 }[span] * 60e3 || 15 * 60e3;
  for (let t = Math.ceil(from / every) * every; t <= to; t += every) grid += `<text x="${x(t)}" y="${H - 4}" text-anchor="middle">${clock(t)}</text>`;

  const paths = lines.map((l) => {
    const a = l.right ? ra : la;
    let d = '', pen = false;
    for (const i of idx) {
      const v = l.values?.[i];
      if (v == null) { pen = false; continue; }
      d += (pen ? 'L' : 'M') + x(hist.at[i]).toFixed(1) + ',' + a.y(Math.min(Math.max(v, a.lo), a.hi)).toFixed(1);
      pen = true;
    }
    return d ? `<path class="tline${l.dash ? ' dash' : ''}" d="${d}" style="stroke:${l.colour}"/>` : '';
  }).join('');

  charts.set(key, { lines, from, to, W, L, R, left: la, right: ra, sort });

  return `<svg viewBox="0 0 ${W} ${H}" width="${W}" height="${H}" data-chart="${esc(key)}" role="img" aria-label="chart">${grid}${paths}</svg>`;
}

// plot draws a time chart into an element, as wide as the element is, so its text stays the size of the page's.
function plot(el, key, lines, left, right, sort) {
  el.innerHTML = timeChart(key, lines, left, right, sort, el.clientWidth);
}

let resizing = 0;
window.addEventListener('resize', () => {
  clearTimeout(resizing);
  resizing = setTimeout(() => state && drawOverview(), 150);
});

// legend names a chart's lines, with each one's latest value.
function legend(lines, left, right) {
  return `<div class="legend">${lines.map((l) => `<span><i class="${l.dash ? 'dash' : ''}" style="--c:${l.colour}"></i>${esc(l.label)} <b>${show(latest(l.values), (l.right ? right : left).unit)}</b></span>`).join('')}</div>`;
}

// the tooltip: every line's value at the round nearest the pointer
document.addEventListener('mousemove', (e) => {
  const tip = $('#tip');
  const svg = e.target.closest?.('svg[data-chart]');
  const c = svg && charts.get(svg.dataset.chart);
  if (!c || !hist.at.length) { tip.hidden = true; return; }

  const box = svg.getBoundingClientRect();
  const sx = ((e.clientX - box.left) / box.width) * c.W;
  const t = c.from + ((sx - c.L) / (c.W - c.L - c.R)) * (c.to - c.from);
  let lo = 0, hi = hist.at.length - 1;
  while (lo < hi) { const mid = (lo + hi) >> 1; if (hist.at[mid] < t) lo = mid + 1; else hi = mid; }
  if (lo > 0 && Math.abs(hist.at[lo - 1] - t) < Math.abs(hist.at[lo] - t)) lo--;
  if (hist.at[lo] < c.from || sx < c.L || sx > c.W - c.R) { tip.hidden = true; return; }

  let found = c.lines.map((l) => ({ l, v: l.values?.[lo] })).filter((r) => r.v != null);
  if (c.sort) found.sort((a, b) => b.v - a.v);
  const more = found.length - 14;
  found = found.slice(0, 14);
  tip.innerHTML = `<b>${esc(when(hist.at[lo]))}</b>${found.map((r) => `<div class="row"><span style="--c:${r.l.colour}">${esc(r.l.label)}</span><span>${show(r.v, (r.l.right ? c.right : c.left).unit)}</span></div>`).join('')}${more > 0 ? `<div class="row dim">and ${more} more</div>` : ''}`;
  tip.hidden = false;
  const left = e.clientX + 16 + tip.offsetWidth > window.innerWidth ? e.clientX - 16 - tip.offsetWidth : e.clientX + 16;
  tip.style.left = left + 'px';
  tip.style.top = Math.min(e.clientY + 16, window.innerHeight - tip.offsetHeight - 8) + 'px';

  const cx = c.L + ((hist.at[lo] - c.from) / (c.to - c.from)) * (c.W - c.L - c.R);
  let cross = svg.querySelector('.cross');
  if (!cross) {
    svg.insertAdjacentHTML('beforeend', `<line class="cross" y1="8" y2="132"/>`);
    cross = svg.querySelector('.cross');
  }
  cross.setAttribute('x1', cx);
  cross.setAttribute('x2', cx);
});

// spark is a reading's line over the charts' span, small enough for a table row.
function spark(values) {
  const W = 120, H = 22;
  const { from, to, idx } = inSpan(W);
  const pts = idx.filter((i) => values?.[i] != null);
  if (pts.length < 2) return '';

  const vs = pts.map((i) => values[i]);
  const lo = Math.min(...vs) - 0.5, hi = Math.max(...vs) + 0.5;
  const d = pts.map((i, k) => (k ? 'L' : 'M') + (((hist.at[i] - from) / (to - from)) * W).toFixed(1) + ',' + (H - 2 - ((values[i] - lo) / (hi - lo)) * (H - 4)).toFixed(1)).join('');

  return `<svg class="spark" viewBox="0 0 ${W} ${H}" width="${W}" height="${H}"><path d="${d}"/></svg>`;
}

// the overview

function drawOverview() {
  drawSummary();
  drawSources();
  drawSystems();
  drawTable();
}

// drawSummary is a tile for each zone and the few things worth seeing first.
function drawSummary() {
  const disks = state.readings.filter((r) => r.disk && r.fresh);
  const hottest = disks.reduce((a, r) => (!a || r.celsius > a.celsius ? r : a), null);
  const fans = state.bmcs.flatMap((b) => b.fans || []);
  const stopped = fans.filter((f) => f.stopped).length;
  const fixes = state.zones.reduce((n, z) => n + z.takeovers + z.resets, 0);
  const hour = state.zones.flatMap((z) => z.corrections || []).filter((c) => Date.now() - new Date(c.at) < 3600e3).length;
  const up = state.bmcs.filter((b) => b.up).length;

  $('#summary').innerHTML = state.zones.map((z) => `
    <div class="tile"><div class="v">${pct(z.duty)}</div><div class="k">${esc(z.name)} <span class="badge g ${esc(z.state)}">${esc(z.state)}</span></div><div class="d">${z.follows.map((f) => `${esc(f.name)} ${deg(f.celsius)}°`).join(' · ') || 'follows nothing'}</div></div>`).join('') + `
    <div class="tile"><div class="v">${hottest ? deg(hottest.celsius) + '°' : '–'}</div><div class="k">hottest disk</div><div class="d">${hottest ? esc([hottest.disk.name, hottest.disk.slot ? 'slot ' + hottest.disk.slot : '', (hottest.zones || []).join(', ')].filter(Boolean).join(' · ')) : 'no disks read'}</div></div>
    <div class="tile ${stopped ? 'bad' : ''}"><div class="v">${fans.filter((f) => f.readable && !f.stopped).length}</div><div class="k">fans turning</div><div class="d">${stopped ? stopped + ' stopped' : 'none stopped'}</div></div>
    <div class="tile ${up < state.bmcs.length ? 'bad' : ''}"><div class="v">${up}/${state.bmcs.length}</div><div class="k">BMCs answering</div></div>
    <a class="tile ${fixes ? 'warn' : ''}" href="#corrections"><div class="v">${fixes}</div><div class="k">corrections</div><div class="d">${hour} in the last hour</div></a>`;
}

function drawSources() {
  $('#sources').innerHTML = state.sources.map((s) => `
    <span class="source" title="${esc(s.error || '')}"><b>${esc(s.name)}</b> <span class="badge g ${s.fresh ? 'ok' : 'bad'}">${s.fresh ? 'answering' : 'stale'}</span>${s.unread ? ` <span class="dim" title="${esc(Object.entries(s.unread).map(([id, why]) => id + ': ' + why).join('\n'))}">${Object.keys(s.unread).length} unread</span>` : ''}</span>`).join('');
}

// homeOf is the section a reading is shown in: the BMC of the first zone that follows it, the BMC it comes from, or this machine's own BMC for its
// chips and the disks in no enclosure, which are inside it. A disk in an enclosure no zone follows could be in any of them, so is shown with the
// temperatures no zone follows.
function homeOf(r, local) {
  const z = r.zones?.length ? state.zones.find((x) => x.name === r.zones[0]) : null;
  if (z) return z.bmc;
  if (r.source.startsWith('ipmi/')) return r.source.slice(5);
  if (local && !r.disk?.enclosure) return local;

  return '';
}

function drawSystems() {
  const root = $('#systems');
  const local = state.bmcs.find((b) => !b.host)?.name;
  const homes = new Map();
  for (const r of state.readings) {
    const h = homeOf(r, local);
    if (!homes.has(h)) homes.set(h, []);
    homes.get(h).push(r);
  }

  const names = state.bmcs.map((b) => b.name);
  for (const z of state.zones) if (!names.includes(z.bmc)) names.push(z.bmc);
  if (homes.has('')) names.push('');

  for (const [name, sec] of systems) {
    if (!names.includes(name)) {
      sec.remove();
      systems.delete(name);
    }
  }
  for (const [name, row] of rows) {
    if (!state.zones.some((z) => z.name === name)) {
      row.el.remove();
      rows.delete(name);
    }
  }

  if (names.length === 0) {
    root.innerHTML = '<p class="empty">Nothing to show yet: no BMC, zone or temperature. Add them in Settings.</p>';
    return;
  }
  root.querySelector('p.empty')?.remove();

  const shown = names.includes(machine) ? machine : names[0];
  $('#machines').innerHTML = names.map((n) => machineButton(n, homes.get(n) || [], n === shown)).join('');

  let prev = null;
  for (const name of names) {
    let sec = systems.get(name);
    if (!sec) {
      sec = document.createElement('section');
      sec.className = 'system';
      sec.innerHTML = '<div class="syshead"></div><div class="zones"></div><div class="systemps"></div>';
      systems.set(name, sec);
    }
    const at = prev ? prev.nextSibling : root.firstChild;
    if (at !== sec) root.insertBefore(sec, at);
    prev = sec;

    sec.hidden = name !== shown;
    if (sec.hidden) continue;

    const b = state.bmcs.find((x) => x.name === name);
    $('.syshead', sec).innerHTML = systemHead(name, b);

    const zones = $('.zones', sec);
    for (const z of state.zones.filter((x) => x.bmc === name)) {
      let row = rows.get(z.name);
      if (!row) {
        row = makeRow(z);
        rows.set(z.name, row);
      }
      if (row.el.parentNode !== zones) zones.appendChild(row.el);
      row.zone = z;
      if (!row.dirty && !row.drag && !(state.rounds < row.keep)) row.draft = draftOf(z);
      drawRow(row);
    }

    drawTemps($('.systemps', sec), name, homes.get(name) || []);
  }
}

// machineButton picks a section to show: a BMC with its zones' speeds and its hottest disk, marked when it is not answering or has a fan stopped.
function machineButton(name, rs, on) {
  const b = state.bmcs.find((x) => x.name === name);
  const hot = rs.filter((r) => r.disk && r.fresh).reduce((a, r) => Math.max(a, r.celsius), -Infinity);
  const facts = name === '' ? [rs.length + ' temperatures'] : [...state.zones.filter((z) => z.bmc === name).map((z) => pct(z.duty)), hot > -Infinity ? 'disks to ' + deg(hot) + '°' : ''].filter(Boolean);
  const trouble = name === '' || !b || !b.up || (b.fans || []).some((f) => f.stopped);

  return `<button data-machine="${esc(name)}" class="${on ? 'on' : ''}">${trouble ? `<i class="dot ${name === '' ? 'warn' : ''}"></i>` : ''}<b>${name ? esc(name) : 'followed by no zone'}</b> <span class="sub">${esc(facts.join(' · '))}</span></button>`;
}

$('#machines').addEventListener('click', (e) => {
  const b = e.target.closest('button[data-machine]');
  if (!b) return;
  machine = b.dataset.machine;
  try { localStorage.setItem('ipmi-fan-controller-machine', machine); } catch { /* kept for this visit only */ }
  drawOverview();
});

// systemHead is a BMC's name, how it is, what it says it is, and all its fans.
function systemHead(name, b) {
  if (name === '') return `<h2>Followed by no zone <span class="desc">nothing sets fans from these, and which machine an enclosure is in is not known until a zone follows it. Follow one with its buttons, or add a curve for it on a zone's chart.</span></h2>`;
  if (!b) return `<h2>${esc(name)} <span class="badge g bad">not answering</span></h2>`;

  const facts = [esc(b.maker), b.device.firmware ? 'firmware ' + esc(b.device.firmware) : '', b.device.product ? 'product 0x' + b.device.product.toString(16) : '', 'fan mode ' + esc(b.mode || '–'), b.retakes ? `readied again ${b.retakes} times` : ''].filter(Boolean);
  const fans = (b.fans || []).map((f) => `<span class="chip ${f.stopped ? 'stopped' : f.readable ? '' : 'none'}" title="zone ${f.zone}">${esc(f.name)} ${f.stopped ? 'STOPPED' : f.readable ? Math.round(f.rpm) : '–'}</span>`).join('');

  return `<h2>${esc(name)} <span class="desc">${esc(b.host || 'this machine')}</span> <span class="badge g ${b.up ? 'ok' : 'bad'}">${b.up ? 'answering' : 'not answering'}</span>${b.tried ? '' : ' <span class="badge g warn">not yet tried on a real BMC</span>'} <span class="facts">${facts.join(' · ')}</span></h2>
    ${b.error ? `<p class="problem">${esc(b.error)}</p>` : ''}
    ${b.made_by && b.made_by !== b.maker ? `<p class="problem">set up as ${esc(b.maker)}, but it says ${esc(b.made_by)} made it</p>` : ''}
    ${fans ? `<div class="chips"><span class="dim">all its fans, rpm</span>${fans}</div>` : ''}`;
}

// zones

// draftOf is a zone's limits and curves as the page changes them: each follow keeps its number in the settings file, which one added has none of.
function draftOf(z) {
  return { floor: z.floor, ceiling: z.ceiling, failsafe: z.failsafe, follows: z.follows.map((f, i) => ({ at: i, name: f.name, sensors: f.sensors, by: f.by, curve: f.curve.map((p) => ({ ...p })) })) };
}

function makeRow(z) {
  const el = document.createElement('article');
  el.className = 'panel zone';
  el.innerHTML = `
    <h3 class="zhead">${esc(z.name)} <span class="desc"></span><span class="val badge g state"></span></h3>
    <div class="zgrid">
      <div class="zmain">
        <div class="live"></div>
        <div class="hold row">
          <label>at <input class="hold-duty" type="number" min="0" max="100" value="${z.floor}">%</label>
          <select class="hold-for">
            <option value="30">for 30 minutes</option><option value="60">for an hour</option><option value="120" selected>for 2 hours</option>
            <option value="240">for 4 hours</option><option value="480">for 8 hours</option><option value="0">until let go</option>
          </select>
          <button class="quiet">Hold quiet</button>
          <button class="full danger">Full speed</button>
          <button class="release">Let go</button>
          <span class="hold-msg note"></span>
        </div>
        <details class="edit"><summary>Its limits, and the curve as numbers</summary><div class="editor"></div></details>
        <details class="find"><summary>Find out which disks it cools</summary>
          <p class="note">Runs this zone flat out for a while, then shows how much every disk cooled, enclosure by enclosure. Disks take minutes to cool, so 15 is a good start. It is loud while it runs, and the zone goes back to its curve after.</p>
          <div class="row">
            <select class="probe-for"><option value="10">for 10 minutes</option><option value="15" selected>for 15 minutes</option><option value="30">for 30 minutes</option></select>
            <button class="probe-start">Start the test</button>
            <span class="probe-msg note"></span>
          </div>
        </details>
      </div>
      <div class="zchart fans"></div>
      <div class="zchart speed"></div>
      <div class="zchart curves">
        <div class="title"></div>
        <div class="row curvebar">
          <select class="curve-pick" aria-label="the curve to change"></select>
          <button class="curve-drop" title="takes this curve away from the zone, once saved">take it away</button>
          <button class="curve-save on">Save</button>
          <button class="curve-reset">Put back</button>
          <span class="edit-msg note"></span>
        </div>
        <div class="legend picks"></div>
        <div class="curvebox"></div>
        <p class="note">Click to add a point, drag one to move it, and double-click or right-click one to take it away. The zone runs at whichever curve asks for the most, between its floor and ceiling.</p>
        <ul class="errors curve-errors"></ul>
      </div>
    </div>
    <div class="probebox"></div>`;

  const row = { el, zone: z, dirty: false, draft: draftOf(z), pick: 0, drag: null, range: null, geo: null };
  const msg = (t) => { $('.hold-msg', el).textContent = t; };
  const minutes = () => Number($('.hold-for', el).value);
  const zonePath = () => '/api/zones/' + encodeURIComponent(row.zone.name);

  $('.quiet', el).onclick = () => api('POST', zonePath() + '/hold', { duty: Number($('.hold-duty', el).value), minutes: minutes() }).then(() => { msg(''); refresh(); }, (e) => msg(e.message));
  $('.full', el).onclick = () => api('POST', zonePath() + '/hold', { full: true, minutes: minutes() }).then(() => { msg(''); refresh(); }, (e) => msg(e.message));
  $('.release', el).onclick = () => api('DELETE', zonePath() + '/hold').then(() => { msg(''); refresh(); }, (e) => msg(e.message));
  $('.probe-start', el).onclick = () => api('POST', zonePath() + '/probe', { minutes: Number($('.probe-for', el).value) }).then(() => { $('.probe-msg', el).textContent = ''; refresh(); }, (e) => { $('.probe-msg', el).textContent = e.message; });

  wireCurves(row);
  drawEditor(row);

  return row;
}

function drawRow(row) {
  const z = row.zone;
  const el = row.el;
  $('.zhead .desc', el).textContent = 'zone ' + z.zone + ' · ' + (z.fans || []).length + ' fans';
  const badge = $('.badge.state', el);
  badge.className = 'val badge g state ' + z.state;
  badge.textContent = z.state + (state.dry_run ? ' (dry run)' : '');

  const fixes = z.takeovers + z.resets;
  const last = z.corrections?.length ? z.corrections[z.corrections.length - 1] : null;
  const follows = z.follows.map((f) => `
    <tr><td>${esc(f.name)}</td><td class="num">${deg(f.celsius)}°</td><td>${f.problem ? `<span class="problem">${esc(f.problem)}</span>` : esc(f.hottest || '') + (f.readings > 1 ? ` <span class="dim">of ${f.readings}</span>` : '')}</td><td class="num">${pct(f.wants)}</td></tr>`).join('');

  $('.live', el).innerHTML = `
    <div class="tiles">
      <div class="tile"><div class="v">${pct(z.duty)}</div><div class="k">set now</div></div>
      <div class="tile"><div class="v">${pct(z.want)}</div><div class="k">${state.dry_run ? 'would set' : 'wanted'}</div></div>
      <div class="tile"><div class="v">${z.floor}–${z.ceiling}</div><div class="k">floor–ceiling %</div></div>
      <a class="tile ${fixes ? 'warn' : ''}" href="#corrections" title="times it was found at another speed than it was set to, and set again"><div class="v">${fixes}</div><div class="k">corrected</div><div class="d">${last ? 'last at ' + esc(when(last.at)) : 'never'}</div></a>
    </div>
    <p class="why">${esc(z.why)}</p>
    <table><thead><tr><th>Follows</th><th class="num">°C</th><th>Hottest</th><th class="num">Asks</th></tr></thead><tbody>${follows}</tbody></table>`;
  $('.release', el).disabled = !z.hold;

  const rpm = { unit: ' rpm', step: 500 };
  const fans = (z.fans || []).map((name, i) => ({ label: name, values: hist.series[`fan/${z.bmc}/${name}`], colour: colour(i) }));
  $('.zchart.fans', el).innerHTML = `<div class="title">Its fans <span class="dim">rpm</span></div>${legend(fans, rpm)}<div class="plot">${fans.length ? '' : '<p class="empty">No fans are known to be in this zone.</p>'}</div>`;
  if (fans.length) plot($('.zchart.fans .plot', el), 'fans/' + z.name, fans, rpm);

  const percent = { unit: '%', min: 0, max: 100 };
  const celsius = { unit: '°', step: 5 };
  const speed = [
    { label: 'set', values: hist.series['duty/' + z.name], colour: 'var(--s1)' },
    { label: 'wanted', values: hist.series['want/' + z.name], colour: 'var(--muted)', dash: true },
    ...z.follows.map((f, i) => ({ label: f.name, values: hist.series[`follow/${z.name}/${f.name}`], colour: colour(i + 1), right: true })),
  ];
  $('.zchart.speed', el).innerHTML = `<div class="title">Its speed <span class="dim">%</span>, and what it follows <span class="dim">°C</span></div>${legend(speed, percent, celsius)}<div class="plot"></div>`;
  plot($('.zchart.speed .plot', el), 'speed/' + z.name, speed, percent, celsius);

  drawCurves(row);
  $('.probebox', el).innerHTML = state.probe && state.probe.zone === z.name ? probePanel(state.probe, z) : '';
}

// curves: every curve of a zone on one chart, the one picked on top with its points to drag

const clamp = (v, lo, hi) => Math.min(Math.max(v, lo), hi);
const byCelsius = (a, b) => a.celsius - b.celsius;

// the curves a curve added on the page starts from, to be changed: a disk's as the settings file starts one, quiet while cool and flat out before
// the 60° most disks are rated for, and anything else's
const diskCurve = [{ celsius: 40, duty: 30 }, { celsius: 50, duty: 45 }, { celsius: 58, duty: 100 }];
const chipCurve = [{ celsius: 50, duty: 30 }, { celsius: 70, duty: 60 }, { celsius: 85, duty: 100 }];

// matches says whether a reading is one of a follow's sensors, as the program matches them: * is any part of a name between slashes.
const globs = new Map();
function matches(sensors, id) {
  return sensors.some((p) => {
    if (!globs.has(p)) globs.set(p, new RegExp('^' + p.replace(/[.+^${}()|\\]/g, '\\$&').replace(/\*/g, '[^/]*').replace(/\?/g, '[^/]') + '$'));
    return globs.get(p).test(id);
  });
}

// dutyAt is the speed a curve asks for at a temperature, as the program works it out: its first point's below it, its last's above, and straight
// lines between.
function dutyAt(curve, t) {
  const c = [...curve].sort(byCelsius);
  if (t <= c[0].celsius) return c[0].duty;
  for (let i = 1; i < c.length; i++) {
    if (t <= c[i].celsius) return c[i - 1].duty + ((t - c[i - 1].celsius) * (c[i].duty - c[i - 1].duty)) / (c[i].celsius - c[i - 1].celsius);
  }

  return c[c.length - 1].duty;
}

// nowOf is the temperature a curve follows now: as the program says for one it has, or the hottest of what one added would follow.
function nowOf(row, f) {
  if (f.at != null) return row.zone.follows[f.at]?.celsius ?? null;
  const vs = state.readings.filter((r) => r.fresh && matches(f.sensors, r.id)).map((r) => r.celsius);

  return vs.length ? Math.max(...vs) : null;
}

// candidates is what a zone could have a curve of its own for: each enclosure's disks, the disks in none, each of this machine's chips, and its
// BMC's sensors, less what its curves follow already.
function candidates(row) {
  const groups = new Map();
  for (const r of state.readings) {
    const [kind, part, ...rest] = r.id.split('/');
    let g = { pattern: r.id, name: rest.join('/') || r.id, label: r.id };
    if (r.disk) g = { pattern: `disk/${part}/*`, name: part === 'none' ? 'disks in no enclosure' : 'disks in ' + part, label: part === 'none' ? 'the disks in no enclosure' : 'the disks in enclosure ' + part, disks: true };
    else if (kind === 'hwmon') g = { pattern: `hwmon/${part}/*`, name: part, label: 'the chip ' + part };
    else if (kind === 'ipmi' && part !== row.zone.bmc) continue;
    else if (kind === 'ipmi') g.label = `${part}'s ${g.name}`;

    if (!groups.has(g.pattern)) groups.set(g.pattern, { ...g, ids: [] });
    groups.get(g.pattern).ids.push(r.id);
  }

  const follows = row.draft.follows;
  return [...groups.values()]
    .filter((g) => !g.ids.every((id) => follows.some((f) => matches(f.sensors, id))))
    .map((g) => ({ ...g, label: g.ids.length > 1 ? `${g.label} (${g.ids.length})` : g.label }));
}

// place adds a point to a curve where it was clicked, at a whole degree, kept between its neighbours' speeds so the curve still rises; a click on a
// degree that has a point already picks that one.
function place(curve, t, v) {
  curve.sort(byCelsius);
  t = Math.round(t);
  const same = curve.find((p) => p.celsius === t);
  if (same) return same;

  const prev = curve.filter((p) => p.celsius < t).at(-1), next = curve.find((p) => p.celsius > t);
  const p = { celsius: t, duty: clamp(Math.round(v), prev ? prev.duty : 0, next ? next.duty : 100) };
  curve.push(p);
  curve.sort(byCelsius);

  return p;
}

// move puts a dragged point under the pointer, at a whole degree and percent, between its neighbours: no cooler or hotter than they are, and no
// slower or faster, so the curve still rises.
function move(curve, p, t, v, range) {
  const j = curve.indexOf(p), prev = curve[j - 1], next = curve[j + 1];
  p.celsius = clamp(Math.round(t), Math.max(range.lo, prev ? prev.celsius + 1 : -40), Math.min(range.hi, next ? next.celsius - 1 : 150));
  p.duty = clamp(Math.round(v), prev ? prev.duty : 0, next ? next.duty : 100);
}

// wireCurves lets a zone's curves be changed on its chart and saved. A drag starts only once the pointer has moved a little, so a click or a double
// click on a point does not nudge it. A double click is told by the time between presses, as the chart is drawn again under the pointer and the
// browser's own double click would not reach it.
function wireCurves(row) {
  const el = row.el;
  const box = $('.curvebox', el);
  const curve = () => row.draft.follows[row.pick]?.curve;
  const changed = () => {
    row.dirty = true;
    $('.edit-msg', el).textContent = '';
    drawCurves(row);
  };

  // where is what is under the pointer: its temperature and speed, and the picked curve's point within reach, if there is one
  const where = (e) => {
    const g = row.geo, svg = $('svg', box);
    if (!g || !svg || !curve()) return null;

    const r = svg.getBoundingClientRect();
    const sx = ((e.clientX - r.left) / r.width) * g.W, sy = ((e.clientY - r.top) / r.height) * g.H;
    let near = null, best = 12;
    for (const p of curve()) {
      const d = Math.hypot(g.x(clamp(p.celsius, g.lo, g.hi)) - sx, g.y(p.duty) - sy);
      if (d < best) [near, best] = [p, d];
    }

    return { near, inside: sx >= g.L && sx <= g.W - g.R && sy >= g.T - 6 && sy <= g.H - g.B + 6, t: g.lo + ((sx - g.L) / (g.W - g.L - g.R)) * (g.hi - g.lo), v: ((g.H - g.B - sy) / (g.H - g.B - g.T)) * 100 };
  };

  box.addEventListener('pointerdown', (e) => {
    const at = e.button === 0 && where(e);
    if (!at) return;

    let p = at.near;
    const again = p && row.press?.p === p && e.timeStamp - row.press.at < 450;
    row.press = { p, at: e.timeStamp };
    if (again) {
      remove(e);
      return;
    }

    const other = e.target.closest('[data-pick]');
    if (!p && other) {
      row.pick = Number(other.dataset.pick);
      drawCurves(row);
      drawEditor(row);
      return;
    }
    if (!p) {
      if (!at.inside) return;
      p = place(curve(), at.t, at.v);
      row.press.p = p;
      row.dirty = true;
      $('.edit-msg', el).textContent = '';
    }

    e.preventDefault();
    curve().sort(byCelsius);
    row.drag = { p, x: e.clientX, y: e.clientY, moving: false };
    row.range = { lo: row.geo.lo, hi: row.geo.hi };
    box.setPointerCapture(e.pointerId);
    drawCurves(row);
  });

  box.addEventListener('pointermove', (e) => {
    const at = where(e);
    if (!at) return;

    if (!row.drag) {
      row.readout = at.inside ? `${Math.round(at.t)}° · ${clamp(Math.round(at.v), 0, 100)}%` : '';
      const out = $('.readout', box);
      if (out) out.textContent = row.readout;
      box.style.cursor = at.near ? 'grab' : at.inside ? 'crosshair' : '';
      return;
    }

    if (!row.drag.moving && Math.hypot(e.clientX - row.drag.x, e.clientY - row.drag.y) < 4) return;
    row.drag.moving = true;
    move(curve(), row.drag.p, at.t, at.v, row.range);
    changed();
  });

  const drop = () => {
    if (!row.drag) return;
    row.drag = null;
    row.range = null;
    drawCurves(row);
    drawEditor(row);
  };
  box.addEventListener('pointerup', drop);
  box.addEventListener('pointercancel', drop);
  box.addEventListener('pointerleave', () => {
    row.readout = '';
    const out = $('.readout', box);
    if (out) out.textContent = '';
  });

  const remove = (e) => {
    const at = where(e);
    if (!at?.near) return;

    e.preventDefault();
    const c = curve();
    if (c.length < 2) {
      $('.edit-msg', el).textContent = 'a curve needs one point at least';
      return;
    }
    c.splice(c.indexOf(at.near), 1);
    changed();
    drawEditor(row);
  };
  box.addEventListener('contextmenu', remove);

  $('.picks', el).addEventListener('click', (e) => {
    const b = e.target.closest('[data-pick]');
    if (!b) return;
    row.pick = Number(b.dataset.pick);
    drawCurves(row);
    drawEditor(row);
  });

  $('.curve-pick', el).addEventListener('change', (e) => {
    const v = e.target.value;
    if (v.startsWith('add:')) {
      const c = candidates(row).find((k) => k.pattern === v.slice(4));
      if (c) {
        const names = row.draft.follows.map((f) => f.name);
        let name = c.name;
        for (let n = 2; names.includes(name); n++) name = `${c.name} ${n}`;
        row.draft.follows.push({ at: null, name, sensors: [c.pattern], by: 'hottest', curve: (c.disks ? diskCurve : chipCurve).map((p) => ({ ...p })) });
        row.pick = row.draft.follows.length - 1;
        row.dirty = true;
      }
    } else {
      row.pick = Number(v);
    }
    drawCurves(row);
    drawEditor(row);
  });

  $('.curve-drop', el).onclick = () => {
    if (row.draft.follows.length < 2) return;
    row.draft.follows.splice(row.pick, 1);
    row.pick = Math.max(0, row.pick - 1);
    changed();
    drawEditor(row);
  };

  $('.curve-reset', el).onclick = () => {
    row.dirty = false;
    row.draft = draftOf(row.zone);
    $('.curve-errors', el).innerHTML = '';
    drawCurves(row);
    drawEditor(row);
  };

  $('.curve-save', el).onclick = async () => {
    const d = row.draft;
    const curves = {}, add = [];
    for (const f of d.follows) {
      const c = [...f.curve].sort(byCelsius);
      if (f.at == null) add.push({ name: f.name, sensors: f.sensors, curve: c });
      else curves[f.at] = c;
    }
    const drop = row.zone.follows.map((_, i) => i).filter((i) => !d.follows.some((f) => f.at === i));

    const errors = $('.curve-errors', el);
    errors.innerHTML = '';
    try {
      await api('PUT', '/api/zones/' + encodeURIComponent(row.zone.name), { floor: d.floor, ceiling: d.ceiling, failsafe: d.failsafe, curves, drop, add });

      // the program takes the change up at its next round, and a round under way may still show what it had: until one after it has been seen,
      // what was saved is shown rather than what the program last said, which could otherwise put a curve taken away back under the next change
      d.follows.forEach((f, i) => { f.at = i; });
      row.dirty = false;
      row.keep = Infinity;
      await refresh();
      row.keep = state.rounds + 2;
      drawCurves(row);
      drawEditor(row);
      $('.edit-msg', el).textContent = 'saved and taken up';
    } catch (err) {
      errors.innerHTML = err.message.split('\n').map((m) => `<li>${esc(m)}</li>`).join('');
    }
  };
}

// drawCurves draws the picker, every curve with what it asks for now, and the chart, as wide as its box. The picker is only drawn again when what
// it offers changes, so it is not closed under the pointer by the next read.
function drawCurves(row) {
  const d = row.draft;
  const el = $('.zchart.curves', row.el);
  row.pick = clamp(row.pick, 0, d.follows.length - 1);

  const cands = candidates(row);
  const opts = `<optgroup label="change the curve for">${d.follows.map((f, i) => `<option value="${i}">${esc(f.name)}${f.at == null ? ' (new)' : ''}</option>`).join('')}</optgroup>${cands.length ? `<optgroup label="add a curve for">${cands.map((c) => `<option value="add:${esc(c.pattern)}">${esc(c.label)}</option>`).join('')}</optgroup>` : ''}`;
  const pick = $('.curve-pick', el);
  if (row.opts !== opts) {
    pick.innerHTML = opts;
    row.opts = opts;
  }
  pick.value = String(row.pick);

  $('.title', el).innerHTML = `Its curves <span class="dim">% for °C${row.dirty ? ', not saved yet' : ''}</span>`;
  $('.curve-drop', el).disabled = d.follows.length < 2;
  $('.curve-save', el).disabled = !row.dirty;
  $('.curve-reset', el).disabled = !row.dirty;

  const nows = d.follows.map((f) => nowOf(row, f));
  const asks = (f, t) => (t == null || !f.curve.length ? '–' : `${deg(t)}° → ${Math.round(clamp(dutyAt(f.curve, t), d.floor, d.ceiling))}%`);
  $('.picks', el).innerHTML = d.follows.map((f, i) => `<span data-pick="${i}" class="${i === row.pick ? 'on' : ''}"><i style="--c:${colour(i + 1)}"></i>${esc(f.name)} <b>${asks(f, nows[i])}</b></span>`).join('');

  const box = $('.curvebox', el);
  box.innerHTML = curveChart(row, nows, box.clientWidth);
}

// curveChart draws every curve of a zone's draft: each with where its temperature is now, the picked one on top with its points and the speed it
// gives once the floor and ceiling have their say shaded under it. It keeps where things are drawn, for the pointer; while a point is dragged the
// temperatures across stay put.
function curveChart(row, nows, width) {
  const d = row.draft;
  const W = Math.max(280, Math.round(width || 420)), H = 220, L = 38, R = 48, T = 14, B = 22;

  let range = row.range;
  if (!range) {
    const temps = [...d.follows.flatMap((f) => f.curve.map((p) => p.celsius)), ...nows.filter((t) => t != null)];
    const lo = temps.length ? Math.floor((Math.min(...temps) - 8) / 5) * 5 : 20;
    range = { lo, hi: Math.max(lo + 30, temps.length ? Math.ceil((Math.max(...temps) + 8) / 5) * 5 : 80) };
  }
  const { lo, hi } = range;
  const x = (t) => L + ((clamp(t, lo, hi) - lo) / (hi - lo)) * (W - L - R);
  const y = (v) => H - B - (v / 100) * (H - B - T);
  row.geo = { W, H, L, R, T, B, lo, hi, x, y };

  const limit = (v) => clamp(v, d.floor, d.ceiling);
  const line = (c, f = (v) => v) => [[lo, c[0].duty], ...c.map((p) => [p.celsius, p.duty]), [hi, c[c.length - 1].duty]].map(([t, v]) => `${x(t).toFixed(1)},${y(f(v)).toFixed(1)}`).join(' ');

  let grid = '';
  for (const v of [0, 25, 50, 75, 100]) grid += `<line class="grid" x1="${L}" x2="${W - R}" y1="${y(v)}" y2="${y(v)}"/><text x="${L - 4}" y="${y(v) + 3}" text-anchor="end">${v}%</text>`;
  const step = hi - lo > 60 ? 10 : 5;
  for (let t = Math.ceil(lo / step) * step; t <= hi; t += step) grid += `<text x="${x(t)}" y="${H - 6}" text-anchor="middle">${t}°</text>`;
  for (const [name, v] of [['floor', d.floor], ['ceiling', d.ceiling]]) grid += `<line class="limit" x1="${L}" x2="${W - R}" y1="${y(v)}" y2="${y(v)}"/><text x="${W - R + 4}" y="${y(v) + (name === 'floor' ? 10 : -3)}">${name}</text>`;

  const others = d.follows.map((f, i) => {
    if (i === row.pick || !f.curve.length) return '';
    const c = [...f.curve].sort(byCelsius);
    return `<g class="other" data-pick="${i}" style="--c:${colour(i + 1)}"><polyline class="hit" points="${line(c)}"/><polyline class="cline" points="${line(c)}"/>${c.map((p) => `<circle class="cdot" cx="${x(p.celsius)}" cy="${y(p.duty)}" r="2.5"/>`).join('')}</g>`;
  }).join('');

  const marks = d.follows.map((f, i) => {
    const t = nows[i];
    if (t == null || !f.curve.length || t < lo || t > hi) return '';
    return `<g class="nowmark" style="--c:${colour(i + 1)}"><line x1="${x(t)}" x2="${x(t)}" y1="${T}" y2="${H - B}"/><circle cx="${x(t)}" cy="${y(limit(dutyAt(f.curve, t)))}" r="4"><title>${esc(f.name)}: ${deg(t)}° now</title></circle></g>`;
  }).join('');

  let picked = '';
  const f = d.follows[row.pick];
  if (f?.curve.length) {
    const c = [...f.curve].sort(byCelsius);
    picked = `<g class="picked" style="--c:${colour(row.pick + 1)}"><polygon class="carea" points="${line(c, limit)} ${x(hi)},${y(0)} ${x(lo)},${y(0)}"/><polyline class="cline" points="${line(c)}"/>${c.map((p) => `<circle class="cpoint${row.drag?.p === p ? ' held' : ''}" cx="${x(p.celsius)}" cy="${y(p.duty)}" r="5.5"/>`).join('')}</g>`;
    const held = row.drag?.p;
    if (held) picked += `<text class="held" x="${x(held.celsius)}" y="${Math.max(T + 4, y(held.duty) - 12)}" text-anchor="middle">${held.celsius}° → ${held.duty}%</text>`;
  }

  return `<svg class="curvechart" viewBox="0 0 ${W} ${H}" width="${W}" height="${H}" role="img" aria-label="curves">${grid}${others}${marks}${picked}<text class="readout" x="${W - R}" y="${T - 4}" text-anchor="end">${esc(row.readout || '')}</text></svg>`;
}

// drawEditor is the zone's limits, and the picked curve's points as numbers, for when a whole degree on the chart is not near enough.
function drawEditor(row) {
  const d = row.draft;
  const f = d.follows[row.pick];
  const ed = $('.editor', row.el);
  f?.curve.sort(byCelsius);

  ed.innerHTML = `
    <div class="limits">
      <label>floor <input data-k="floor" type="number" min="0" max="100" value="${d.floor}">%</label>
      <label>ceiling <input data-k="ceiling" type="number" min="0" max="100" value="${d.ceiling}">%</label>
      <label>failsafe <input data-k="failsafe" type="number" min="0" max="100" value="${d.failsafe}">%</label>
    </div>
    ${f ? `
      <p><strong>${esc(f.name)}</strong> <span class="dim">${esc(f.sensors.join(', '))}, ${esc(f.by)}</span></p>
      <table><thead><tr><th>at °C</th><th>run at %</th><th></th></tr></thead><tbody>
        ${f.curve.map((p, j) => `<tr><td><input data-p="${j}" data-k="celsius" type="number" step="0.5" value="${p.celsius}"></td><td><input data-p="${j}" data-k="duty" type="number" min="0" max="100" value="${p.duty}"></td><td><button data-del="${j}"${f.curve.length < 2 ? ' disabled' : ''}>remove</button></td></tr>`).join('')}
      </tbody></table>
      <div class="row"><button class="add-point">add a point</button></div>` : ''}
    <p class="note">Saved with the Save beside its curves.</p>`;

  const changed = () => {
    row.dirty = true;
    drawCurves(row);
  };
  for (const input of ed.querySelectorAll('.limits input')) {
    input.oninput = () => { d[input.dataset.k] = Number(input.value); changed(); };
  }
  if (!f) return;

  for (const input of ed.querySelectorAll('table input')) {
    input.oninput = () => { f.curve[Number(input.dataset.p)][input.dataset.k] = Number(input.value); changed(); };
  }
  for (const b of ed.querySelectorAll('button[data-del]')) {
    b.onclick = () => { f.curve.splice(Number(b.dataset.del), 1); changed(); drawEditor(row); };
  }
  $('.add-point', ed).onclick = () => {
    const last = f.curve[f.curve.length - 1] || { celsius: 40, duty: d.floor };
    f.curve.push({ celsius: last.celsius + 5, duty: Math.min(100, last.duty + 10) });
    changed();
    drawEditor(row);
  };
}

// a section's temperatures: its disks as their enclosures' bays, and every temperature charted

// heat is a temperature's colour: green when cool, red at 60° and over.
const heat = (c) => `hsl(${Math.round(140 - 140 * Math.min(Math.max((c - 30) / 30, 0), 1))} 70% 45%)`;
const slotOf = (r) => { const n = parseInt(String(r.disk?.slot ?? '').replace(/\D+/g, ''), 10); return Number.isNaN(n) ? 1e6 : n; };
const short = (r) => r.id.replace(/^ipmi\/[^/]+\//, '').replace(/^hwmon\//, '');

function drawTemps(box, name, rs) {
  if (!rs.length) {
    box.innerHTML = '';
    return;
  }

  const disks = rs.filter((r) => r.disk);
  const others = rs.filter((r) => !r.disk);
  const enclosures = new Map();
  for (const r of disks) {
    const k = r.disk.enclosure || '';
    if (!enclosures.has(k)) enclosures.set(k, []);
    enclosures.get(k).push(r);
  }

  const chips = others.map((r) => `<span class="chip ${r.zones?.length ? 'followed' : ''}" title="${esc(r.id)}${r.zones?.length ? ', followed by ' + esc(r.zones.join(', ')) : ''}">${esc(short(r))} ${deg(r.celsius)}°</span>`).join('');
  box.innerHTML = `<div class="panel temps">${[...enclosures].map(([enc, ds]) => bay(enc, ds)).join('')}<div class="tgrid">
    ${disks.length ? `<div class="zchart"><div class="title">Its disks <span class="dim">°C, ${disks.length}</span></div><div class="plot disks"></div></div>` : ''}
    ${others.length ? `<div class="zchart"><div class="title">${name ? 'Its sensors' : "This machine's chips"} <span class="dim">°C</span></div><div class="chips">${chips}</div><div class="plot others"></div></div>` : ''}
  </div></div>`;

  const celsius = { unit: '°', step: 5 };
  if (disks.length) plot($('.plot.disks', box), 'disks/' + name, disks.map((r, i) => ({ label: `${r.disk.name}${r.disk.slot ? ' · slot ' + r.disk.slot : ''}`, values: hist.series['temp/' + r.id], colour: colour(i) })), celsius, null, true);
  if (others.length) plot($('.plot.others', box), 'sensors/' + name, others.map((r, i) => ({ label: short(r), values: hist.series['temp/' + r.id], colour: colour(i) })), celsius, null, true);
}

// bay is an enclosure's disks in slot order, each coloured by how hot it is, and which zones follow them.
function bay(enc, ds) {
  ds = [...ds].sort((a, b) => slotOf(a) - slotOf(b) || a.disk.name.localeCompare(b.disk.name));
  const hottest = Math.max(...ds.map((r) => r.celsius));
  const zones = [...new Set(ds.flatMap((r) => r.zones || []))];
  const where = enc ? `enclosure <code>${esc(enc)}</code>${ds[0].disk.enclosure_name ? ` <span class="dim">${esc(ds[0].disk.enclosure_name)}</span>` : ''}` : 'in no enclosure';

  return `<div class="bay">
    <div class="title">${where} <span class="dim">· ${ds.length} disks · hottest ${deg(hottest)}° · ${zones.length ? 'followed by ' + esc(zones.join(', ')) : 'followed by no zone'}</span>${!zones.length && enc && state.zones.length ? ' ' + followButtons(enc, state.zones) : ''}</div>
    <div class="slots">${ds.map((r) => `<div class="slot ${r.fresh ? '' : 'stale'}" style="--h:${heat(r.celsius)}" title="${esc([r.disk.name, r.disk.model, r.disk.serial ? 'serial ' + r.disk.serial : '', r.disk.slot ? 'slot ' + r.disk.slot : '', 'read with ' + r.disk.via, r.fresh ? '' : 'stale'].filter(Boolean).join(' · '))}"><span class="n">${r.disk.slot ? 'slot ' + esc(r.disk.slot) : esc(r.disk.name)}</span><b>${Math.round(r.celsius)}°</b><span class="d">${esc(r.disk.name)}</span></div>`).join('')}</div>
  </div>`;
}

// the test of which disks a zone cools: each enclosure, the most cooled first, and each disk's change

// cooling is how strongly a disk's tile is coloured for cooling by d degrees: not at all for none, fully for 6° and more.
const cooling = (d) => `color-mix(in srgb, var(--s1) ${Math.round(Math.min(Math.max(d, 0), 6) / 6 * 100)}%, var(--axis))`;

function probePanel(p, z) {
  const left = Math.max(0, Math.ceil((new Date(p.until) - Date.now()) / 60e3));
  const head = !p.done ? `Testing which disks it cools: flat out until ${clock(p.until)}, ${left} minute${left === 1 ? '' : 's'} left` : p.stopped ? `The test was stopped: what it saw from ${clock(p.started)} until then` : `Tested from ${clock(p.started)} to ${clock(p.until)}`;

  const groups = new Map();
  for (const d of p.disks || []) {
    if (!groups.has(d.enclosure)) groups.set(d.enclosure, []);
    groups.get(d.enclosure).push(d);
  }
  const each = [...groups].map(([enc, ds]) => ({ enc, ds, avg: ds.reduce((n, d) => n + d.before - d.now, 0) / ds.length, most: Math.max(...ds.map((d) => d.before - d.now)) }));
  each.sort((a, b) => b.avg - a.avg);

  const zonesOf = (enc) => [...new Set(state.readings.filter((r) => r.disk && (r.disk.enclosure || '') === enc).flatMap((r) => r.zones || []))];
  const where = (enc) => (enc ? `enclosure <code>${esc(enc)}</code>` : 'in no enclosure');
  const best = each[0];
  const verdict = !best ? 'No disks have been read yet.' : best.avg >= 1 ? `Its fans cool ${where(best.enc)} the most${p.done ? '' : ' so far'}: its disks cooled ${best.avg.toFixed(1)}° on average.` : `No enclosure's disks have cooled by a degree on average${p.done ? '' : ' yet'}.`;

  return `<div class="panel probe">
    <div class="row"><b>${esc(head)}</b><button data-act="probe-stop" data-zone="${esc(z.name)}">${p.done ? 'Put it away' : 'Stop the test'}</button></div>
    <p class="why">${verdict}</p>
    <table><thead><tr><th>Where</th><th class="num">Disks</th><th class="num">Cooled on average</th><th class="num">Cooled most</th><th>Followed by</th></tr></thead><tbody>
      ${each.map((g) => { const zs = zonesOf(g.enc); return `<tr><td>${where(g.enc)}</td><td class="num">${g.ds.length}</td><td class="num">${g.avg.toFixed(1)}°</td><td class="num">${g.most.toFixed(1)}°</td><td>${zs.length ? esc(zs.join(', ')) : g.enc ? followButtons(g.enc, [z]) : '<span class="dim">no zone</span>'}</td></tr>`; }).join('')}
    </tbody></table>
    ${each.map((g) => `<div class="bay"><div class="title">${where(g.enc)}</div><div class="slots">${[...g.ds].sort((a, b) => slotOf({ disk: a }) - slotOf({ disk: b }) || a.name.localeCompare(b.name)).map((d) => `<div class="slot" style="--h:${cooling(d.before - d.now)}" title="${esc(d.name)}: ${deg(d.before)}° when it began, ${deg(d.now)}° now"><span class="n">${d.slot ? 'slot ' + esc(d.slot) : esc(d.name)}</span><b>${d.before - d.now > 0 ? '−' : '+'}${Math.abs(d.before - d.now).toFixed(1)}°</b><span class="d">${esc(d.name)}</span></div>`).join('')}</div></div>`).join('')}
  </div>`;
}

// following an enclosure: a button for each zone, which asks for a second click before it changes the settings file

let armed = null;

function followButtons(enc, zones) {
  const pattern = `disk/${enc}/*`;
  return `<span class="assign">${zones.map((z) => {
    const ready = armed && armed.key === z.name + ' ' + pattern && Date.now() < armed.until;
    return `<button data-act="follow" data-zone="${esc(z.name)}" data-pattern="${esc(pattern)}" class="${ready ? 'on' : ''}" title="adds ${esc(pattern)} to what ${esc(z.name)} follows, in the settings file">${ready ? `click again: ${esc(z.name)} follows ${esc(pattern)}` : 'follow with ' + esc(z.name)}</button>`;
  }).join('')}</span>`;
}

function notice(text) {
  const n = $('#notice');
  n.textContent = text;
  n.hidden = false;
  clearTimeout(notice.timer);
  notice.timer = setTimeout(() => { n.hidden = true; }, 8000);
}

document.addEventListener('click', async (e) => {
  const b = e.target.closest('button[data-act]');
  if (!b) return;

  try {
    if (b.dataset.act === 'probe-stop') await api('DELETE', `/api/zones/${encodeURIComponent(b.dataset.zone)}/probe`);
    if (b.dataset.act === 'follow') {
      const key = b.dataset.zone + ' ' + b.dataset.pattern;
      if (!armed || armed.key !== key || Date.now() > armed.until) {
        armed = { key, until: Date.now() + 6000 };
        drawOverview();
        return;
      }
      armed = null;
      await api('PUT', `/api/zones/${encodeURIComponent(b.dataset.zone)}`, { follow: b.dataset.pattern });
      notice(`${b.dataset.zone} now follows ${b.dataset.pattern}, saved in the settings file. Its curve is in Settings, and on its row.`);
    }
    await refresh();
  } catch (err) {
    notice(err.message);
  }
});

// every temperature, as a table, drawn only while it is open

function drawTable() {
  if (!$('#every').open) return;

  $('#spark-head').textContent = 'last ' + ($('#span button.on')?.textContent || '');
  const filter = $('#filter').value.trim().toLowerCase();
  $('#readings').innerHTML = state.readings
    .filter((r) => !filter || r.id.toLowerCase().includes(filter))
    .map((r) => `<tr><td><code>${esc(r.id)}</code></td><td class="num">${deg(r.celsius)}</td><td>${spark(hist.series['temp/' + r.id])}</td><td>${esc(r.source)}${r.fresh ? '' : ' <span class="problem">stale</span>'}</td><td>${esc((r.zones || []).join(', '))}</td><td class="dim">${r.disk ? esc([r.disk.name, r.disk.model, r.disk.slot ? 'slot ' + r.disk.slot : ''].filter(Boolean).join(' · ')) : ''}</td></tr>`)
    .join('');
}
$('#filter').addEventListener('input', () => state && drawTable());
$('#every').addEventListener('toggle', () => state && drawTable());

// corrections: every zone's, newest first

function drawCorrections() {
  const all = state.zones.flatMap((z) => (z.corrections || []).map((c) => ({ ...c, zone: z.name, bmc: z.bmc })));
  all.sort((a, b) => new Date(b.at) - new Date(a.at));

  const changed = state.zones.reduce((n, z) => n + z.takeovers, 0);
  const resets = state.zones.reduce((n, z) => n + z.resets, 0);
  const total = changed + resets;
  const hour = all.filter((c) => Date.now() - new Date(c.at) < 3600e3).length;
  const hours = state.started ? (Date.now() - new Date(state.started)) / 3600e3 : 0;
  const since = state.started ? 'since ' + when(state.started) : '';

  $('#corrections-n').textContent = total ? total : '';
  $('#fix-tiles').innerHTML = `
    <div class="tile ${total ? 'warn' : ''}"><div class="v">${total}</div><div class="k">corrections</div><div class="d">${esc(since)}</div></div>
    <div class="tile"><div class="v">${hour}</div><div class="k">in the last hour</div></div>
    <div class="tile"><div class="v">${hours >= 1 ? (total / hours).toFixed(1) : '–'}</div><div class="k">an hour, on average</div></div>
    <div class="tile"><div class="v">${changed}</div><div class="k">found changed</div><div class="d">by its BMC or someone</div></div>
    <div class="tile"><div class="v">${resets}</div><div class="k">after a reset</div><div class="d">its BMC readied again</div></div>`;

  if (all.length === 0) {
    const none = state.dry_run ? 'A dry run sets nothing, so nothing is corrected.' : `None ${since}: every zone has been where it was set.`;
    $('#fixes').innerHTML = `<tr><td colspan="8" class="empty">${esc(none)}</td></tr>`;
    return;
  }

  $('#fixes').innerHTML = all.map((c) => `
    <tr>
      <td class="when">${esc(when(c.at))}</td>
      <td>${esc(c.zone)} <span class="dim">${esc(c.bmc)}</span></td>
      <td><span class="badge g ${esc(c.why)}">${c.why === 'reset' ? 'BMC reset' : 'changed'}</span></td>
      <td class="num">${pct(c.found)}</td>
      <td class="num">${pct(c.was)}</td>
      <td class="num">${pct(c.set)}</td>
      <td class="dim">${esc(c.mode || '–')}</td>
      <td>${esc(c.says)}</td>
    </tr>`).join('');
}

// settings

async function loadSettings() {
  try {
    const c = await api('GET', '/api/config');
    $('#path').textContent = c.exists ? c.path : c.path + ' is not there yet: saving writes it';
    $('#yaml').value = c.text;
    $('#errors').innerHTML = '';
  } catch (e) {
    $('#errors').innerHTML = `<li>${esc(e.message)}</li>`;
  }
}

$('#reload').onclick = loadSettings;
$('#save').onclick = async () => {
  $('#errors').innerHTML = '';
  $('#saved').textContent = '';
  try {
    await api('PUT', '/api/config', { text: $('#yaml').value });
    $('#saved').textContent = 'saved and taken up';
    for (const row of rows.values()) row.dirty = false;
    refresh();
  } catch (e) {
    $('#errors').innerHTML = e.message.split('\n').map((m) => `<li>${esc(m)}</li>`).join('');
  }
};

// the token

$('#token').value = token;
$('#token').addEventListener('change', () => {
  token = $('#token').value;
  try { localStorage.setItem('ipmi-fan-controller-token', token); } catch { /* kept for this visit only */ }
});

// start

api('GET', '/api/about').then((a) => {
  $('#version').textContent = a.version;
  if (a.token) $('#tokenbox').hidden = false;
}, () => {});
showSpan();
showTab();
refresh();
setInterval(refresh, 3000);
