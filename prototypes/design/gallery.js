// The gallery's own script (design/index.html): icons into their slots, swatches read from the live tokens, and the
// behaviour each component has in a product (the bar turning to glass, the segmented thumb, sheets, a menu, a toast).
// Products implement these in their own components; this file only demonstrates them. Plain DOM, no dependencies.
(() => {
  const $ = (s, r = document) => r.querySelector(s);
  const $$ = (s, r = document) => [...r.querySelectorAll(s)];
  const still = () => (window.HL ? HL.still() : matchMedia('(prefers-reduced-motion: reduce)').matches);

  // ---------- icons and the mark ----------
  for (const el of $$('[data-i]')) {
    const cls = ['icon', ...el.className.split(/\s+/).filter(Boolean)].join(' ');
    el.outerHTML = window.I(el.dataset.i, cls);
  }
  for (const el of $$('[data-mark]')) el.outerHTML = window.MARK;
  for (const el of $$('[data-mark16]')) el.outerHTML = window.MARK_16;
  for (const el of $$('[data-mark32]')) el.outerHTML = window.MARK_32;
  for (const el of $$('[data-mark-large]')) el.outerHTML = window.MARK_LARGE;
  for (const el of $$('[data-lockup]')) el.outerHTML = window.LOCKUP;
  for (const el of $$('[data-lockup-lg]')) el.outerHTML = window.LOCKUP.replace('class="lockup"', 'class="lockup lockup-lg"');

  // ---------- swatches, read from each themed half ----------
  const SW = [
    ['--ground', 'Ground', 'The page'], ['--surface', 'Surface', 'Cards, groups, widgets'], ['--elevated', 'Elevated', 'Sheets'],
    ['--fill', 'Fill', 'Grey buttons, chips, tracks'], ['--separator', 'Separator', 'Hairlines between rows'], ['--control-border', 'Control border', 'Fields, an off switch'],
    ['--text', 'Text', 'Words'], ['--text-2', 'Secondary text', 'Sublines, values'], ['--accent', 'Accent', 'The primary action, links, the chosen'],
    ['--accent-tint', 'Accent tint', 'A chosen chip, an info notice'], ['--green', 'Green', 'Working, passed, on'], ['--green-text', 'Green words', 'A good verdict in words'],
    ['--orange', 'Orange', 'Updating, waiting'], ['--orange-text', 'Orange words', 'A warning in words'], ['--red', 'Red', 'Failed, not working'], ['--red-text', 'Red words', 'A failure, destructive actions'],
  ];
  const toHex = (c) => { const cs = c.match(/color\(srgb ([\d.]+) ([\d.]+) ([\d.]+)(?: \/ ([\d.]+))?\)/); if (cs) c = `rgba(${cs[1] * 255}, ${cs[2] * 255}, ${cs[3] * 255}, ${cs[4] ?? 1})`; const m = c.match(/rgba?\(([^)]+)\)/); if (!m) return c.trim(); const p = m[1].split(/[ ,/]+/).filter(Boolean).map(Number); const h = p.slice(0, 3).map((x) => Math.round(x).toString(16).padStart(2, '0')).join(''); return '#' + h + (p.length > 3 && p[3] < 1 ? ` at ${Math.round(p[3] * 100)}%` : ''); };
  for (const box of $$('[data-swatches]')) {
    const scope = box.closest('[data-theme]');
    box.innerHTML = SW.map(([v, name, use]) => `<div class="ds-swatch"><span class="ds-chip" style="background:var(${v})"></span><span style="min-width:0"><b>${name}</b><span>${use}</span><span class="ds-hex" data-var="${v}"></span></span></div>`).join('');
    const probe = document.createElement('span'); scope.appendChild(probe);
    for (const h of $$('.ds-hex', box)) { probe.style.color = `var(${h.dataset.var})`; h.textContent = `${h.dataset.var} · ${toHex(getComputedStyle(probe).color)}`; }
    probe.remove();
  }
  const space = $('[data-space]');
  if (space) space.innerHTML = [1, 2, 3, 4, 5, 6, 8, 10, 12, 16].map((n) => `<div><span>--space-${n} · ${n * 4}px</span><i style="width:${n * 4 * 3}px"></i></div>`).join('');

  // ---------- the restore strip and a QR code ----------
  for (const s of $$('[data-strip]')) s.innerHTML = [...s.dataset.strip].map((c) => `<span class="strip-cell ${c === 'f' ? 'is-fail' : c === '-' ? 'is-none' : ''}"></span>`).join('');
  const qr = (seed = 7) => {
    let cells = '', s = seed;
    const finder = (x, y) => { for (const [ox, oy] of [[0, 0], [18, 0], [0, 18]]) { const dx = x - ox, dy = y - oy; if (dx >= 0 && dx < 7 && dy >= 0 && dy < 7) return dx === 0 || dx === 6 || dy === 0 || dy === 6 || (dx >= 2 && dx <= 4 && dy >= 2 && dy <= 4) ? 1 : 0; if (dx >= -1 && dx <= 7 && dy >= -1 && dy <= 7) return 0; } return null; };
    for (let y = 0; y < 25; y++) for (let x = 0; x < 25; x++) { s = (s * 1103515245 + 12345) & 0x7fffffff; const f = finder(x, y); if (f === null ? (s >> 9) % 2 === 0 : f === 1) cells += `<rect x="${x}" y="${y}" width="1.02" height="1.02"/>`; }
    return `<svg class="qr" viewBox="0 0 25 25" role="img" aria-label="QR code to join the mesh (an example; it encodes nothing)">${cells}</svg>`;
  };
  for (const el of $$('[data-qr]')) el.outerHTML = qr();

  // ---------- segmented controls: the thumb springs to the chosen segment ----------
  const segPos = (seg) => { const b = $$('button', seg); const i = b.findIndex((x) => x.getAttribute('aria-checked') === 'true' || x.getAttribute('aria-pressed') === 'true'); seg.style.setProperty('--pos', Math.max(0, i)); };
  for (const seg of $$('.segmented')) {
    segPos(seg);
    seg.addEventListener('click', (e) => { const b = e.target.closest('button'); if (!b || b.disabled) return; $$('button', seg).forEach((x) => x.setAttribute(x.hasAttribute('aria-pressed') ? 'aria-pressed' : 'aria-checked', String(x === b))); segPos(seg); });
    seg.addEventListener('keydown', (e) => { if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return; const b = $$('button', seg); const i = b.indexOf(document.activeElement); if (i < 0) return; const n = b[(i + (e.key === 'ArrowRight' ? 1 : b.length - 1)) % b.length]; n.focus(); n.click(); e.preventDefault(); });
  }

  // ---------- appearance: auto follows the device; a choice is remembered here (a per-viewer convenience) ----------
  const root = document.documentElement, sw = $('[data-theme-switch]');
  const show = (t) => { $$('button', sw).forEach((b) => b.setAttribute('aria-checked', String(b.dataset.themeSet === t))); segPos(sw); };
  show(root.dataset.theme || 'auto');
  sw.addEventListener('click', (e) => {
    const b = e.target.closest('button'); if (!b) return; const t = b.dataset.themeSet;
    const apply = () => { if (t === 'auto') delete root.dataset.theme; else root.dataset.theme = t; };
    try { if (t === 'auto') localStorage.removeItem('hl-theme'); else localStorage.setItem('hl-theme', t); } catch (err) { /* private mode: the choice lasts this visit */ }
    if (window.HL) HL.run(apply, { type: 'fade' }); else apply();
    show(t);
  });

  // ---------- chips and choices ----------
  document.addEventListener('click', (e) => { const c = e.target.closest('.chip'); if (c && !c.disabled) c.setAttribute('aria-pressed', String(c.getAttribute('aria-pressed') !== 'true')); });

  // ---------- the top bar: glass once the page moves; the large title collapses into it ----------
  const bar = $('#top'), h1 = $('#h-ds');
  const onScroll = () => bar.classList.toggle('is-scrolled', scrollY > 4);
  addEventListener('scroll', onScroll, { passive: true }); onScroll();
  if ('IntersectionObserver' in window) new IntersectionObserver((es) => bar.classList.toggle('is-titled', !es[0].isIntersecting), { rootMargin: '-52px 0px 0px 0px' }).observe(h1);
  for (const ph of $$('[data-phone-scroll]')) {
    const tb = $('.topbar', ph), title = $('.ds-phone-title', ph);
    ph.addEventListener('scroll', () => { tb.classList.toggle('is-scrolled', ph.scrollTop > 4); tb.classList.toggle('is-titled', title.getBoundingClientRect().bottom < ph.getBoundingClientRect().top + 52); }, { passive: true });
  }

  // ---------- motion demo: four knobs, one per curve ----------
  $('[data-play]')?.addEventListener('click', () => $$('[data-motion]').forEach((t) => t.classList.toggle('is-on')));

  // ---------- illustrations ----------
  for (const el of $$('[data-illo]')) { const f = window.ILLO && ILLO[el.dataset.illo]; if (f) el.outerHTML = f({ name: 'The Ferreira household', domain: 'ferreira.family', made: true }).replace('class="illo ', 'class="illo is-in '); }

  // ---------- "Try it on this page": reduced transparency and reduced motion, as the system settings would ----------
  for (const b of $$('[data-try]')) b.addEventListener('click', () => {
    const k = b.dataset.try, on = root.dataset[k] !== 'reduce';
    if (on) root.dataset[k] = 'reduce'; else delete root.dataset[k];
    $$(`[data-try="${k}"]`).forEach((x) => { x.setAttribute('aria-pressed', String(on)); x.textContent = on ? 'Back to normal' : 'Try it on this page'; });
  });

  // ---------- the phone's tab bar: a lens snaps to the chosen tab ----------
  for (const tb of $$('.ds-phone .tabbar')) {
    window.HL?.lens(tb);
    tb.addEventListener('click', (e) => { const t = e.target.closest('.tab'); if (!t) return; e.preventDefault(); $$('.tab', tb).forEach((x) => x.toggleAttribute('aria-current', x === t)); $$('.tab', tb).forEach((x) => { if (x === t) x.setAttribute('aria-current', 'page'); }); HL.lens(tb); });
  }

  // ---------- a screen and a shared element: open an app, its icon flies to the next screen; Back brings it home ----------
  const mini = $('[data-mini]');
  const APPS = [['immich', 'Immich', 'Photos', 'Working · backed up at 14:00'], ['jellyfin', 'Jellyfin', 'Films and series', 'Working · Attic NAS'], ['navidrome', 'Navidrome', 'Music', 'Working · Attic NAS']];
  const listView = () => `<div class="ds-mini-screen vt-screen"><p class="ds-mini-title">Apps</p><div class="group lead-md">${APPS.map(([id, n, f]) => `<button type="button" class="row row-link" data-mini-open="${id}"><span class="row-lead"><img class="app-icon app-icon-md" src="examples/apps/${id}.svg" alt=""></span><span class="row-main"><span class="row-title" data-name>${n}</span><span class="row-sub">${f}</span></span>${window.I('chevronRight', 'icon row-chev')}</button>`).join('')}</div></div>`;
  const pageView = (id) => { const [, n, f, st] = APPS.find((a) => a[0] === id); return `<div class="ds-mini-screen vt-screen"><button type="button" class="topbar-back" data-mini-back="${id}">${window.I('chevronBack')}Apps</button><div class="ds-mini-hero"><img class="app-icon" src="examples/apps/${id}.svg" alt="" data-hero><h3 data-hero-title>${n}</h3><p class="subhead">${f} · ${st}</p></div><div class="group"><div class="row"><span class="row-main"><span class="row-label">Audience</span></span><span class="row-value">Adults</span></div><div class="row"><span class="row-main"><span class="row-label">Last backup</span></span><span class="row-value">14:00</span></div></div></div>`; };
  if (mini) {
    mini.innerHTML = listView();
    mini.addEventListener('click', (e) => {
      const o = e.target.closest('[data-mini-open]'), back = e.target.closest('[data-mini-back]');
      if (o) { const id = o.dataset.miniOpen; HL.morph(() => { mini.innerHTML = pageView(id); }, [[$('.app-icon', o), 'hero', '[data-hero]'], [$('[data-name]', o), 'hero-title', '[data-hero-title]']], 'push'); }
      if (back) { const id = back.dataset.miniBack; HL.morph(() => { mini.innerHTML = listView(); }, [[$('[data-hero]', mini), 'hero', `[data-mini-open="${id}"] .app-icon`], [$('[data-hero-title]', mini), 'hero-title', `[data-mini-open="${id}"] [data-name]`]], 'pop'); }
    });
  }

  // ---------- a list that gains and loses rows ----------
  const list = $('[data-demo-list]');
  let devs = [['tp', 'Tomás’s phone', 'Pixel 9 · Tailscale'], ['td', 'Tomás’s desktop', 'Windows 11 PC · Tailscale'], ['tv', 'Living-room TV', 'Android TV · NetBird']];
  const spare = [['li', 'Lúcia’s iPad', 'iPad · NetBird'], ['rt', 'Rita’s tablet', 'Galaxy Tab · NetBird']];
  const drawList = () => { list.innerHTML = devs.map(([id, n, sub]) => `<div class="row" data-row="${id}"><span class="row-lead">${window.I(id === 'tv' ? 'tv' : id === 'td' ? 'desktop' : id === 'tp' ? 'phone' : 'tablet', 'icon icon-md')}</span><span class="row-main"><span class="row-title">${n}</span><span class="row-sub">${sub}</span></span><button type="button" class="btn btn-sm" data-revoke="${id}">Revoke</button></div>`).join('') || '<div class="empty"><p class="empty-title">No devices</p></div>'; };
  if (list) {
    drawList();
    list.addEventListener('click', (e) => { const b = e.target.closest('[data-revoke]'); if (!b) return; HL.list(() => { const gone = devs.find((d) => d[0] === b.dataset.revoke); devs = devs.filter((d) => d !== gone); spare.push(gone); drawList(); }, list); });
    $('[data-list-add]')?.addEventListener('click', () => { if (!spare.length) return; HL.list(() => { devs.push(spare.shift()); drawList(); }, list); });
  }

  // ---------- progress: the bar fills on the smooth spring, each check draws itself as its step lands ----------
  const prog = $('[data-demo-progress]');
  const STEPS = ['Checked the plan', 'Pulled 3 images, pinned by digest', 'Made 3 secrets', 'Started Immich on Study PC'];
  let at = -1, timer = null;
  const drawProg = (fresh) => { prog.innerHTML = `<div class="progress-row"><div class="progress-head"><b>Installing Immich</b><span>${Math.max(0, at + 1)} of ${STEPS.length}</span></div><div class="progress" role="progressbar" aria-label="Install progress" aria-valuemin="0" aria-valuemax="${STEPS.length}" aria-valuenow="${Math.max(0, at + 1)}"><span style="--value:${(Math.max(0, at + 1) / STEPS.length) * 100}%"></span></div></div>
      <ol class="steps">${STEPS.map((t, i) => { const st = i <= at ? 'is-done' : i === at + 1 && timer ? 'is-now' : 'is-todo'; return `<li class="step ${st} ${i === fresh ? 'is-new' : ''}"><span class="step-mark">${st === 'is-done' ? window.I('check2') : st === 'is-now' ? '<span class="spinner"></span>' : ''}</span><span class="step-text"><span class="step-title">${t}</span></span></li>`; }).join('')}</ol>
      <div class="btn-row"><button type="button" class="btn btn-sm" data-run ${timer ? 'disabled' : ''}>${at >= STEPS.length - 1 ? 'Run it again' : 'Run the install'}</button></div>`; };
  if (prog) {
    drawProg(-1);
    prog.addEventListener('click', (e) => {
      if (!e.target.closest('[data-run]') || timer) return;
      at = -1; timer = setInterval(() => { at++; if (at >= STEPS.length - 1) { clearInterval(timer); timer = null; } drawProg(at); }, still() ? 200 : 700);
      drawProg(-1);
    });
  }

  // ---------- sheets: open on a scrim, focus kept inside, Escape or the scrim closes, focus goes back ----------
  const scrim = $('[data-scrim]'); let openSheet = null, opener = null;
  const focusables = (el) => $$('button:not([disabled]), a[href], input:not([disabled]), select, textarea, [tabindex="0"]', el);
  function open(sheet, from) {
    opener = from; openSheet = sheet; sheet.hidden = false;
    requestAnimationFrame(() => { sheet.classList.add('is-open'); scrim.classList.add('is-open'); });
    root.style.overflow = 'hidden';
    setTimeout(() => (focusables(sheet).find((b) => b.classList.contains('btn-primary')) || sheet).focus({ preventScroll: true }), 60);
  }
  function close() {
    if (!openSheet) return; const s = openSheet; openSheet = null;
    s.classList.remove('is-open'); scrim.classList.remove('is-open'); root.style.overflow = '';
    setTimeout(() => { if (!s.classList.contains('is-open')) s.hidden = true; }, still() ? 0 : 500);
    opener?.focus({ preventScroll: true });
  }
  $$('[data-open-sheet]').forEach((b) => b.addEventListener('click', () => open($('#ds-sheet-' + b.dataset.openSheet), b)));
  $$('[data-close]').forEach((b) => b.addEventListener('click', close));
  scrim.addEventListener('click', close);

  // ---------- a menu grows from its button ----------
  const menu = $('#ds-menu'), mb = $('[data-open-menu]');
  const setMenu = (on) => {
    if (on) { const r = mb.getBoundingClientRect(); menu.style.top = `${r.bottom + 8}px`; menu.style.left = `${Math.min(r.left, innerWidth - 304)}px`; menu.style.transformOrigin = 'top left'; }
    menu.classList.toggle('is-open', on); mb.setAttribute('aria-expanded', String(on));
    if (on) setTimeout(() => $('.menu-item', menu)?.focus({ preventScroll: true }), 30);
  };
  mb?.addEventListener('click', (e) => { e.stopPropagation(); setMenu(!menu.classList.contains('is-open')); });
  document.addEventListener('click', (e) => { if (menu.classList.contains('is-open') && !menu.contains(e.target)) setMenu(false); });
  addEventListener('scroll', () => { if (menu.classList.contains('is-open')) setMenu(false); }, { passive: true });

  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') { if (openSheet) { e.preventDefault(); close(); } else if (menu.classList.contains('is-open')) { setMenu(false); mb.focus(); } }
    if (e.key === 'Tab' && openSheet) { const f = focusables(openSheet); if (!f.length) return; const i = f.indexOf(document.activeElement); if (e.shiftKey && i <= 0) { e.preventDefault(); f[f.length - 1].focus(); } else if (!e.shiftKey && i === f.length - 1) { e.preventDefault(); f[0].focus(); } }
  });

  // ---------- a toast ----------
  $('[data-toast]')?.addEventListener('click', () => {
    const host = $('.toast-host'), t = document.createElement('div');
    t.className = 'toast toast-ok'; t.innerHTML = `${window.I('check')}<span>Tomás's phone was cut off at 14:07.</span>`;
    host.appendChild(t);
    setTimeout(() => { t.classList.add('is-leaving'); setTimeout(() => t.remove(), still() ? 0 : 220); }, 3600);
  });
})();

