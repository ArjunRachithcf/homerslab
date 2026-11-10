// HomersLab prototypes: shared interaction pieces, on the design system's components. PROTOTYPE, throwaway.
// The step-up is the security moment the polish must never hide: HomersLab's own sheet says exactly what the passkey
// approves and which plan it is bound to; then the browser's passkey prompt (simulated here) asks for the passkey.
(() => {
  const esc = (v) => String(v ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]);
  const D = () => window.HL_DATA;
  const still = () => (window.HL ? HL.still() : matchMedia('(prefers-reduced-motion: reduce)').matches);

  // a plan id from its content: the same plan gets the same id, and any change gets a new one
  const planId = (obj) => { const s = JSON.stringify(obj); let h = 0x811c9dc5; for (let i = 0; i < s.length; i++) { h ^= s.charCodeAt(i); h = Math.imul(h, 0x01000193); } return 'p-' + (h >>> 0).toString(16).padStart(8, '0').slice(0, 6); };

  // the prototype's clock: it starts at the household's "now" and moves with real time
  const t0 = Date.now();
  const now = () => new Date(D().NOW.getTime() + (Date.now() - t0));
  const hhmm = (d) => `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`;
  const DAYS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'];
  const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
  const dayMonth = (d) => `${d.getDate()} ${MONTHS[d.getMonth()]}`;
  const weekdayDate = (d) => `${DAYS[d.getDay()].slice(0, 3)} ${dayMonth(d)}`;
  const poss = (n) => (/s$/i.test(n) ? `${n}’` : `${n}’s`); // Documents’ data, Immich’s data
  const gb = (v) => (v >= 1000 ? `${(v / 1000).toFixed(v >= 10000 ? 1 : 2)} TB` : v >= 10 ? `${v.toFixed(0)} GB` : `${v.toFixed(1)} GB`);
  const and = (xs) => (xs.length <= 1 ? xs.join('') : xs.slice(0, -1).join(', ') + ' and ' + xs[xs.length - 1]);

  // an app's own icon, as its definition ships it; a person's initial on their own hue
  const appIcon = (def, cls = '') => `<img class="app-icon ${def.iconFull ? 'is-full' : ''} ${cls}" src="${esc(def.icon)}" alt="" decoding="async">`;
  const avatar = (p, cls = '') => `<span class="avatar ${cls}" style="--h:${p.hue}" aria-hidden="true">${esc(p.short[0])}</span>`;

  // ---------- after every render: segmented thumbs spring from where they were; bars watch the scroll ----------
  const lastPos = new Map();
  function paint(root = document) {
    for (const seg of root.querySelectorAll('.segmented')) {
      const b = [...seg.querySelectorAll('button')];
      const i = Math.max(0, b.findIndex((x) => x.getAttribute('aria-pressed') === 'true' || x.getAttribute('aria-checked') === 'true' || x.getAttribute('aria-selected') === 'true'));
      const key = seg.dataset.seg, was = key ? lastPos.get(key) : undefined;
      const thumb = seg.querySelector('.segmented-thumb');
      if (thumb && was !== undefined && was !== i && !still()) {
        thumb.style.transition = 'none'; seg.style.setProperty('--pos', was); void thumb.offsetWidth; thumb.style.transition = '';
        requestAnimationFrame(() => seg.style.setProperty('--pos', i));
      } else seg.style.setProperty('--pos', i);
      if (key) lastPos.set(key, i);
    }
  }
  let titleObserver = null;
  function bars(bar, title) {
    if (!bar) return;
    const onScroll = () => bar.classList.toggle('is-scrolled', scrollY > 4);
    if (!bar.dataset.wired) { addEventListener('scroll', onScroll, { passive: true }); bar.dataset.wired = '1'; }
    onScroll();
    titleObserver?.disconnect();
    if (title && 'IntersectionObserver' in window) {
      titleObserver = new IntersectionObserver((es) => bar.classList.toggle('is-titled', !es[0].isIntersecting), { rootMargin: `-${bar.offsetHeight || 52}px 0px 0px 0px` });
      titleObserver.observe(title);
    } else bar.classList.remove('is-titled');
  }

  // ---------- a toast: one sentence about what just happened ----------
  function toast(msg, icon = 'check') {
    let host = document.querySelector('.toast-host');
    if (!host) { host = document.createElement('div'); host.className = 'toast-host'; host.setAttribute('role', 'status'); host.setAttribute('aria-live', 'polite'); document.body.appendChild(host); }
    const kind = icon === 'check' || icon === 'key' ? 'toast-ok' : icon === 'alert' || icon === 'trash' ? 'toast-error' : '';
    const t = document.createElement('div'); t.className = `toast ${kind}`; t.innerHTML = `${I(icon)}<span>${msg}</span>`;
    host.appendChild(t);
    setTimeout(() => { t.classList.add('is-leaving'); setTimeout(() => t.remove(), still() ? 0 : 220); }, 4200);
  }

  // ---------- a sheet with focus kept inside, Escape or the scrim to close; resolves with the button's value ----------
  const KIND = { primary: 'btn-primary', ghost: '', danger: 'btn-danger', destructive: 'btn-destructive', plain: 'btn-plain' };
  function dialog({ title, sub, body = '', actions = [], wide = false, mark = '' }) {
    return new Promise((resolve) => {
      const before = document.activeElement;
      const scrim = document.createElement('div'); scrim.className = 'scrim';
      const sheet = document.createElement('section'); sheet.className = `sheet ${wide ? 'sheet-wide' : ''}`;
      sheet.setAttribute('role', 'dialog'); sheet.setAttribute('aria-modal', 'true'); sheet.setAttribute('aria-labelledby', 'sheet-title'); sheet.tabIndex = -1;
      sheet.innerHTML = `<div class="sheet-grab" aria-hidden="true"></div>
        <div class="sheet-head"><div class="sheet-head-text">${mark}<h2 class="sheet-title" id="sheet-title">${title}</h2>${sub ? `<p class="sheet-sub">${sub}</p>` : ''}</div>
          <button type="button" class="sheet-close" data-dv="" aria-label="Close">${I('x')}</button></div>
        <div class="sheet-body">${body}</div>
        <div class="sheet-foot">${actions.map((a, i) => `<button type="button" class="btn ${KIND[a.kind] ?? ''}" data-dv="${esc(a.value)}" ${a.disabled ? 'disabled' : ''} ${i === actions.length - 1 ? 'data-default' : ''}>${a.icon ? I(a.icon) : ''}${a.label}</button>`).join('')}</div>`;
      document.body.append(scrim, sheet);
      document.documentElement.style.overflow = 'hidden';
      requestAnimationFrame(() => requestAnimationFrame(() => { scrim.classList.add('is-open'); sheet.classList.add('is-open'); }));
      // a body taller than the sheet scrolls: it takes focus from the keyboard, so it can be scrolled without a pointer
      requestAnimationFrame(() => { const b = sheet.querySelector('.sheet-body'); if (b && b.scrollHeight > b.clientHeight + 1) { b.tabIndex = 0; b.setAttribute('role', 'region'); b.setAttribute('aria-label', 'Details, scrolls'); } });
      const close = (v) => {
        document.removeEventListener('keydown', onKey, true);
        sheet.classList.remove('is-open'); scrim.classList.remove('is-open'); document.documentElement.style.overflow = '';
        setTimeout(() => { sheet.remove(); scrim.remove(); }, still() ? 0 : 420);
        before?.focus?.({ preventScroll: true }); resolve(v);
      };
      const focusables = () => [...sheet.querySelectorAll('button:not([disabled]), input:not([disabled]), select, textarea, a[href]')];
      const onKey = (e) => {
        if (e.key === 'Escape') { e.preventDefault(); close(null); }
        if (e.key === 'Tab') { const f = focusables(); if (!f.length) return; const i = f.indexOf(document.activeElement); if (e.shiftKey && i <= 0) { e.preventDefault(); f[f.length - 1].focus(); } else if (!e.shiftKey && i === f.length - 1) { e.preventDefault(); f[0].focus(); } }
      };
      document.addEventListener('keydown', onKey, true);
      sheet.addEventListener('click', (e) => { const b = e.target.closest('[data-dv]'); if (b && !b.disabled) close(b.dataset.dv || null); });
      scrim.addEventListener('click', () => close(null));
      dialog.onOpen?.(sheet);
      paint(sheet);
      setTimeout(() => (sheet.querySelector('input, select, textarea') || sheet.querySelector('[data-default]:not([disabled])') || sheet).focus({ preventScroll: true }), 40);
    });
  }

  // ---------- the browser's own passkey prompt, simulated; it is not HomersLab's UI, so it looks like the browser's ----------
  function osPrompt({ create = false, site, account }) {
    return new Promise((resolve) => {
      const before = document.activeElement;
      const s = document.createElement('div'); s.className = 'os-scrim';
      s.innerHTML = `<div class="os-prompt" role="dialog" aria-modal="true" aria-labelledby="os-title">
        <div class="os-bar"><span>${esc(site)}</span></div>
        <div class="os-body"><div class="os-key">${I('key', 'icon')}</div>
          <h3 id="os-title">${create ? 'Create a passkey' : 'Use your passkey'}</h3>
          <p>${create ? `Save a passkey for <b>${esc(account)}</b> on this device, or on a security key.` : `Confirm it’s you, <b>${esc(account)}</b>, with Touch ID or your security key.`}</p></div>
        <div class="os-actions"><button type="button" class="os-btn" data-os="0">Cancel</button><button type="button" class="os-btn go" data-os="1">${create ? 'Create' : 'Continue'}</button></div>
        <p class="os-note">Simulated: in the real product this is your browser’s own passkey prompt, not HomersLab’s.</p></div>`;
      document.body.appendChild(s);
      const close = (ok) => { s.remove(); document.removeEventListener('keydown', onKey, true); before?.focus?.({ preventScroll: true }); resolve(ok); };
      const onKey = (e) => { if (e.key === 'Escape') { e.preventDefault(); close(false); } };
      document.addEventListener('keydown', onKey, true);
      s.addEventListener('click', (e) => { const b = e.target.closest('[data-os]'); if (b) close(b.dataset.os === '1'); });
      s.querySelector('[data-os="1"]').focus();
    });
  }

  // ---------- step-up: one fresh passkey answer, bound to one plan id, that approves exactly the listed things ----------
  async function stepUp({ plan, what, title = 'Approve with your passkey', action = 'Use my passkey', sub = 'This is a risky change, so HomersLab asks for your passkey again, every time.' }) {
    const by = D().people[0];
    const body = `
      <ul class="group stepup-list" aria-label="What your passkey approves">${what.map((w) => `<li>${I(w.icon || 'check')}<span>${w.text}</span></li>`).join('')}</ul>
      <p class="stepup-bound">${I('lock')}<span>Bound to plan <span class="plan-id">${esc(plan.id)}</span>: ${esc(plan.summary)}. It approves nothing else, and lapses in 5 minutes. Signed in as ${esc(by.name)}.</span></p>`;
    const v = await dialog({ title, sub, body, mark: I('key', 'icon stepup-mark'), actions: [{ label: 'Cancel', value: 'no', kind: 'ghost' }, { label: action, value: 'yes', kind: 'primary', icon: 'key' }] });
    if (v !== 'yes') return null;
    const ok = await osPrompt({ site: D().household.adminUrl, account: by.mail });
    if (!ok) { toast('Nothing changed: the passkey prompt was cancelled.', 'info'); return null; }
    return { planId: plan.id, by: by.short, at: hhmm(now()) };
  }

  window.UI = { esc, poss, and, planId, now, hhmm, dayMonth, weekdayDate, gb, DAYS, MONTHS, appIcon, avatar, paint, bars, toast, dialog, osPrompt, stepUp, still };
})();
