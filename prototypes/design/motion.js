// HomersLab motion: screen changes and shared elements with the View Transitions API, lists that gain or lose a row,
// the tab bar's lens, and the light that follows a pointer across Home's glass. Framework-free, no dependencies.
// Every function makes its change at once where the API is missing. Under reduced motion (the system setting, or
// data-motion="reduce" on <html>) screens only cross-fade and lists and shared elements change instantly.
(() => {
  const root = document.documentElement;
  const mq = matchMedia('(prefers-reduced-motion: reduce)');
  const still = () => mq.matches || root.dataset.motion === 'reduce';
  const can = () => typeof document.startViewTransition === 'function' && !document.hidden;
  const ident = (s) => String(s).replace(/[^a-zA-Z0-9_-]/g, '-');
  const supportsClass = typeof CSS !== 'undefined' && CSS.supports && CSS.supports('view-transition-class', 'row');

  function name(el, n, cls) { if (!el) return; el.style.viewTransitionName = n; if (cls && supportsClass) el.style.viewTransitionClass = cls; }
  function unname(el) { if (!el) return; el.style.viewTransitionName = ''; if (supportsClass) el.style.viewTransitionClass = ''; }

  // run(update, { type, from, to, cls }): the update as one view transition.
  //   type: 'push' (deeper), 'pop' (back), 'fade' (a tab, a theme), 'list' (rows), 'morph' (a shared element)
  //   from: [[element, name], ...] in the old state; to(): the same pairs in the new state, after the update
  function run(update, { type = 'fade', from = [], to = null, cls = null } = {}) {
    if (!can()) { update(); return Promise.resolve(); }
    if (still()) {
      if (type === 'list' || type === 'morph') { update(); return Promise.resolve(); }
      from = []; to = null; type = 'fade';   // screens cross-fade, nothing travels
    }
    root.dataset.vt = type;
    for (const [el, n] of from) name(el, n, cls);
    let named = [];
    let vt;
    try {
      vt = document.startViewTransition(() => {
        for (const [el] of from) unname(el);
        update();
        if (to) { named = to() || []; for (const [el, n] of named) name(el, n, cls); }
      });
    } catch { delete root.dataset.vt; for (const [el] of from) unname(el); update(); return Promise.resolve(); }
    const done = () => { delete root.dataset.vt; for (const [el] of named) unname(el); };
    vt.finished.then(done, done);
    vt.ready.catch(() => {});        // a skipped transition still applies the update
    return vt.updateCallbackDone.catch(() => {});
  }

  // a list that gains or loses rows: each row ([data-row="<id>"]) keeps its name across the update, so the others
  // glide into place and the new or gone row fades
  function list(update, scope = document) {
    const pairs = () => [...scope.querySelectorAll('[data-row]')].map((el) => [el, `row-${ident(el.dataset.row)}`]);
    return run(update, { type: 'list', from: pairs(), to: pairs, cls: 'row' });
  }

  // a shared element: give the old element a name now and the new one the same name after the update
  function morph(update, pairs, type = 'push') {
    const from = pairs.map(([el, n]) => [el, n]);
    const to = () => pairs.map(([, n, find]) => [typeof find === 'function' ? find() : document.querySelector(find), n]).filter(([el]) => el);
    return run(update, { type: still() ? 'fade' : type, from: from.filter(([el]) => el), to });
  }

  // the tab bar's lens slides to the chosen tab, from where it was
  const lensAt = new WeakMap();
  function lens(bar) {
    if (!bar) return;
    const tabs = [...bar.querySelectorAll('.tab')], i = Math.max(0, tabs.findIndex((t) => t.getAttribute('aria-current') === 'page'));
    let l = bar.querySelector('.tabbar-lens');
    if (!l) { l = document.createElement('span'); l.className = 'tabbar-lens'; l.setAttribute('aria-hidden', 'true'); bar.prepend(l); }
    bar.style.setProperty('--n', tabs.length);
    const was = lensAt.get(bar);
    if (was !== undefined && was !== i && !still()) {
      l.style.transition = 'none'; bar.style.setProperty('--pos', was); void l.offsetWidth; l.style.transition = '';
      requestAnimationFrame(() => bar.style.setProperty('--pos', i));
    } else bar.style.setProperty('--pos', i);
    lensAt.set(bar, i);
  }

  // light that follows the pointer across glass marked .glass-react (Home's panels and tiles)
  addEventListener('pointermove', (e) => {
    if (still() || e.pointerType === 'touch') return;
    const el = e.target.closest && e.target.closest('.glass-react'); if (!el) return;
    const r = el.getBoundingClientRect();
    el.style.setProperty('--mx', `${Math.round(e.clientX - r.left)}px`); el.style.setProperty('--my', `${Math.round(e.clientY - r.top)}px`);
  }, { passive: true });
  addEventListener('pointerout', (e) => { const el = e.target.closest && e.target.closest('.glass-react'); if (el && !el.contains(e.relatedTarget)) { el.style.removeProperty('--mx'); el.style.removeProperty('--my'); } }, { passive: true });

  // ---------- the glass level: Clear, Balanced, Frosted, Solid, or a number between Clear (0) and Frosted (1) ----------
  // Applied on <html>; the change glides on the smooth spring (the registered --glass transitions). The system's
  // reduced-transparency setting always wins: CSS keeps every surface solid under it, whatever is chosen here.
  const PRESETS = { clear: 0, balanced: 0.5, frosted: 1 };
  const reduced = () => matchMedia('(prefers-reduced-transparency: reduce)').matches || root.dataset.transparency === 'reduce';
  let glassSet = false;
  function glass(v) {
    // the first level a page sets is simply there; only later changes glide
    if (!glassSet) { glassSet = true; root.style.transition = 'none'; requestAnimationFrame(() => requestAnimationFrame(() => root.style.removeProperty('transition'))); }
    if (v == null || v === '') v = 'balanced';
    if (v === 'solid' || v in PRESETS) { root.dataset.glass = v; root.style.removeProperty('--glass'); return v; }
    const n = Math.max(0, Math.min(1, Number(v))); if (Number.isNaN(n)) return glass('balanced');
    const near = Object.keys(PRESETS).find((k) => Math.abs(PRESETS[k] - n) < 0.001);
    if (near) return glass(near);
    root.dataset.glass = 'custom'; root.style.setProperty('--glass', String(n)); return n;
  }
  const glassValue = () => (root.dataset.glass === 'custom' ? Number(root.style.getPropertyValue('--glass')) : root.dataset.glass || 'balanced');
  // the control: four presets and, unless Solid, a fine slider from Clear to Frosted
  function glassControl(id = 'glass') {
    const v = glassValue(), n = typeof v === 'number' ? v : PRESETS[v] ?? 1, solid = v === 'solid';
    const preset = typeof v === 'number' ? null : v;
    return `<div class="glass-control" data-glass-control>
      <div class="segmented segmented-block" style="--n:4" role="radiogroup" aria-label="Glass" data-seg="${id}"><span class="segmented-thumb" aria-hidden="true"></span>${[['clear', 'Clear'], ['balanced', 'Balanced'], ['frosted', 'Frosted'], ['solid', 'Solid']].map(([k, l]) => `<button type="button" role="radio" aria-checked="${preset === k}" data-glass-set="${k}">${l}</button>`).join('')}</div>
      ${reduced() ? '<p class="hint">This device asks for reduced transparency, so glass stays solid whatever you choose here.</p>' : ''}
      ${solid ? '' : `<label class="glass-slider"><span class="sr-only">Glass, from clear to frosted</span><span aria-hidden="true">Clear</span><input type="range" min="0" max="1" step="0.05" value="${n}" data-glass-range><span aria-hidden="true">Frosted</span></label>`}</div>`;
  }

  // a control anywhere on the page sets the level live; the page hears 'hl:glass' to keep the choice and redraw
  // every control on the page follows the level in place (no redraw, so a slider keeps the pointer while dragged)
  function sync() {
    const v = glassValue(), preset = typeof v === 'number' ? null : v;
    for (const c of document.querySelectorAll('[data-glass-control]')) {
      const seg = c.querySelector('.segmented'), b = [...c.querySelectorAll('[data-glass-set]')];
      b.forEach((x) => x.setAttribute('aria-checked', String(x.dataset.glassSet === preset)));
      const i = b.findIndex((x) => x.dataset.glassSet === preset);
      seg.classList.toggle('is-free', i < 0); if (i >= 0) seg.style.setProperty('--pos', i);
      const r = c.querySelector('[data-glass-range]'), solid = v === 'solid';
      if (solid && r) r.closest('.glass-slider').remove();
      else if (!solid && !r) { const t = document.createElement('div'); t.innerHTML = glassControl(); c.append(t.querySelector('.glass-slider')); }
      else if (r && document.activeElement !== r) r.value = typeof v === 'number' ? v : PRESETS[v];
    }
  }
  const told = (v) => { sync(); document.dispatchEvent(new CustomEvent('hl:glass', { detail: v })); };
  document.addEventListener('click', (e) => { const b = e.target.closest && e.target.closest('[data-glass-set]'); if (!b) return; told(glass(b.dataset.glassSet)); });
  document.addEventListener('input', (e) => { const r = e.target.closest && e.target.closest('[data-glass-range]'); if (!r) return; told(glass(r.value)); });

  window.HL = { still, run, list, morph, lens, ident, glass, glassValue, glassControl, glassSync: sync };
})();
