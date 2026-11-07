'use strict';

const API = '/api/v1';
const FAST_POLL_MS = 3000;   // while a scan is running
const SLOW_POLL_MS = 30000;

const state = { workspaces: [], selectedScan: null, openStatus: null, kindFilter: null, timer: null };

// --- API ------------------------------------------------------------------

function apiKey() {
  try { return localStorage.getItem('tfdrift-api-key') || ''; } catch { return ''; }
}

async function api(path, opts = {}) {
  const headers = { ...(opts.headers || {}) };
  const key = apiKey();
  if (key) headers['X-API-Key'] = key;
  const res = await fetch(API + path, { ...opts, headers });
  if (res.status === 401) {
    const entered = prompt('This server requires an API key:');
    if (entered) {
      try { localStorage.setItem('tfdrift-api-key', entered); } catch {}
      return api(path, opts);
    }
  }
  const body = await res.json().catch(() => ({}));
  if (!res.ok) throw Object.assign(new Error(body.error || res.statusText), { body, status: res.status });
  return body;
}

// --- helpers ----------------------------------------------------------------

function el(tag, props = {}, children = []) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(props)) {
    if (v == null) continue;
    if (k === 'class') node.className = v;
    else if (k === 'text') node.textContent = v;
    else if (k.startsWith('on')) node.addEventListener(k.slice(2), v);
    else node.setAttribute(k, v);
  }
  for (const c of [].concat(children)) if (c != null) node.append(c);
  return node;
}

function fmtTime(iso) {
  if (!iso) return '—';
  return new Date(iso).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' });
}

function fmtDuration(ms) {
  if (ms == null) return '—';
  return ms < 1000 ? `${ms} ms` : `${(ms / 1000).toFixed(1)} s`;
}

function fmtValue(v) {
  if (v === null || v === undefined) return '(absent)';
  return typeof v === 'string' ? JSON.stringify(v) : String(v);
}

function describeSource(s) {
  if (s.s3) return `s3://${s.s3.bucket}/${s.s3.key}`;
  return s.url || s.path;
}

function statusBadge(scan) {
  if (scan.status === 'completed') {
    if (scan.has_drift) return el('span', { class: 'badge has-drift', text: 'drift' });
    if (scan.summary?.errors) return el('span', { class: 'badge incomplete', text: 'incomplete', title: 'Some resources could not be checked' });
    return el('span', { class: 'badge in-sync', text: 'in sync' });
  }
  return el('span', { class: `badge ${scan.status}`, text: scan.status });
}

function setStatus(msg) { document.getElementById('status').textContent = msg; }

// --- workspaces ---------------------------------------------------------------

function renderWorkspaces() {
  const box = document.getElementById('workspaces');
  box.replaceChildren();
  if (!state.workspaces.length) {
    box.append(el('p', { class: 'muted', text: 'No workspaces yet. Add them to drift.yaml or POST /api/v1/workspaces.' }));
    return;
  }
  for (const ws of state.workspaces) {
    const running = Boolean(ws.running_scan_id);
    const last = ws.last_scan;
    const button = el('button', {
      type: 'button', text: running ? 'Scanning…' : 'Run scan',
      onclick: () => triggerScan(ws.name, button),
    });
    button.disabled = running;

    box.append(el('article', { class: 'card' }, [
      el('h3', {}, [ws.name, ws.managed_by === 'config' ? el('span', { class: 'badge config', text: 'config' }) : null]),
      el('dl', {}, [
        el('dt', { text: 'State' }), el('dd', { class: 'mono', text: ws.sources.map(describeSource).join(', ') }),
        el('dt', { text: 'Region' }), el('dd', { text: ws.aws?.region || 'default' }),
        el('dt', { text: 'Schedule' }), el('dd', { text: ws.schedule ? `${ws.schedule} · next ${fmtTime(ws.next_run)}` : 'on demand' }),
        el('dt', { text: 'Last scan' }), el('dd', {}, last ? [statusBadge(last), ' ', fmtTime(last.started_at)] : 'never'),
      ]),
      el('div', { class: 'row' }, [
        last ? el('a', { href: '#', text: 'View last report', onclick: (e) => { e.preventDefault(); showReport(last.scan_id); } }) : el('span'),
        button,
      ]),
    ]));
  }
}

