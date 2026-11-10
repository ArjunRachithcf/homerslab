// HomersLab prototypes: the floating prototype bar. Not part of the design being judged (its look is in proto.css).
// - Variants: a screen with an open design question registers its variants; the arrows (and the left/right keys)
//   cycle them, and the choice lives in the URL, so it is shareable and survives a reload. Round 1 settled the
//   install plan (a page) and the kid's Home (its own layout), so those variants are gone.
// - Options: switches for the household's situation (its meshes, who is looking, on the mesh or away).
// - State: the in-memory state after every action (nothing is saved: reload to start again).
// ?bar=0 hides the bar (for clean screenshots); ?theme=light|dark forces a theme (the default follows the system);
// ?motion=reduce and ?transparency=reduce show what the system's reduce-motion and reduce-transparency settings do.
(() => {
  const params = new URLSearchParams(location.search);
  const listeners = [];
  const variants = {};      // key -> [{ key: 'A', name }]
  const options = [];       // [{ key, label, values: [{ v, name }], def }]
  let screen = null;        // the variants key of the screen on show, or null
  let stateFn = () => ({});
  let panelOpen = false;

  const get = (k, def) => (params.has(k) ? params.get(k) : def);
  const writeUrl = () => { const q = params.toString(); history.replaceState(null, '', location.pathname + (q ? '?' + q : '') + location.hash); };
  const set = (k, v, def) => { if (v == null || v === def) params.delete(k); else params.set(k, v); writeUrl(); applyTheme(); render(); listeners.forEach((f) => f(k, v)); };

  const applyTheme = () => {
    const r = document.documentElement, t = get('theme', 'auto');
    const own = window.HL_THEME_DEFAULT ? window.HL_THEME_DEFAULT() : null;   // a page with its own default (Admin: dark)
    if (t === 'light' || t === 'dark') r.dataset.theme = t; else if (own) r.dataset.theme = own; else delete r.dataset.theme;
    if (get('motion', 'auto') === 'reduce') r.dataset.motion = 'reduce'; else delete r.dataset.motion;
    if (get('transparency', 'auto') === 'reduce') r.dataset.transparency = 'reduce'; else delete r.dataset.transparency;
    // the glass level: the bar's choice for review, else the page's own (a person's choice, kept in this browser)
    const g = get('glass', null); if (window.HL) { HL.glass(g || (window.HL_GLASS_DEFAULT ? window.HL_GLASS_DEFAULT() : 'balanced')); HL.glassSync(); }
  };
  applyTheme();

  const esc = (s) => String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]);
  const icon = (n) => (window.I ? window.I(n, 'icon') : '');

  let bar, panel;
  function render() {
    if (get('bar', '1') === '0') { bar?.remove(); panel?.remove(); document.body.classList.remove('has-proto-bar'); return; }
    if (!bar) { bar = document.createElement('div'); bar.className = 'proto-bar'; bar.setAttribute('role', 'region'); bar.setAttribute('aria-label', 'Prototype controls'); document.body.appendChild(bar); document.body.classList.add('has-proto-bar'); }
    const vs = screen && variants[screen];
    const cur = vs ? current(screen) : null;
    const curV = vs ? vs.find((v) => v.key === cur) : null;
    const theme = get('theme', 'auto');
    const themeNext = { auto: 'light', light: 'dark', dark: 'auto' }[theme];
    bar.innerHTML = `
      <span class="pb-tag">Prototype</span>
      ${vs ? `<span class="pb-sep"></span>
        <button type="button" data-pb="prev" aria-label="Previous variant">${icon('chevronLeft')}</button>
        <span class="pb-label" aria-live="polite">${esc(cur)} · ${esc(curV.name)}</span>
        <button type="button" data-pb="next" aria-label="Next variant">${icon('chevronRight')}</button>` : ''}
      <span class="pb-sep"></span>
      <button type="button" data-pb="theme" aria-label="Theme: ${theme}. Switch to ${themeNext}">${icon(theme === 'dark' ? 'moon' : theme === 'light' ? 'sun' : 'auto')}<span>${theme === 'auto' ? 'Auto' : theme === 'light' ? 'Light' : 'Dark'}</span></button>
      <button type="button" data-pb="state" aria-expanded="${panelOpen}" aria-controls="proto-panel">State</button>`;
    renderPanel();
  }
  function renderPanel() {
    if (!panelOpen) { panel?.remove(); panel = null; return; }
    if (!panel) { panel = document.createElement('div'); panel.className = 'proto-panel'; panel.id = 'proto-panel'; panel.setAttribute('role', 'region'); panel.setAttribute('aria-label', 'Prototype state'); document.body.appendChild(panel); }
    const vs = screen && variants[screen];
    const st = stateFn() || {};
    panel.innerHTML = `
      ${vs ? `<div class="pp-group"><h3>Variants of this screen</h3><div class="pp-opts">${vs.map((v) => `<button type="button" data-pv="${v.key}" aria-pressed="${v.key === current(screen)}">${esc(v.key)} · ${esc(v.name)}</button>`).join('')}</div>${vs.find((v) => v.key === current(screen))?.question ? `<p>${esc(vs.find((v) => v.key === current(screen)).question)}</p>` : ''}</div>` : ''}
      ${options.map((o) => `<div class="pp-group"><h3>${esc(o.label)}</h3><div class="pp-opts">${o.values.map((x) => `<button type="button" data-po="${esc(o.key)}" data-val="${esc(x.v)}" aria-pressed="${get(o.key, o.def) === x.v}">${esc(x.name)}</button>`).join('')}</div>${o.note ? `<p>${esc(o.note)}</p>` : ''}</div>`).join('')}
      <div class="pp-group"><h3>State, in memory only (reload to start again)</h3><dl>${Object.entries(st).map(([k, v]) => `<dt>${esc(k)}</dt><dd>${esc(v)}</dd>`).join('')}</dl></div>`;
  }
  function current(key) { const vs = variants[key]; if (!vs) return null; const v = get(key, vs[0].key); return vs.some((x) => x.key === v) ? v : vs[0].key; }
  function cycle(dir) { const vs = variants[screen]; if (!vs) return; const i = vs.findIndex((v) => v.key === current(screen)); const n = vs[(i + dir + vs.length) % vs.length]; set(screen, n.key, vs[0].key); }

  document.addEventListener('click', (e) => {
    const b = e.target.closest('[data-pb],[data-pv],[data-po]');
    if (!b || !(bar?.contains(b) || panel?.contains(b))) return;
    if (b.dataset.pb === 'prev') cycle(-1);
    else if (b.dataset.pb === 'next') cycle(1);
    else if (b.dataset.pb === 'theme') { const t = get('theme', 'auto'); set('theme', { auto: 'light', light: 'dark', dark: 'auto' }[t], 'auto'); }
    else if (b.dataset.pb === 'state') { panelOpen = !panelOpen; render(); }
    else if (b.dataset.pv) set(screen, b.dataset.pv, variants[screen][0].key);
    else if (b.dataset.po) { const o = options.find((x) => x.key === b.dataset.po); set(o.key, b.dataset.val, o.def); }
  });
  document.addEventListener('keydown', (e) => {
    if (!screen || !variants[screen] || e.altKey || e.ctrlKey || e.metaKey) return;
    const t = e.target; if (t && (t.closest('input, textarea, select, [contenteditable=""], [contenteditable="true"]'))) return;
    if (document.querySelector('.scrim.is-open, .os-scrim')) return;
    if (e.key === 'ArrowLeft') { e.preventDefault(); cycle(-1); } else if (e.key === 'ArrowRight') { e.preventDefault(); cycle(1); }
  });

  window.Proto = {
    get, set,
    variants(key, list) { variants[key] = list; },
    option(o) { options.push(o); },
    current,
    screen(key) { screen = key && variants[key] ? key : null; render(); },
    state(fn) { stateFn = fn; },
    refresh() { if (panelOpen) renderPanel(); },
    onChange(f) { listeners.push(f); },
    start() {
      options.push({ key: 'motion', label: 'Motion', def: 'auto', values: [{ v: 'auto', name: 'As the device' }, { v: 'reduce', name: 'Reduced' }], note: 'Reduced: screens cross-fade; nothing else moves.' });
      options.push({ key: 'glass', label: 'Glass level', def: '', values: [{ v: '', name: 'As chosen' }, { v: 'clear', name: 'Clear' }, { v: 'balanced', name: 'Balanced' }, { v: 'frosted', name: 'Frosted' }, { v: 'solid', name: 'Solid' }], note: 'For review: overrides the person’s own choice in Settings or the person’s menu.' });
      options.push({ key: 'transparency', label: 'Transparency', def: 'auto', values: [{ v: 'auto', name: 'As the device' }, { v: 'reduce', name: 'Reduced' }], note: 'Reduced: every glass surface turns solid.' });
      render();
    },
  };
})();
