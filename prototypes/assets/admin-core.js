// HomersLab Admin prototype: state, router, shell, the planner and shared renderers. PROTOTYPE, throwaway.
// One static app, hash routes, state in memory only. The install plan is its own page, #/plans/<plan id>: any change
// makes a new plan with a new id, and one passkey at Install approves the whole plan (decisions of round 1).
(() => {
  const D = HL_DATA;
  const { esc, planId, now, hhmm, gb, and, appIcon, avatar } = UI;
  const clone = (x) => JSON.parse(JSON.stringify(x));

  // ---------- state: in memory only ----------
  const S = {
    installed: clone(D.installed), removed: clone(D.removed), people: clone(D.people), devices: clone(D.devices), invites: clone(D.invites),
    nodes: clone(D.nodes), storage: clone(D.storage), signup: D.household.signup,
    drafts: {}, planTimes: {}, plans: {}, planHistory: {}, approvals: [], installs: {}, jobs: {}, events: [], ejected: [],
  };
  // what was installed earlier in this tab (an answered request, say) stays installed when Admin opens again
  for (const x of STORE.state().installs) if (!S.installed.some((a) => a.id === x.record.id)) { S.installed.push(x.record); S.removed = S.removed.filter((r) => r.id !== x.record.id); }
  const log = (e) => { S.events.unshift(`${hhmm(now())} ${e}`); S.events = S.events.slice(0, 8); Proto.refresh(); };

  const cat = (id) => D.catalog.find((a) => a.id === id);
  const inst = (id) => S.installed.find((a) => a.id === id);
  const rem = (id) => S.removed.find((a) => a.id === id);
  const node = (id) => S.nodes.find((n) => n.id === id);
  const person = (id) => S.people.find((p) => p.id === id);
  const NAMES = { plex: 'plex', photoprism: 'pictures', calibreweb: 'ebooks', freshrss: 'news', gramps: 'tree', immich: 'photos', paperless: 'documents', opencloud: 'files', bentopdf: 'pdf', jellyfin: 'films', navidrome: 'music', kavita: 'books', audiobookshelf: 'listen', homeassistant: 'house', donetick: 'chores', mealie: 'recipes', grocy: 'stock', vaultwarden: 'passwords', actual: 'budget', bills: 'bills', karakeep: 'links', memos: 'notes', outline: 'wiki', openwebui: 'assistant' };
  const host = (id) => `${NAMES[id] || id}.${D.household.domain}`;

  // ---------- the household's meshes (a prototype option, so the Headscale and Tailscale cases can be seen) ----------
  const MESH = {
    'nb+ts': { names: ['NetBird', 'Tailscale'], pub: 'netbird' },
    nb: { names: ['NetBird'], pub: 'netbird' },
    ts: { names: ['Tailscale'], pub: 'funnel' },
    hs: { names: ['Headscale'], pub: null },
    'nb+hs': { names: ['NetBird', 'Headscale'], pub: 'netbird' },
  };
  const mesh = () => MESH[Proto.get('mesh', 'nb+ts')] || MESH['nb+ts'];
  const meshWords = () => mesh().names.join(' and ');

  // ---------- audiences ----------
  const inAud = (aud, p) => !!(aud.everyone || (aud.admins && p.role === 'admin') || (aud.adults && (p.role === 'admin' || p.role === 'adult')) || (aud.kids && p.role === 'kid') || (aud.people || []).includes(p.id));
  const audPeople = (aud) => S.people.filter((p) => inAud(aud, p));
  function audWords(aud) {
    if (aud.everyone) return 'Everyone';
    const parts = [];
    if (aud.adults) parts.push('Adults'); else if (aud.admins) parts.push('Admins');
    if (aud.kids) parts.push(parts.length ? 'kids' : 'Kids');
    const named = (aud.people || []).map(person).filter((p) => p && !(aud.adults && p.role !== 'kid') && !(aud.kids && p.role === 'kid')).map((p) => p.short);
    return parts.length ? and([...parts, ...named]) : and(named) || 'Nobody';
  }

  // ---------- nodes: what is free, and whether an app fits ----------
  const memUsed = (n) => n.baseMemGB + S.installed.filter((a) => a.node === n.id).reduce((t, a) => t + a.memNowGB, 0);
  const memFree = (n) => +(n.memGB - memUsed(n)).toFixed(1);
  const appsDisk = (n) => n.disks.find((d) => d.apps);
  function fit(def, n) {
    const r = def.resources, why = [];
    const free = memFree(n), disk = appsDisk(n), diskFree = disk.sizeGB - disk.usedGB;
    let ok = true;
    if (!r.arch.includes(n.arch)) { ok = false; why.push(`No ${n.arch} build`); }
    if (free < r.memGB) { ok = false; why.push(`Not enough memory: needs ${r.memGB} GB, ${free} GB free`); }
    if (r.gpu === 'required' && !(n.gpu && n.gpu.kind === 'nvidia')) { ok = false; why.push(`Needs a GPU: ${r.gpuMin || 'NVIDIA'}`); }
    if (diskFree < r.diskGB) { ok = false; why.push('Not enough disk'); }
    let score = ok ? free / 10 : -1;
    const good = [];
    if (ok && r.gpu !== 'none' && n.gpu) { score += n.gpu.kind === 'nvidia' ? 3 : 1; good.push(`has the ${n.gpu.kind === 'nvidia' ? 'NVIDIA GPU' : 'Intel GPU'}`); }
    if (ok && def.extras.some((x) => x.id === 'usb') && n.usb) { score += 4; good.push('has the Zigbee stick'); }
    if (ok && def.needsStorage) { const st = S.storage.find((s) => s.id === def.needsStorage); if (st && st.node === n.id) { score += 2; good.push(`holds the ${st.name}`); } }
    if (ok) good.push(`${free} GB of memory free`);
    return { ok, why, good, score, free, diskFree };
  }
  const suggest = (def) => S.nodes.map((n) => ({ n, f: fit(def, n) })).filter((x) => x.f.ok).sort((a, b) => b.f.score - a.f.score)[0]?.n;

  // ---------- the planner: an app definition plus the household's state -> one immutable plan with its own id ----------
  function draft(appId) {
    if (!S.drafts[appId]) { const def = cat(appId); S.drafts[appId] = { node: suggest(def)?.id || S.nodes[0].id, audience: clone(def.defaultAudience), extras: {} }; }
    return S.drafts[appId];
  }
  function buildPlan(appId) {
    const def = cat(appId), dr = draft(appId), n = node(dr.node), f = fit(def, n);
    const extras = def.extras.map((x) => {
      const applicable = x.needs === 'gpu' ? !!n.gpu : x.id === 'usb' ? !!n.usb : true;
      const chosen = applicable && (x.optional ? dr.extras[x.id] !== false : true);
      const fill = (s) => (s || '').replace(/\{node\}/g, n.name).replace(/\{gpu\}/g, n.gpu ? n.gpu.name : '');
      return { ...x, applicable, chosen, text: fill(x.words), detailText: x.id === 'usb' ? n.usb || '' : fill(x.detail) };
    });
    const chosen = extras.filter((x) => x.chosen);
    const people = audPeople(dr.audience);
    const content = { app: appId, v: def.version, node: n.id, people: people.map((p) => p.id), extras: chosen.map((x) => x.id) };
    const id = planId(content);
    if (!S.planTimes[id]) S.planTimes[id] = hhmm(now());
    S.plans[id] = appId;
    const hist = (S.planHistory[appId] = S.planHistory[appId] || []);
    if (hist[hist.length - 1] !== id) hist.push(id);
    return { id, kind: 'install', appId, def, node: n, fit: f, audience: dr.audience, people, extras, chosen, risky: chosen.length > 0, host: host(appId), madeAt: S.planTimes[id], summary: `Install ${def.name} on ${n.name}` };
  }
  const approvalFor = (pid) => S.approvals.find((a) => a.planId === pid);

  // ---------- small renderers ----------
  const TIER = { verified: 'Verified', community: 'Community', third: 'Third-party' };
  const TIER_ICON = { verified: 'shield', community: 'people', third: 'key' };
  const tier = (t) => `<span class="tier">${I(TIER_ICON[t])}${TIER[t]}</span>`;
  const SIGNIN = { native: 'Native sign-in', guarded: 'Guarded at the edge' };
  const ROLE = { admin: 'Admin', adult: 'Adult', kid: 'Kid' };
  const DEVICE_ICON = { phone: 'phone', laptop: 'laptop', tablet: 'tablet', tv: 'tv', desktop: 'desktop' };
  const chev = () => I('chevronRight', 'icon row-chev');

  // a bar at its true ratio, both ends labelled; what an app would add is the accent ("13.1 + 6.0 of 32.0 GB")
  function meter({ label, used, add = 0, total, fmt = gb, unit = '', note }) {
    const pu = Math.min(100, (used / total) * 100), pa = Math.max(0, Math.min(100 - pu, (add / total) * 100));
    const over = used + add > total, full = !add && used / total > 0.9;
    return `<div class="meter ${add ? 'has-add' : ''} ${over ? 'is-over' : ''} ${full ? 'is-full' : ''}" style="--used:${pu.toFixed(2)}%;--add:${pa.toFixed(2)}%">
      <div class="meter-head"><b>${esc(label)}</b><span class="num">${add ? `${fmt(used)} + ${fmt(add)} of ${fmt(total)}${unit}` : `${fmt(used)} of ${fmt(total)}${unit}`}</span></div>
      <div class="meter-track" role="img" aria-label="${esc(label)}: ${fmt(used)}${unit} used${add ? `, ${fmt(add)}${unit} more for this app` : ''}, of ${fmt(total)}${unit}"><span class="meter-fill"></span>${add ? '<span class="meter-add"></span>' : ''}</div>
      ${note ? `<div class="meter-head"><span>${note}</span></div>` : ''}</div>`;
  }

  // twelve Sundays of restore tests, oldest first: a bar per week, its date and result in its title and the label
  function weeks(results) {
    const passed = results.filter((r) => r === 'pass').length, failed = results.map((r, i) => (r === 'fail' ? D.SUNDAYS[i] : null)).filter(Boolean);
    const label = `Restore tests on the last 12 Sundays: ${passed} passed${failed.length ? `, ${failed.length} failed (${failed.map((d) => UI.dayMonth(d)).join(', ')})` : ''}${results.includes(null) ? `, ${results.filter((r) => r === null).length} before it was installed` : ''}`;
    return `<div class="strip" role="img" aria-label="${label}">${results.map((r, i) => `<span class="strip-cell ${r === 'fail' ? 'is-fail' : r === null ? 'is-none' : ''}" title="${UI.weekdayDate(D.SUNDAYS[i])}: ${r === 'pass' ? 'passed' : r === 'fail' ? 'failed' : 'not installed yet'}"></span>`).join('')}</div>`;
  }
  const weeksAxis = () => `<div class="strip-axis" aria-hidden="true"><span>${UI.dayMonth(D.SUNDAYS[0])}</span><span>${UI.dayMonth(D.SUNDAYS[11])}</span></div>`;
  const weeksLegend = (r) => `<div class="strip-legend"><span><i class="strip-cell" aria-hidden="true"></i>Passed</span><span><i class="strip-cell is-fail" aria-hidden="true"></i>Failed</span>${r.includes(null) ? '<span><i class="strip-cell is-none" aria-hidden="true"></i>Not installed yet</span>' : ''}</div>`;

  // who can reach an app, and how: three ways in, each open or closed
  function reach({ name, nodeName, exposure, publicAllowed = true, lanExtra = false }) {
    const m = mesh(), pub = exposure === 'public';
    const pubFrom = !pub ? (m.pub ? 'The internet' : 'The internet') : m.pub === 'netbird' ? 'The internet, through NetBird’s public path' : 'The internet, through Tailscale Funnel';
    const pubSub = !pub ? (!m.pub ? 'Headscale has no public path' : publicAllowed ? 'Not public: you can open it from Exposure' : 'Its definition allows mesh only') : `Strangers reach the sign-in page only; TLS ends on ${nodeName}`;
    const row = (icon, from, sub, open) => `<div class="row"><span class="row-lead">${I(icon, 'icon icon-md')}</span><span class="row-main"><span class="row-title wrap">${from}</span><span class="row-sub wrap">${sub}</span></span><span class="reach-end ${open ? 'is-open' : 'is-closed'}">${open ? `${I('check')}Reaches it` : `${I('x')}Closed`}</span></div>`;
    return `<div class="group-block"><div class="group-head"><h3>Who can reach ${esc(name)}</h3></div><div class="group reach lead-sm">
      ${row('mesh', 'The household’s devices', `Over ${esc(meshWords())}, each signed in; the edge on ${esc(nodeName)} asks for the household account`, true)}
      ${row('globe', pubFrom, pubSub, pub)}
      ${row('home', 'The home network', lanExtra ? `${esc(name)} may look out onto it to find devices; nothing on it can reach ${esc(name)}` : 'Even from inside the house', false)}
    </div><p class="group-foot">No port is open on the router, ever. ${pub ? 'The public path never reads the traffic.' : 'Only devices on the mesh can open it.'}</p></div>`;
  }

  // ---------- the shell ----------
  const NAV = [
    { href: '#/apps', label: 'Apps', icon: 'apps', match: /^#\/apps/ },
    { href: '#/store', label: 'Store', icon: 'store', match: /^#\/(store|catalog|plans|install)/ },
    { href: '#/people', label: 'People', icon: 'people', match: /^#\/people/ },
    { href: '#/nodes', label: 'Nodes', icon: 'nodes', match: /^#\/(nodes|disks|analyzer)/ },
    { href: '#/backups', label: 'Backups', icon: 'backups', match: /^#\/backups/ },
  ];
  const narrow = matchMedia('(max-width: 1000px)');
  function shell(r, back) {
    const me = S.people[0], h = location.hash || '#/apps';
    const cur = NAV.find((n) => n.match.test(h));
    const settings = /^#\/settings/.test(h);
    const updates = S.installed.filter((a) => a.update).length, asks = STORE.open().length;
    document.querySelector('#side').innerHTML = `
      <a class="brand" href="#/apps" aria-label="HomersLab Admin">${LOCKUP}</a>
      <div class="sidebar-scope"><b>${esc(D.household.name)}</b><span>Admin · on the mesh</span></div>
      <nav class="nav" aria-label="Admin">${NAV.map((n) => `<a class="nav-item" href="${n.href}" ${n === cur ? 'aria-current="page"' : ''}>${I(n.icon)}<span>${n.label}</span>${n.label === 'Store' && updates + asks ? `<span class="nav-count" aria-label="${updates} ${updates === 1 ? 'update' : 'updates'} ready, ${asks} ${asks === 1 ? 'request' : 'requests'} waiting">${updates + asks}</span>` : ''}</a>`).join('')}</nav>
      <div class="sidebar-foot">
        <nav class="nav" aria-label="More"><a class="nav-item" href="#/settings" ${settings ? 'aria-current="page"' : ''}>${I('settings')}<span>Settings</span></a>
          <a class="nav-item" href="home.html">${I('home')}<span>Open Home</span></a></nav>
        <div class="me-row">${avatar(me)}<span class="me-text"><b>${esc(me.name)}</b><span>Household admin</span></span></div>
      </div>`;
    document.querySelector('#tabs').innerHTML = NAV.map((n) => `<a class="tab" href="${n.href}" ${n === cur ? 'aria-current="page"' : ''}>${I(n.icon)}<span>${n.label}</span></a>`).join('');
    HL.lens(document.querySelector('#tabs'));
    document.querySelector('#bar').innerHTML = `
      ${back ? `<a class="topbar-back" href="${back.href}">${I('chevronBack')}<span>${esc(back.label)}</span></a>` : `<a class="brand a-phone-brand" href="#/apps" aria-label="HomersLab Admin, apps">${MARK}</a>`}
      <span class="topbar-title" aria-hidden="true"></span>
      <div class="topbar-end">
        <div class="a-search a-wide" role="search">${I('search')}<label class="sr-only" for="search">Search apps, the Store, people and nodes</label><input id="search" type="search" role="combobox" aria-autocomplete="list" placeholder="Search" autocomplete="off" aria-controls="results" aria-expanded="false"><kbd aria-hidden="true">/</kbd></div>
        <span class="a-reach" title="Admin answers only over the mesh. You are on it as Inês’s laptop."><span class="dot dot-green" aria-hidden="true"></span><span>On the mesh</span><span class="long">· Inês’s laptop</span></span>
        <button type="button" class="icon-btn a-me-btn" data-act="search-open" aria-label="Search">${I('search')}</button>
        <button type="button" class="icon-btn a-me-btn" data-act="me-open" aria-haspopup="dialog" aria-expanded="false" aria-controls="me-menu" aria-label="${esc(me.name)}: your account and Settings" style="padding:0">${avatar(me)}</button>
      </div>`;
    document.querySelector('#me-menu').innerHTML = `
      <div class="menu-who">${avatar(me, 'avatar-lg')}<span><b>${esc(me.name)}</b><small>Household admin · on ${esc(mesh().names[0])}</small></span></div>
      <div class="menu-sep"></div>
      <a class="menu-item" href="#/settings">${I('settings')}<span>Settings</span></a>
      <a class="menu-item" href="home.html">${I('home')}<span>Open Home</span></a>
      <button type="button" class="menu-item" data-act="sign-out">${I('signout')}<span>Sign out<small>On this device</small></span></button>`;
    document.body.classList.toggle('has-tabbar', narrow.matches);
    wireSearch();
  }
  narrow.addEventListener('change', () => document.body.classList.toggle('has-tabbar', narrow.matches));

  // ---------- search: installed apps, the catalog, people, nodes ----------
  function searchResults(q) {
    q = q.trim().toLowerCase(); if (!q) return [];
    const out = [];
    for (const a of S.installed) { const d = cat(a.id); if ((d.purpose + ' ' + d.name).toLowerCase().includes(q)) out.push({ href: `#/apps/${a.id}`, label: d.name, sub: d.purpose, kind: 'Installed', def: d }); }
    for (const d of D.catalog) if (!inst(d.id) && (d.purpose + ' ' + d.name + ' ' + d.blurb).toLowerCase().includes(q)) out.push({ href: `#/store/app/${d.id}`, label: d.name, sub: d.purpose, kind: 'Store', def: d });
    for (const p of S.people) if (p.name.toLowerCase().includes(q)) out.push({ href: `#/people/${p.id}`, label: p.name, sub: ROLE[p.role], kind: 'Person', p });
    for (const n of S.nodes) if (n.name.toLowerCase().includes(q)) out.push({ href: `#/nodes/${n.id}`, label: n.name, sub: n.os, kind: 'Node' });
    return out.slice(0, 7);
  }
  function wireSearch() {
    const input = document.querySelector('#search'), box = document.querySelector('#results');
    const place = () => { const r = input.closest('.a-search').getBoundingClientRect(); box.style.top = `${r.bottom + 8}px`; box.style.left = `${Math.max(16, Math.min(r.right - 420, innerWidth - 436))}px`; };
    const draw = () => {
      const r = searchResults(input.value), on = !!input.value.trim();
      if (on) place();
      box.classList.toggle('is-open', on); input.setAttribute('aria-expanded', String(on));
      box.innerHTML = r.length ? r.map((x) => `<a class="menu-item" role="option" href="${x.href}">${x.def ? appIcon(x.def, 'app-icon-sm') : x.p ? avatar(x.p, 'avatar-sm') : I('nodes')}<span>${esc(x.label)}<small>${esc(x.sub)}</small></span><span class="kind">${x.kind}</span></a>`).join('') : `<p class="none">Nothing called “${esc(input.value)}”.</p>`;
    };
    input.addEventListener('input', draw);
    input.addEventListener('keydown', (e) => { if (e.key === 'Enter') { const a = box.querySelector('a'); if (a) { location.hash = a.getAttribute('href'); input.value = ''; draw(); input.blur(); } } if (e.key === 'Escape') { input.value = ''; draw(); input.blur(); } });
    input.addEventListener('blur', () => setTimeout(() => { if (!box.matches(':hover')) { box.classList.remove('is-open'); input.setAttribute('aria-expanded', 'false'); } }, 150));
  }
  document.addEventListener('click', (e) => { const a = e.target.closest('#results a'); if (a) { const i = document.querySelector('#search'); if (i) i.value = ''; document.querySelector('#results').classList.remove('is-open'); } });
  document.addEventListener('keydown', (e) => { if (e.key === '/' && !e.target.closest('input, textarea, select') && !document.querySelector('.scrim')) { e.preventDefault(); actions['search-open'](); } });

  // ---------- router ----------
  const routes = [];
  const route = (re, fn, opts = {}) => routes.push({ re, fn, ...opts });
  let firstRender = true;
  function render() {
    const h = location.hash || '#/apps';
    const [path, query] = h.split('?');
    const q = new URLSearchParams(query || '');
    const r = routes.find((x) => x.re.test(path)) || routes[0];
    const m = path.match(r.re) || [];
    if (r.redirect) { const to = r.redirect(m, q); if (to) { history.replaceState(null, '', location.pathname + location.search + to); return render(); } }
    const back = typeof r.back === 'function' ? r.back(m, q) : r.back;
    shell(r, back);
    document.querySelector('#results').classList.remove('is-open');
    document.querySelector('#me-menu').classList.remove('is-open');
    const main = document.querySelector('#main');
    main.innerHTML = `<div class="a-page">${r.fn(m, q)}</div>`;
    r.after?.(m, q);
    const h1 = main.querySelector('h1');
    document.querySelector('#bar .topbar-title').textContent = h1?.textContent.trim() || '';
    document.title = `${h1?.textContent.trim() || 'Admin'} · HomersLab Admin`;
    UI.paint(main);
    UI.bars(document.querySelector('#top'), h1);
    if (!firstRender) { window.scrollTo(0, 0); h1?.setAttribute('tabindex', '-1'); h1?.focus({ preventScroll: true }); }
    firstRender = false;
    Proto.refresh();
  }
  const go = (h) => { if (location.hash === h) render(); else location.hash = h; };

  // ---------- screen changes: push deeper, pop back, cross-fade to another section; an app's icon flies along ----------
  const SECTION = (p) => { const s = (p.split('/')[1] || 'apps'); return s === 'plans' || s === 'install' || s === 'catalog' ? 'store' : s === 'disks' || s === 'analyzer' ? 'nodes' : s; };
  // the Store's segments (Discover, Updates, Requests) sit side by side; an app's page is one deeper, its plan deeper still
  const DEPTH = (p) => { const parts = p.split('/').filter((x) => x && x !== '#'); return parts[0] === 'plans' ? 3 : parts[0] === 'install' ? 4 : parts[0] === 'store' ? (parts[1] === 'app' || parts[1] === 'browse' ? 2 : 1) : parts[0] === 'nodes' && (parts[1] === 'disks' || parts[1] === 'uptime') ? 1 : parts[0] === 'disks' ? parts.length : parts[0] === 'analyzer' ? 2 : parts.length; };
  let lastPath = null, pendingMorph = null;
  const heroFrom = (el) => { if (!el) return null; const icon = el.querySelector('.app-icon'), name = el.querySelector('[data-name]'); return icon ? [[icon, 'hero', '.a-ident .app-icon'], ...(name ? [[name, 'hero-title', '.a-ident h1']] : [])] : null; };
  function navigate() {
    const path = (location.hash || '#/apps').split('?')[0];
    let type = 'fade';
    if (lastPath && SECTION(lastPath) === SECTION(path) && lastPath !== path) type = DEPTH(path) > DEPTH(lastPath) ? 'push' : DEPTH(path) < DEPTH(lastPath) ? 'pop' : 'fade';
    let morph = pendingMorph; pendingMorph = null;
    if (!morph && type === 'pop') { const id = document.querySelector('.a-ident[data-app-id]')?.dataset.appId; const icon = document.querySelector('.a-ident .app-icon'); if (id && icon) morph = [[icon, 'hero', `[data-app-id="${id}"]:not(.a-ident) .app-icon`]]; }
    lastPath = path;
    if (morph) HL.morph(render, morph, type); else HL.run(render, { type });
  }
  // a list that gains or loses a row: the others glide into place
  const renderList = () => HL.list(render, document.querySelector('#main'));
  // a card or a row that opens an app's page: its icon flies to the page's header
  document.addEventListener('click', (e) => { const a = e.target.closest('a[data-morph]'); if (a && !e.defaultPrevented) pendingMorph = heroFrom(a); }, true);
  // a re-render that lets a switch or a chip finish its spring first
  const soon = (fn = render) => setTimeout(fn, UI.still() ? 0 : 260);

  // ---------- events: one delegated handler, actions registered by each screen ----------
  const actions = {};
  const act = (name, fn) => { actions[name] = fn; };
  document.addEventListener('click', (e) => {
    const t = e.target.closest('[data-act]'); if (!t) return;
    const fn = actions[t.dataset.act]; if (!fn) return;
    e.preventDefault(); fn(t, e);
  });
  document.addEventListener('change', (e) => { const t = e.target.closest('[data-change]'); if (t && actions[t.dataset.change]) actions[t.dataset.change](t, e); });
  act('search-open', () => {
    const s = document.querySelector('.a-search'); s.classList.remove('a-wide');
    s.style.cssText = 'position:fixed;left:16px;right:16px;top:8px;width:auto;z-index:45';
    const i = document.querySelector('#search'); i.focus();
    i.addEventListener('blur', () => setTimeout(() => { if (!i.value) { s.style.cssText = ''; s.classList.add('a-wide'); } }, 160), { once: true });
  });
  act('me-open', (t) => {
    const m = document.querySelector('#me-menu'), on = !m.classList.contains('is-open');
    if (on) { const r = t.getBoundingClientRect(); m.style.top = `${r.bottom + 8}px`; m.style.right = `${Math.max(16, document.documentElement.clientWidth - r.right)}px`; m.style.left = 'auto'; }
    m.classList.toggle('is-open', on); t.setAttribute('aria-expanded', String(on));
  });
  document.addEventListener('click', (e) => { const m = document.querySelector('#me-menu'); if (m.classList.contains('is-open') && !e.target.closest('#me-menu, [data-act="me-open"]')) m.classList.remove('is-open'); });
  document.addEventListener('keydown', (e) => { if (e.key === 'Escape') { document.querySelector('#me-menu').classList.remove('is-open'); document.querySelector('#results').classList.remove('is-open'); } });
  act('sign-out', () => UI.toast('Prototype: you would be signed out of Admin on this device.', 'info'));
  act('open-app', (t) => UI.toast(`In the real product this opens ${esc(t.dataset.host)} in a new tab.`, 'external'));
  act('copy', () => UI.toast('Copied.'));
  act('row', (t, e) => { pendingMorph = heroFrom(t); if (e.target.closest('a, button')) { if (e.target.closest('a')) location.hash = e.target.closest('a').getAttribute('href'); else pendingMorph = null; return; } go(t.dataset.href); });

  // ---------- the prototype bar: options and state ----------
  Proto.option({ key: 'mesh', label: 'The household’s meshes', def: 'nb+ts', values: [{ v: 'nb+ts', name: 'NetBird + Tailscale' }, { v: 'nb', name: 'NetBird' }, { v: 'ts', name: 'Tailscale' }, { v: 'hs', name: 'Headscale' }, { v: 'nb+hs', name: 'NetBird + Headscale' }], note: 'Changes what “public” can mean on an app’s page.' });
  Proto.option({ key: 'outcome', label: 'How the next install ends', def: 'ok', values: [{ v: 'ok', name: 'Healthy' }, { v: 'fail', name: 'Fails its first health check' }], note: 'A failed first health check undoes the whole install.' });
  Proto.state(() => ({
    route: location.hash || '#/apps',
    'apps installed': S.installed.map((a) => a.id).join(', '),
    'apps removed': S.removed.map((a) => a.id).join(', ') || '—',
    'plans made': Object.entries(S.planHistory).map(([k, v]) => `${k}: ${v.join(' → ')}`).join('; ') || '—',
    'passkey approvals': S.approvals.map((a) => `${a.planId} by ${a.by} at ${a.at}`).join('; ') || 'none yet',
    installs: Object.entries(S.installs).map(([k, v]) => `${k}: ${v.done ? (v.failed ? 'failed, undone' : 'done') : 'step ' + (v.step + 1)}`).join('; ') || '—',
    'sign-up': S.signup,
    events: S.events.join(' · ') || '—',
  }));
  Proto.onChange((k) => { if (k === 'mesh' || k === 'theme') render(); });

  window.A = { D, S, cat, inst, rem, node, person, host, mesh, meshWords, and, audPeople, audWords, inAud, memUsed, memFree, appsDisk, fit, suggest, draft, buildPlan, approvalFor,
    appIcon, avatar, tier, TIER, SIGNIN, ROLE, DEVICE_ICON, chev, meter, weeks, weeksAxis, weeksLegend, reach, route, render, renderList, go, soon, act, actions, log, clone,
    morphTo(pairs) { pendingMorph = pairs; } };

  window.addEventListener('hashchange', navigate);
  window.addEventListener('DOMContentLoaded', () => { Proto.start(); lastPath = (location.hash || '#/apps').split('?')[0]; render(); });
})();