async function triggerScan(name, button) {
  button.disabled = true;
  button.textContent = 'Scanning…';
  try {
    const { scan_id } = await api(`/workspaces/${encodeURIComponent(name)}/scans`, { method: 'POST' });
    await showReport(scan_id);
  } catch (e) {
    if (e.status === 409 && e.body?.scan_id) await showReport(e.body.scan_id);
    else alert(`Could not start scan: ${e.message}`);
  }
  refresh();
}

// --- history ------------------------------------------------------------------

function countCell(n) {
  return el('td', { class: n ? 'num' : 'num zero', text: String(n ?? 0) });
}

function renderScans(scans) {
  const body = document.querySelector('#scans tbody');
  body.replaceChildren();
  if (!scans.length) {
    body.append(el('tr', {}, el('td', { colspan: '9', class: 'muted', text: 'No scans yet.' })));
    return;
  }
  for (const s of scans) {
    const row = el('tr', {
      class: 'clickable' + (s.scan_id === state.selectedScan ? ' selected' : ''),
      tabindex: '0',
      onclick: () => showReport(s.scan_id),
      onkeydown: (e) => { if (e.key === 'Enter') showReport(s.scan_id); },
    }, [
      el('td', { text: fmtTime(s.started_at), title: s.scan_id }),
      el('td', { text: s.workspace }),
      el('td', { title: s.error || null }, statusBadge(s)),
      countCell(s.summary.deleted),
      countCell(s.summary.modified),
      countCell(s.summary.tags_changed),
      countCell(s.summary.unmanaged),
      countCell(s.summary.errors),
      el('td', { class: 'num', text: s.status === 'running' ? '…' : fmtDuration(s.duration_ms) }),
    ]);
    body.append(row);
  }
}

function syncFilterOptions() {
  const select = document.getElementById('history-filter');
  const current = select.value;
  select.replaceChildren(el('option', { value: '', text: 'All workspaces' }),
    ...state.workspaces.map((ws) => el('option', { value: ws.name, text: ws.name })));
  select.value = state.workspaces.some((ws) => ws.name === current) ? current : '';
}

// --- report -------------------------------------------------------------------

const KINDS = ['DELETED', 'MODIFIED', 'TAGS_CHANGED', 'UNMANAGED', 'ERROR', 'SKIPPED'];

async function showReport(id, keepFilter = false) {
  state.selectedScan = id;
  if (!keepFilter) state.kindFilter = null;
  history.replaceState(null, '', `#scan=${encodeURIComponent(id)}`);
  try {
    renderReport(await api(`/scans/${encodeURIComponent(id)}`));
  } catch (e) {
    alert(`Could not load report: ${e.message}`);
  }
  document.querySelectorAll('#scans tbody tr').forEach((tr) => {
    tr.classList.toggle('selected', tr.firstChild?.title === id);
  });
}

function renderReport(r) {
  const section = document.getElementById('report');
  const wasHidden = section.hidden;
  section.hidden = false;
  state.openStatus = r.status;
  document.getElementById('report-id').textContent = r.scan_id;
  document.getElementById('report-json').href = `${API}/scans/${encodeURIComponent(r.scan_id)}/report?format=json`;
  document.getElementById('report-text').href = `${API}/scans/${encodeURIComponent(r.scan_id)}/report?format=text`;

  const meta = [`Workspace ${r.workspace || '—'}`, fmtTime(r.started_at), r.status];
  if (r.status !== 'running') meta.push(fmtDuration(r.duration_ms));
  if (r.sources?.length) meta.push(`state: ${r.sources.join(', ')}`);
  document.getElementById('report-meta').textContent = meta.join(' · ');

  const s = r.summary;
  const tiles = [
    ['Resources', s.resources], ['In sync', s.in_sync], ['Deleted', s.deleted],
    ['Modified', s.modified], ['Tags changed', s.tags_changed], ['Unmanaged', s.unmanaged],
    ['Errors', s.errors], ['Not checked', s.skipped],
  ];
  document.getElementById('tiles').replaceChildren(...tiles.map(([label, value]) =>
    el('div', { class: 'tile' }, [el('div', { class: 'value', text: String(value ?? 0) }), el('div', { class: 'label', text: label })])));

  const counts = Object.fromEntries(KINDS.map((k) => [k, r.drifts.filter((d) => d.kind === k).length]));
  const chips = document.getElementById('kind-filter');
  chips.replaceChildren(...[null, ...KINDS.filter((k) => counts[k])].map((k) => el('button', {
    type: 'button', class: 'chip', 'aria-pressed': String(state.kindFilter === k),
    text: k ? `${k.replace('_', ' ').toLowerCase()} (${counts[k]})` : `all (${r.drifts.length})`,
    onclick: () => { state.kindFilter = k; renderReport(r); },
  })));

  const list = document.getElementById('drifts');
  list.replaceChildren();
  if (r.status === 'failed') list.append(el('p', { class: 'drift ERROR', text: `Scan failed: ${r.error}` }));
  else if (r.status === 'running') list.append(el('p', { class: 'muted', text: 'Scan in progress…' }));
  const shown = r.drifts.filter((d) => !state.kindFilter || d.kind === state.kindFilter);
  if (r.status === 'completed' && !shown.length) {
    list.append(el('p', { class: 'muted', text: r.drifts.length ? 'Nothing matches this filter.' : 'No drift detected.' }));
  }
  for (const d of shown) list.append(renderDrift(d));

  if (wasHidden) section.scrollIntoView({ behavior: 'smooth', block: 'start' });
}