// the glass level for the whole page: kept in this browser
(() => {
  const host = document.querySelector('[data-ds-glass]'); if (!host || !window.HL) return;
  try { const v = localStorage.getItem('hl-glass'); if (v) HL.glass(v); } catch (e) {}
  host.innerHTML = HL.glassControl('ds-glass'); HL.glassSync();
  document.addEventListener('hl:glass', (e) => { try { localStorage.setItem('hl-glass', String(e.detail)); } catch (err) {} });
})();

// round 4: the charts, drawn from plain numbers
(() => {
  if (!window.HL || !HL.treemap) return;
  const put = (k, html) => { const el = document.querySelector(`[data-demo="${k}"]`); if (el) el.innerHTML = html; };
  put('usage', HL.usage([{ label: 'Media library', gb: 19400, tone: 2 }, { label: 'Jellyfin', gb: 182, tone: 1 }, { label: 'HomersLab’s leftovers', gb: 130, tone: 4 }, { label: 'System and snapshots', gb: 128, tone: 5 }], 21800, { label: 'Media pool' }));
  const days = Array.from({ length: 90 }, (_, i) => (i === 81 ? 'down' : i === 66 || i === 74 || i === 84 ? 'partial' : 'ok'));
  put('days', `<div class="stack" style="gap:12px">${HL.days(days, { label: 'Paperless-ngx, ninety days' })}<p class="cluster" style="gap:10px">${HL.spark([180, 175, 190, 260, 182, 178, 171, 169, 185, 192, 188, 176, 181, 183, 179, 186, 190, 194, 188, 182, 177, 175, 180, 182])}<span class="num">182 ms today · 99.82% over 30 days</span></p></div>`);
  put('treemap', HL.treemap([{ label: 'Films', value: 8400, tone: 2 }, { label: 'Series', value: 7920, tone: 2 }, { label: 'Home videos', value: 1460, tone: 6 }, { label: 'Music', value: 1120, tone: 3 }, { label: 'Downloads', value: 412, tone: 4 }, { label: 'Books', value: 210, tone: 6 }, { label: 'Jellyfin’s cache', value: 182, tone: 1 }, { label: 'Snapshots', value: 129, tone: 5 }], { label: 'Media pool', h: 320 })); requestAnimationFrame(() => HL.treemapFit());
  put('notif', `<article class="notif is-critical"><span class="notif-mark" aria-hidden="true"></span><div class="notif-top"><span><span class="notif-level">Critical</span> · HomersLab · mail · ntfy</span><span>19:52</span></div><div class="notif-title">Study PC isn’t answering</div><div class="notif-body">The heartbeat stopped at 19:42. Attic NAS can’t reach it either.</div></article>`);
})();