function renderDrift(d) {
  const target = d.address || `${d.type} (not in state)`;
  return el('div', { class: `drift ${d.kind}` }, [
    el('div', { class: 'drift-head' }, [
      el('span', { class: `kind ${d.kind}`, text: d.kind.replace('_', ' ') }),
      el('span', { class: 'sev', text: d.severity }),
      el('span', { class: 'addr mono', text: target }),
      el('span', { class: 'muted mono', text: [d.id, d.region].filter(Boolean).join(' · ') }),
    ]),
    d.message ? el('p', { class: 'msg', text: d.message }) : null,
    d.changes?.length ? el('div', { class: 'changes' }, d.changes.map(renderChange)) : null,
  ]);
}

// Lists (rules, security groups) show removed/added elements; scalars show
// state → cloud.
function renderChange(c) {
  const isList = Array.isArray(c.expected) || Array.isArray(c.actual);
  if (isList) {
    const exp = c.expected || [];
    const act = c.actual || [];
    const lines = [
      ...exp.filter((v) => !act.includes(v)).map((v) => el('del', { class: 'line mono', text: `− ${v}` })),
      ...act.filter((v) => !exp.includes(v)).map((v) => el('ins', { class: 'line mono', text: `+ ${v}` })),
    ];
    return el('div', { class: 'change' }, [el('span', { class: 'path mono', text: c.path }), ...lines]);
  }
  return el('div', { class: 'change' }, [
    el('div', { class: 'vals' }, [
      el('span', { class: 'path mono', text: c.path }),
      el('del', { class: 'mono', text: fmtValue(c.expected), title: 'in Terraform state' }),
      '→',
      el('ins', { class: 'mono', text: fmtValue(c.actual), title: 'in the cloud' }),
    ]),
  ]);
}

// --- refresh loop -------------------------------------------------------------

async function refresh() {
  clearTimeout(state.timer);
  let busy = false;
  try {
    const filter = document.getElementById('history-filter').value;
    const [workspaces, scans] = await Promise.all([
      api('/workspaces'),
      api(`/scans?limit=25${filter ? `&workspace=${encodeURIComponent(filter)}` : ''}`),
    ]);
    state.workspaces = workspaces;
    busy = workspaces.some((ws) => ws.running_scan_id) || scans.some((s) => s.status === 'running');
    renderWorkspaces();
    syncFilterOptions();
    renderScans(scans);

    // Keep an open report current while its scan runs.
    const reportOpen = !document.getElementById('report').hidden;
    if (reportOpen && state.openStatus === 'running') await showReport(state.selectedScan, true);
    setStatus(`Updated ${new Date().toLocaleTimeString()}`);
  } catch (e) {
    setStatus(`Error: ${e.message}`);
  }
  state.timer = setTimeout(refresh, busy ? FAST_POLL_MS : SLOW_POLL_MS);
}

document.getElementById('history-filter').addEventListener('change', refresh);
document.getElementById('report-close').addEventListener('click', () => {
  document.getElementById('report').hidden = true;
  state.selectedScan = null;
  state.openStatus = null;
  history.replaceState(null, '', location.pathname);
  document.querySelectorAll('#scans tbody tr.selected').forEach((tr) => tr.classList.remove('selected'));
});

// Reports are linkable: /#scan=<id> opens that report.
const linked = new URLSearchParams(location.hash.slice(1)).get('scan');
if (linked) showReport(linked);
refresh();
