// HomersLab Home prototype: the apps a person may open, with their state. PROTOTYPE, throwaway.
// Home is unprivileged: it reads only a projection of apps, audiences and health, and can't change anything.
// Prototype options (State in the bar): who is looking, on the mesh or away, and app states. Kids get their own
// layout: bigger tiles that say what each app is for (decision of round 1).
(() => {
  const D = HL_DATA, { esc, and, toast, osPrompt, now, appIcon, avatar, paint, bars } = UI;
  // Home's projection: the household's apps, Immich installed today
  const APPS = [
    { id: 'immich', audience: { adults: true }, exposure: 'mesh' },
    ...D.installed.map((a) => ({ id: a.id, audience: a.audience, exposure: a.exposure })),
  ];
  // what Admin installed earlier in this tab (an answered request, say) is on Home too
  for (const x of STORE.state().installs) if (!APPS.some((a) => a.id === x.record.id)) APPS.push({ id: x.record.id, audience: x.record.audience, exposure: x.record.exposure, fresh: true });
  const ST = STORE;
  const cat = (id) => D.catalog.find((a) => a.id === id);
  const NAMES = { audiobookshelf: 'listen', mealie: 'recipes', grocy: 'stock', gramps: 'tree', plex: 'plex', immich: 'photos', paperless: 'documents', jellyfin: 'films', navidrome: 'music', kavita: 'books', donetick: 'chores', actual: 'budget', karakeep: 'links' };
  const host = (id) => `${NAMES[id] || id}.${D.household.domain}`;
  const inAud = (aud, p) => !!(aud.everyone || (aud.admins && p.role === 'admin') || (aud.adults && (p.role === 'admin' || p.role === 'adult')) || (aud.kids && p.role === 'kid') || (aud.people || []).includes(p.id));
  const MESHNAME = { netbird: 'NetBird', tailscale: 'Tailscale' };
  const admin = D.people.find((p) => p.role === 'admin');
  // the panels, in reading order; each catalog category belongs to one
  const GROUPS = [
    { id: 'watch', title: 'Watch, listen, read', cats: ['films', 'music', 'books'] },
    { id: 'files', title: 'Photos, documents and links', cats: ['photos', 'documents', 'knowledge'] },
    { id: 'home', title: 'Home and money', cats: ['home', 'money', 'passwords', 'assistants'] },
  ];

  Proto.option({ key: 'as', label: 'Who is looking', def: 'tomas', values: D.people.map((p) => ({ v: p.id, name: `${p.short} (${p.role}${p.age ? ', ' + p.age : ''})` })) });
  Proto.option({ key: 'where', label: 'Where their phone is', def: 'mesh', values: [{ v: 'mesh', name: 'On the mesh' }, { v: 'away', name: 'Away, not on the mesh' }], note: 'Home is public behind the household account; most apps are mesh only.' });
  Proto.option({ key: 'apps', label: 'App states', def: 'ok', values: [{ v: 'ok', name: 'All working' }, { v: 'updating', name: 'Jellyfin updating' }, { v: 'down', name: 'Navidrome not working' }] });

  const me = () => D.people.find((p) => p.id === Proto.get('as', 'tomas')) || D.people[1];
  Proto.option({ key: 'window', label: 'Maintenance window', def: 'no', values: [{ v: 'no', name: 'Not now' }, { v: 'yes', name: 'Running (02:40)' }], note: 'Planned work shows “updating” on Home, with when it ends.' });
  // ---------- hiding one of two apps for one purpose: each person's own Home only, kept in this browser here ----------
  const hidKey = (p) => `hl-home-hidden:${p.id}`;
  const hidden = (p = me()) => { try { return JSON.parse(localStorage.getItem(hidKey(p)) || '[]'); } catch (e) { return []; } };
  const setHidden = (list, p = me()) => { try { localStorage.setItem(hidKey(p), JSON.stringify(list)); } catch (e) {} };
  // a purpose where this person sees two or more apps, before hiding
  const pairs = (p, all) => { const by = {}; for (const x of all) (by[x.d.purpose] = by[x.d.purpose] || []).push(x); return Object.values(by).filter((xs) => xs.length > 1); };
  const away = () => Proto.get('where', 'mesh') === 'away';
  function stateOf(a) {
    const s = Proto.get('apps', 'ok'), d = cat(a.id);
    if ((s === 'updating' || Proto.get('window', 'no') === 'yes') && a.id === 'jellyfin') return { kind: 'updating', dot: 'orange', words: 'Updating until 03:00', kidWords: 'Updating until 03:00', long: `${d.name} is updating until about 03:00.` };
    if (s === 'down' && a.id === 'navidrome') return { kind: 'down', dot: 'red', words: 'Not working', kidWords: 'Not working right now', long: `${d.name} isn’t working right now. ${admin.short} has been told.` };
    if (away() && a.exposure !== 'public') return { kind: 'away', words: 'Needs the mesh' };
    return null;
  }
  const device = (p) => D.devices.find((d) => d.person === p.id && (d.kind === 'phone' || d.kind === 'tablet'));
  const greet = () => { const h = now().getHours(); return h < 5 ? 'Good evening' : h < 12 ? 'Good morning' : h < 18 ? 'Good afternoon' : 'Good evening'; };

  // choose spans for the panels after the first so that panels in one row end level (12-column grid)
  function layout(groups) {
    const out = [];
    const rowsOf = (n, cols) => Math.ceil(n / cols);
    const [first, ...rest] = groups;
    if (first) out.push({ g: first, span: 12, cols: Math.min(3, first.items.length) });
    for (let i = 0; i < rest.length; i += 2) {
      const a = rest[i], b = rest[i + 1];
      if (!b) { out.push({ g: a, span: 12, cols: Math.min(3, a.items.length) }); break; }
      const tries = [[8, 2, 4, 1], [9, 3, 3, 1], [6, 1, 6, 1], [6, 2, 6, 2], [4, 1, 8, 2], [3, 1, 9, 3]];
      const fit = tries.find(([, ca, , cb]) => rowsOf(a.items.length, ca) === rowsOf(b.items.length, cb) && a.items.length >= ca && b.items.length >= cb);
      if (fit) { out.push({ g: a, span: fit[0], cols: fit[1] }, { g: b, span: fit[2], cols: fit[3] }); }
      else { out.push({ g: a, span: 12, cols: Math.min(3, a.items.length) }, { g: b, span: 12, cols: Math.min(3, b.items.length) }); }
    }
    return out;
  }

  let first = true, lastFact = null;
  function render() {
    if (location.hash.startsWith('#/ask')) return renderAsk();
    const p = me(), kid = p.role === 'kid', isAway = away();
    const seen = APPS.filter((a) => inAud(a.audience, p)).map((a) => ({ a, d: cat(a.id), st: stateOf(a) }));
    const hid = kid ? [] : hidden(p), duo = kid ? [] : pairs(p, seen);
    const mine = seen.filter((x) => !hid.includes(x.a.id));
    const trouble = mine.find((x) => x.st && (x.st.kind === 'down' || x.st.kind === 'updating'));
    const reachable = mine.filter((x) => !x.st || x.st.kind !== 'away');
    const dev = device(p), mesh = dev ? MESHNAME[dev.mesh] : 'the mesh app';
    const fact = kid ? 'What would you like to do?' : trouble ? (trouble.st.kind === 'down' ? `${trouble.d.name} isn’t working.` : `${trouble.d.name} is updating.`) : isAway ? 'You’re away from the mesh.' : mine.length === 1 ? 'Your app is working.' : 'Everything is working.';
    const sub = kid ? '' : trouble ? `${trouble.st.kind === 'down' ? `${admin.short} has been told.` : 'It will be back in a few minutes.'}${mine.length > 1 ? ' The rest are working.' : ''}`
      : isAway ? `${reachable.length} of ${mine.length} open from here; the others need the mesh.` : `Your apps at ${D.household.domain}`;
    const R = (i) => (first ? ` rise" style="--i:${i}` : '');

    // the bar: the household's word, which gives way to the greeting on scroll; the person's menu
    document.querySelector('#bar').innerHTML = `
      <a class="brand h-brand" href="home.html${location.search}" aria-label="Home, ${esc(D.household.name)}">${MARK}<span class="names"><span class="word">${esc(D.household.short)}</span><span class="mini" aria-hidden="true">${greet()}, ${esc(p.short)}</span></span></a>
      <div class="topbar-end"><button type="button" class="h-me" id="me-btn" aria-haspopup="dialog" aria-expanded="false" aria-controls="me-menu">${avatar(p)}<span class="h-me-name">${esc(p.name)}</span></button></div>`;

    const away1 = isAway ? `<div class="notice glass h-away${R(3)}" role="status">${I('wifiOff')}<span class="notice-body"><b>You’re away from the mesh.</b>${reachable.length ? `${esc(and(reachable.map((x) => x.d.name)))} ${reachable.length === 1 ? 'opens' : 'open'} from anywhere. ` : ''}The others open once ${esc(mesh)} is on${dev ? `, on this ${esc(dev.kind)}` : ''}.</span><span class="notice-actions"><button type="button" class="btn btn-sm" data-h="connect">How to connect</button></span></div>` : '';

    let body;
    if (!mine.length) {
      body = `<section class="panel glass h-none${R(3)}" aria-labelledby="h-none"><div class="empty"><p class="empty-title" id="h-none">Nothing here yet</p><p class="empty-text">Your household account has no apps yet. Ask ${esc(admin.short)} if you think that’s wrong.</p></div></section>`;
    } else if (kid) {
      body = `<section aria-label="Your apps"><div class="kid-tiles">${mine.map(({ a, d, st }, i) => `
        <button type="button" class="kid-tile glass glass-react ${st ? 'is-' + st.kind : ''}${R(3 + i)}" data-h="open" data-id="${a.id}" aria-label="${esc(d.name)}: ${esc(d.kidFor || d.homeFor)}${st ? '. ' + esc(st.kidWords || st.words) : ''}">
          ${appIcon(d)}<span class="kid-tile-name">${esc(d.name)}</span><span class="kid-tile-for">${esc(d.kidFor || d.homeFor)}</span>
          ${st ? `<span class="kid-tile-state">${st.kind === 'away' ? I('lock', 'icon icon-sm') : `<span class="dot dot-${st.dot}"></span>`}${esc(st.kidWords || st.words)}</span>` : ''}</button>`).join('')}
        <a class="kid-tile kid-ask${R(3 + mine.length)}" href="#/ask"><span class="kid-ask-mark" aria-hidden="true">${I('plus')}</span><span class="kid-tile-name">Ask for an app</span><span class="kid-tile-for">Ask ${esc(admin.short)} for something new</span></a></div></section>`;
    } else {
      const groups = GROUPS.map((g) => ({ ...g, items: mine.filter((x) => g.cats.includes(x.d.category)) })).filter((g) => g.items.length);
      body = `<div class="grid h-panels">${layout(groups).map(({ g, span, cols }, gi) => `
        <section class="panel glass glass-react span-${span}${R(4 + gi)}" aria-labelledby="g-${g.id}"><h2 class="panel-title" id="g-${g.id}">${esc(g.title)} <span class="count">${g.items.length}</span></h2>
          <div class="panel-rows ${cols === 1 ? 'list' : ''}" style="--cols:${cols}">${g.items.map(({ a, d, st }) => `
            <button type="button" class="app-row ${st ? 'is-' + st.kind : ''}" data-h="open" data-id="${a.id}">
              ${appIcon(d)}<span class="app-row-text"><span class="app-row-name">${esc(d.name)}</span><span class="app-row-for">${esc(d.homeFor)}</span></span>
              <span class="app-row-end">${st && st.kind === 'away' ? `${I('lock', 'icon icon-sm')}<span class="app-row-state">${esc(st.words)}</span>` : `${st ? `<span class="app-row-state">${esc(st.words)}</span>` : '<span class="sr-only">Working</span>'}<span class="dot is-checking" data-dot="${st ? st.dot : 'green'}" aria-hidden="true"></span>`}${I('chevronRight', 'icon row-chev')}</span>
            </button>`).join('')}</div>${duo.filter((xs) => g.cats.includes(xs[0].d.category)).map((xs) => { const h = xs.filter((x) => hid.includes(x.a.id)); return `<div class="h-pair">${h.length ? `<span>${esc(and(h.map((x) => x.d.name)))} ${h.length === 1 ? 'is' : 'are'} hidden on your Home.</span><button type="button" class="btn btn-sm btn-plain" data-h="unhide" data-id="${h.map((x) => x.a.id).join(',')}">Show ${h.length === 1 ? 'it' : 'them'}</button>` : `<span>${esc(and(xs.map((x) => x.d.name)))} do the same thing. Use only one?</span><button type="button" class="btn btn-sm btn-plain" data-h="hide-pick" data-cat="${esc(xs[0].d.purpose)}">Hide one</button>`}</div>`; }).join('')}</section>`).join('')}</div>`;
    }

    // a quiet way to ask for what is missing, at the end of their apps
    const askTile = kid || !mine.length ? '' : `<a class="h-ask${R(8)}" href="#/ask"><span class="h-ask-mark" aria-hidden="true">${I('plus')}</span><span class="h-ask-text"><b>Ask for an app</b><span>Something missing? Ask ${esc(admin.short)}, or say what you need.</span></span>${I('chevronRight', 'icon row-chev')}</a>`;
    // answers to what this person asked for; for an admin, what is waiting
    const answers = ST.state().requests.filter((r) => r.answer?.fresh && (r.state === 'installed' || r.state === 'notnow') && r.asks.some((a) => a.by === p.id));
    const answerCards = answers.map((r) => { const d = r.app ? cat(r.app) : null, by = ST.person(r.answer.by).short; return `<section class="h-answer glass${R(3)}" aria-label="An answer to your request">${d ? appIcon(d) : `<span class="h-ask-mark" aria-hidden="true">${I('ask')}</span>`}<span class="h-answer-text">${r.state === 'installed'
      ? `<b>${esc(d.name)} is on your Home now</b><span>${esc(by)} installed what you asked for${kid && d.kidView ? `, with its kid view` : ''}.</span>`
      : `<b>Not now: ${esc(d ? d.name : r.text)}</b><span>${esc(by)}${r.answer.reason ? `: “${esc(r.answer.reason)}”` : ' said not now.'} You can ask again later.</span>`}</span><button type="button" class="btn btn-sm" data-h="seen" data-id="${r.id}">OK</button></section>`; }).join('');
    const waitingRq = p.role === 'admin' ? ST.open() : [];
    const newReq = waitingRq.length ? `<section class="h-look h-look-ask glass glass-react${R(3)}" aria-label="New requests in Admin"><span class="h-look-mark" aria-hidden="true">${I('ask')}</span><span class="h-look-text"><a class="h-look-link" href="admin.html#/store/requests">${waitingRq.length === 1 ? 'New request' : `${waitingRq.length} new requests`}</a><span>${esc(and(waitingRq.map((r) => `${ST.who(r)} asked for ${r.app ? cat(r.app).name : `“${r.text}”`}`)))}.</span></span>${I('chevronRight', 'icon row-chev')}</section>` : '';
    const looks = p.role === 'admin' ? D.installed.filter((a) => a.restore[11] === 'fail').map((a) => ({ a, d: cat(a.id), words: `${cat(a.id).name}’s restore test failed on Sunday.` })) : [];
    if (p.role === 'admin' && Proto.get('disk', '') === 'failed') looks.unshift({ a: { id: 'disk' }, href: 'admin.html#/disks/an-apps', title: 'A disk failed on Attic NAS', words: 'App data runs on its other disk; replace Bay 1 soon.' });
    const look = looks.length ? `<section class="h-look glass glass-react${R(3)}" aria-label="Needs a look in Admin"><span class="h-look-mark" aria-hidden="true">${I('alert')}</span><span class="h-look-text"><a class="h-look-link" href="${looks[0].href || `admin.html#/apps/${looks[0].a.id}`}">${esc(looks[0].title || 'Something needs a look in Admin')}</a><span>${esc(looks[0].words)}${looks[0].href ? '' : ' The backups themselves are fine.'}</span></span>${I('chevronRight', 'icon row-chev')}</section>` : '';
    const adminSec = p.role === 'admin' ? `<section class="h-sec${R(9)}" aria-labelledby="h-admin"><div class="group-head"><h2 id="h-admin">For you as admin</h2></div>
      <div class="group glass lead-sm"><a class="row row-link" href="admin.html"><span class="row-lead">${I('settings', 'icon icon-md')}</span><span class="row-main"><span class="row-title">Admin</span><span class="row-sub">${isAway ? 'Opens only on the mesh' : 'Apps, people, nodes and backups · on the mesh only'}</span></span>${I('chevronRight', 'icon row-chev')}</a></div></section>` : '';
    const devSec = kid ? '' : `<section class="h-sec${R(10)}" aria-labelledby="h-dev"><div class="group-head"><h2 id="h-dev"><span class="only-phone">On this phone</span><span class="only-wide">On this computer</span></h2></div>
      <div class="group glass lead-sm">
        <div class="row only-phone"><span class="row-lead">${I('phone', 'icon icon-md')}</span><span class="row-main"><span class="row-title">Put Home on your home screen</span><span class="row-sub wrap">In Safari: Share, then Add to Home Screen. On Android: the menu, then Install app.</span></span></div>
        <div class="row only-wide"><span class="row-lead">${I('desktop', 'icon icon-md')}</span><span class="row-main"><span class="row-title">Get the family app</span><span class="row-sub wrap">For macOS, Windows and Linux: Home one click away, and alerts reach you. It signs in through your browser.</span></span><button type="button" class="btn btn-sm" data-h="desktop-app">${I('download')}Download</button></div>
      </div></section>`;

    const main = document.querySelector('#home');
    main.className = `page ${kid ? 'h-kid' : ''}`;
    main.innerHTML = `
      <section class="h-hello" aria-labelledby="h-title"><h1 id="h-title"${first ? ' class="rise"' : ''}>${kid ? `Hi, ${esc(p.short)}.` : `${greet()}, ${esc(p.short)}.`} <span class="fact" id="fact">${esc(lastFact && !first ? lastFact : fact)}</span></h1>${sub ? `<p class="h-sub${R(2)}">${esc(sub)}</p>` : ''}</section>
      ${away1}${answerCards}${look}${newReq}${body}${askTile}${adminSec}${devSec}
      <footer class="h-foot"><span>${esc(D.household.name)} · Home opens your apps; it can’t change anything.</span><span>App icons: selfh.st/icons, CC BY 4.0</span></footer>`;
    document.title = 'Home · HomersLab';

    // the dots breathe while each app is checked, then pop into their state; the fact cross-fades if it changed
    setTimeout(() => main.querySelectorAll('.dot[data-dot]').forEach((dot, i) => setTimeout(() => { dot.classList.remove('is-checking'); dot.classList.add('dot-' + dot.dataset.dot, 'is-new'); }, i * 60)), first ? 500 : 250);
    const fEl = main.querySelector('#fact');
    if (lastFact !== null && lastFact !== fact && !UI.still()) { fEl.classList.add('swap'); setTimeout(() => { fEl.textContent = fact; fEl.classList.remove('swap'); }, 300); } else fEl.textContent = fact;
    lastFact = fact;

    // the first screen rises on arrival (classic's entrance); what is below it is simply there when you scroll
    main.querySelectorAll('.rise').forEach((el) => { if (el.getBoundingClientRect().top < innerHeight) el.classList.add('in'); else el.classList.remove('rise'); });
    first = false;
    bars(document.querySelector('#top'), main.querySelector('#h-title'));
    renderMenu();
    Proto.refresh();
  }

  // ---------- the glass level: each person's own, kept in this browser (the prototype bar can override it for review) ----------
  const glassKey = () => `hl-home-glass:${me().id}`;
  window.HL_GLASS_DEFAULT = () => { try { return localStorage.getItem(glassKey()) || 'balanced'; } catch (e) { return 'balanced'; } };
  document.addEventListener('hl:glass', (e) => { try { localStorage.setItem(glassKey(), String(e.detail)); } catch {} if (Proto.get('glass', '')) Proto.set('glass', null, ''); });
  if (!Proto.get('glass', '')) HL.glass(window.HL_GLASS_DEFAULT());

  // ---------- the person's menu: grows from their name ----------
  function renderMenu() {
    const p = me(), t = Proto.get('theme', 'auto');
    document.querySelector('#me-menu').innerHTML = `
      <div class="menu-who">${avatar(p, 'avatar-lg')}<span><b>${esc(p.name)}</b><small>${p.role === 'kid' ? 'Kid' : p.role === 'admin' ? 'Admin' : 'Adult'} in ${esc(D.household.name.replace(/^The /, 'the '))}</small></span></div>
      <p class="menu-label" id="appearance">Appearance</p>
      <div class="segmented segmented-block" style="--n:3;margin:0 8px 8px" role="radiogroup" aria-labelledby="appearance" data-seg="appearance"><span class="segmented-thumb" aria-hidden="true"></span>
        ${['auto', 'light', 'dark'].map((v) => `<button type="button" role="radio" aria-checked="${t === v}" data-theme-set="${v}">${v === 'auto' ? 'Auto' : v === 'light' ? 'Light' : 'Dark'}</button>`).join('')}</div>
      <p class="menu-label" id="glass-l">Glass</p>
      <div class="h-glass">${HL.glassControl('home-glass')}</div>
      <div class="menu-sep"></div>
      ${hidden(p).length ? `<button type="button" class="menu-item" data-h="unhide" data-id="${hidden(p).join(',')}">${I('eye')}<span>Show hidden apps<small>${esc(and(hidden(p).map((id) => cat(id).name)))}, hidden on your Home</small></span></button>` : ''}
      <a class="menu-item" href="#/ask">${I('ask')}<span>Ask for an app<small>${(() => { const n = ST.state().requests.filter((r) => r.state === 'waiting' && r.asks.some((a) => a.by === p.id)).length; return n ? `${n} waiting for ${esc(admin.short)}` : `${esc(admin.short)} decides`; })()}</small></span></a>
      <button type="button" class="menu-item" data-h="me">${I('key')}<span>Passkeys and devices<small>${p.passkeys.length} ${p.passkeys.length === 1 ? 'passkey' : 'passkeys'}, ${D.devices.filter((d) => d.person === p.id).length} on the mesh</small></span></button>
      <button type="button" class="menu-item" data-h="out">${I('signout')}<span>Sign out<small>On this device</small></span></button>`;
    paint(document.querySelector('#me-menu'));
  }
  const menu = () => document.querySelector('#me-menu'), meBtn = () => document.querySelector('#me-btn');
  function setMenu(on) {
    const m = menu(), b = meBtn(); if (!b) return;
    if (on) { const r = b.getBoundingClientRect(); m.style.top = `${r.bottom + 8}px`; m.style.right = `${Math.max(16, document.documentElement.clientWidth - r.right)}px`; m.style.left = 'auto'; paint(m); }
    m.classList.toggle('is-open', on); b.setAttribute('aria-expanded', String(on));
    if (on) setTimeout(() => m.querySelector('[aria-checked="true"]')?.focus({ preventScroll: true }), 40);
  }

  // ---------- sheets ----------
  async function meSheet() {
    const p = me(), devs = D.devices.filter((d) => d.person === p.id);
    const v = await UI.dialog({ title: esc(p.name), sub: `${p.role === 'kid' ? 'Kid' : p.role === 'admin' ? 'Admin' : 'Adult'} in ${esc(D.household.name.replace(/^The /, 'the '))}`,
      body: `<div><p class="group-head">Your devices</p><ul class="group lead-sm">${devs.map((d) => `<li class="row"><span class="row-lead">${I(d.kind === 'tv' ? 'tv' : d.kind, 'icon icon-md')}</span><span class="row-main"><span class="row-title">${esc(d.name)}</span><span class="row-sub">${esc(d.model)} · ${esc(MESHNAME[d.mesh])}</span></span></li>`).join('') || '<li class="row"><span class="row-main"><span class="row-label">None of your own</span></span></li>'}</ul></div>
        <div><p class="group-head">Your passkeys</p><ul class="group lead-sm">${p.passkeys.map((k) => `<li class="row"><span class="row-lead">${I('key', 'icon icon-md')}</span><span class="row-main"><span class="row-title">${esc(k.name)}</span><span class="row-sub">Added ${esc(k.added)}</span></span></li>`).join('')}</ul></div>`,
      actions: [{ label: 'Close', value: 'close', kind: 'ghost' }, { label: 'Add a passkey', value: 'passkey', kind: 'primary', icon: 'key' }] });
    if (v === 'passkey') { const ok = await osPrompt({ create: true, site: D.household.homeUrl, account: p.mail || p.short }); if (ok) toast('Passkey added. You can sign in on the new device now.', 'key'); }
  }
  async function connectSheet() {
    const p = me(), dev = device(p), m = dev ? MESHNAME[dev.mesh] : 'NetBird';
    await UI.dialog({ title: 'Open your apps from anywhere', sub: `Your ${dev ? esc(dev.kind) : 'phone'} is already set up; the ${esc(m)} app just needs to be on.`,
      body: `<ol class="group stepup-list"><li>${I('mesh')}<span>Open the <b>${esc(m)}</b> app</span></li><li>${I('check')}<span>Turn it on<small>It signs in with your household account</small></span></li><li>${I('home')}<span>Come back here: your apps open again</span></li></ol>`,
      actions: [{ label: 'OK', value: 'ok', kind: 'primary' }] });
  }

  // ---------- asking for an app: the Store's look, read-only, with Ask for it in place of Get ----------
  const has = (id) => APPS.some((a) => a.id === id);
  const askable = (d, p = me()) => !has(d.id) && (p.role !== 'kid' || ST.kidOk(d));
  const mineRq = (p = me()) => ST.state().requests.filter((r) => r.state !== 'withdrawn' && r.asks.some((a) => a.by === p.id));
  const waitingFor = (id) => ST.state().requests.find((r) => r.app === id && r.state === 'waiting');
  function askBtn(d) {
    const p = me(), r = ST.state().requests.find((x) => x.app === d.id && x.state !== 'withdrawn' && x.state !== 'notnow' && x.state !== 'installed');
    if (r && r.asks.some((a) => a.by === p.id)) return { label: r.state === 'approved' ? 'Approved' : 'Asked', disabled: true, label2: `${d.name}: ${r.state === 'approved' ? 'approved' : 'asked, waiting'}` };
    if (r) return { label: 'Ask too', attrs: `data-h="ask" data-id="${d.id}"`, label2: `Ask for ${d.name} too` };
    return { label: 'Ask for it', attrs: `data-h="ask" data-id="${d.id}"`, label2: `Ask for ${d.name}` };
  }
  const AO = { btn: askBtn, href: (d) => `#/ask/app/${d.id}`, hide: (d) => !askable(d) };
  function askHead(seg) {
    const n = mineRq().filter((r) => r.state === 'waiting').length;
    return `<div class="store-head"><div class="store-head-row"><div><h1 tabindex="-1">Ask for an app</h1><p class="h-sub">${esc(admin.short)} decides what the household installs. Ask for anything here, or say what you need.</p></div></div>
      <div class="store-tools"><div class="segmented" style="--n:2" role="tablist" aria-label="Ask for an app" data-seg="ask-seg"><span class="segmented-thumb" aria-hidden="true"></span>
        <button type="button" role="tab" aria-selected="${seg === 'discover'}" data-h="go" data-to="#/ask">Discover</button><button type="button" role="tab" aria-selected="${seg === 'requests'}" data-h="go" data-to="#/ask/requests">Your requests${n ? ` <span class="count">${n}</span>` : ''}</button></div>
        ${seg === 'discover' ? ST.search('ask-q', '', 'Search') : ''}</div></div>`;
  }
  function renderAsk() {
    const p = me(), kid = p.role === 'kid', h = location.hash, [path, qs] = h.split('?'), q = new URLSearchParams(qs || '');
    const back = path.startsWith('#/ask/app/') || path.startsWith('#/ask/browse') ? { href: '#/ask', label: 'Ask for an app' } : { href: '#', label: 'Home' };
    document.querySelector('#bar').innerHTML = `<a class="topbar-back" href="${back.href}">${I('chevronBack')}<span>${esc(back.label)}</span></a><span class="topbar-title" aria-hidden="true"></span>
      <div class="topbar-end"><button type="button" class="h-me" id="me-btn" aria-haspopup="dialog" aria-expanded="false" aria-controls="me-menu">${avatar(p)}<span class="h-me-name">${esc(p.name)}</span></button></div>`;
    const cands = D.catalog.filter((d) => askable(d));
    let body;
    if (path === '#/ask/requests') {
      const list = mineRq();
      body = askHead('requests') + (list.length ? `<div class="store-requests">${list.map((r) => { const d = r.app ? cat(r.app) : null, me1 = r.asks.find((a) => a.by === p.id), others = r.asks.filter((a) => a.by !== p.id).map((a) => ST.person(a.by).short);
        return `<article class="card store-request" data-row="${r.id}"><div class="store-request-head">${d ? appIcon(d) : `<span class="store-request-mark" aria-hidden="true">${I('ask')}</span>`}<div class="store-request-title"><b>${esc(d ? d.name : `“${r.text}”`)}</b><span>Asked ${esc(me1.when)}${others.length ? ` · ${esc(and(others))} asked too` : ''}</span></div>${ST.statePill(r)}</div>
          ${me1.note ? `<p class="h-ask-note"><q>${esc(me1.note)}</q></p>` : ''}
          <p class="store-request-have">${r.state === 'waiting' ? `Waiting for ${esc(admin.short)}. You hear the answer here${esc(ST.reach(p).replace('on Home', ''))}.` : r.state === 'approved' ? `${esc(admin.short)} approved it and is installing it; it appears on your Home when it is ready.` : r.state === 'installed' ? `${esc(d.name)} is on your Home.` : `${esc(ST.person(r.answer.by).short)}, ${esc(r.answer.when)}${r.answer.reason ? `: “${esc(r.answer.reason)}”` : ': not now.'}`}</p>
          ${r.state === 'waiting' ? `<div class="store-request-actions"><button type="button" class="btn" data-h="withdraw" data-id="${r.id}">Withdraw</button></div>` : r.state === 'installed' ? `<div class="store-request-actions"><a class="btn" href="#">${I('home')}See it on Home</a></div>` : ''}</article>`; }).join('')}</div>`
        : `<div class="group glass"><div class="empty">${I('ask')}<p class="empty-title">Nothing asked yet</p><p class="empty-text">Apps you ask for wait here until ${esc(admin.short)} answers.</p></div></div>`);
    } else if (path.startsWith('#/ask/app/')) {
      const d = cat(path.split('/')[3]);
      if (!d || !askable(d)) body = `<div class="group glass"><div class="empty"><p class="empty-title">${d && has(d.id) ? `${esc(d.name)} is already here` : 'Not one to ask for'}</p><p class="empty-text"><a class="link" href="#/ask">Back to Ask for an app</a></p></div></div>`;
      else {
        const r = waitingFor(d.id), mates = D.catalog.filter((x) => x.category === d.category && has(x.id) && inAud(APPS.find((a) => a.id === x.id).audience, p));
        const notes = [];
        if (mates.length) notes.push(`<div class="notice notice-info glass a-finding" role="note">${I('apps')}<span class="notice-body"><b>You already have ${esc(and(mates.map((x) => x.name)))} for ${esc(ST.purpose(d.category).name.toLowerCase())}; ${esc(d.name)} could sit beside it.</b></span></div>`);
        if (r) notes.push(`<div class="notice notice-info glass a-finding" role="note">${I('ask')}<span class="notice-body"><b>${esc(ST.who(r))} asked for ${esc(d.name)}.</b>${r.asks.some((a) => a.by === p.id) ? `Waiting for ${esc(admin.short)}.` : 'Asking too tells ' + esc(admin.short) + ' more people want it.'}</span></div>`);
        body = ST.page(d, { ...AO, notice: notes.join(''), actNote: `${esc(admin.short)} decides; you hear the answer here${esc(ST.reach(p).replace('on Home', ''))}.` });
      }
    } else if (path === '#/ask/browse') {
      const k = ST.purpose(q.get('cat')), list = cands.filter((d) => d.category === k?.id);
      body = `<div class="store-head"><h1 tabindex="-1">${esc(k ? k.name : 'Apps')}</h1></div><div class="card store-grid">${list.map((d) => ST.item(d, AO)).join('')}</div>`;
    } else {
      const hero = D.store.hero, heroOk = askable(cat(hero.app));
      const shelves = D.store.collections.map((c) => ({ c, apps: c.apps.map(cat).filter((d) => askable(d)) })).filter((x) => x.apps.length >= 2);
      const catsWith = D.categories.filter((k) => cands.some((d) => d.category === k.id));
      body = `${askHead('discover')}<div id="ask-body">
        ${heroOk ? ST.hero(hero, AO) : ''}
        ${shelves.map(({ c, apps }) => ST.shelf({ ...c, sub: kid && c.id === 'family' ? 'With a kid view' : c.sub }, apps, AO)).join('')}
        ${catsWith.length ? ST.cats(catsWith, (k) => `#/ask/browse?cat=${k.id}`) : ''}
        <section class="store-shelf" aria-labelledby="h-cant"><div class="card store-cant"><span class="store-request-mark" aria-hidden="true">${I('ask')}</span><div><h2 id="h-cant">Can’t find it?</h2><p>Say what you’d like an app for, in your own words. ${esc(admin.short)} may know one that does it.</p></div><button type="button" class="btn btn-primary" data-h="ask-free">Ask in your own words</button></div></section></div>
        <div id="ask-results" hidden></div>`;
    }
    const main = document.querySelector('#home');
    main.className = `page h-store ${kid ? 'h-kid' : ''}`;
    main.innerHTML = `${body}<footer class="h-foot"><span>${esc(D.household.name)} · Asking installs nothing: ${esc(admin.short)} decides.</span><span>App icons: selfh.st/icons, CC BY 4.0</span></footer>`;
    document.title = 'Ask for an app · Home';
    const inp = main.querySelector('#ask-q');
    inp?.addEventListener('input', () => { const s = inp.value.trim().toLowerCase(), box = main.querySelector('#ask-body'), res = main.querySelector('#ask-results'); box.hidden = !!s; res.hidden = !s;
      const list = cands.filter((d) => (d.name + ' ' + d.purpose + ' ' + d.blurb).toLowerCase().includes(s));
      res.innerHTML = list.length ? `<div class="card store-grid">${list.map((d) => ST.item(d, AO)).join('')}</div>` : `<div class="group glass"><div class="empty"><p class="empty-title">Nothing called “${esc(inp.value)}”</p><p class="empty-text">Ask in your own words instead: <button type="button" class="link" data-h="ask-free">say what you need</button>.</p></div></div>`; paint(res); });
    paint(main); bars(document.querySelector('#top'), main.querySelector('h1'));
    renderMenu(); Proto.refresh();
  }
  async function hidePick(catId) {
    const p = me(), xs = APPS.filter((a) => inAud(a.audience, p) && cat(a.id).purpose === catId).map((a) => cat(a.id));
    const v = await UI.dialog({ title: 'Hide one on your Home', sub: `Only on your Home: the household keeps both, and everyone else still sees both. Show it again here, or from your menu.`,
      body: `<div class="group" role="radiogroup" aria-label="Which to hide">${xs.map((d, i) => `<label class="row row-choice"><input type="radio" name="hide-one" value="${d.id}" ${i === xs.length - 1 ? 'checked' : ''}><span class="row-lead">${appIcon(d, 'app-icon-sm')}</span><span class="row-main"><span class="row-title">Hide ${esc(d.name)}</span><span class="row-sub">${esc(d.homeFor)}</span></span><span class="row-trail">${I('check2', 'icon checkmark')}</span></label>`).join('')}</div>`,
      actions: [{ label: 'Cancel', value: 'no', kind: 'ghost' }, { label: 'Hide it', value: 'yes', kind: 'primary', icon: 'eyeOff' }] });
    if (v !== 'yes') return;
    const id = document.querySelector('input[name="hide-one"]:checked')?.value; if (!id) return;
    setHidden([...new Set([...hidden(), id])]); toast(`${cat(id).name} is hidden on your Home.`, 'eyeOff'); HL.list(render, document.querySelector('#home'));
  }
  async function askFor(id) {
    const p = me(), d = cat(id), r = waitingFor(id);
    const v = await UI.dialog({ title: `Ask for ${esc(d.name)}`, sub: `${esc(admin.short)} decides. You hear the answer here${esc(ST.reach(p).replace('on Home', ''))}.${r ? ` ${esc(ST.who(r))} asked for it too.` : ''}`,
      body: `<div class="field"><label class="field-label" for="ask-note">Why would you like it? <span class="muted">(optional)</span></label><textarea id="ask-note" class="input" rows="3" placeholder="${p.role === 'kid' ? 'So I can …' : 'For …'}"></textarea></div>`,
      actions: [{ label: 'Cancel', value: 'no', kind: 'ghost' }, { label: r ? 'Ask too' : 'Ask for it', value: 'yes', kind: 'primary' }], mark: appIcon(d, 'app-icon-lg') });
    if (v !== 'yes') return;
    const ask = { by: p.id, when: `Today ${UI.hhmm(now())}`, note: (document.querySelector('#ask-note')?.value || '').trim() };
    const st = ST.state(), old = st.requests.find((x) => x.app === id && x.state === 'notnow' && x.asks.some((a) => a.by === p.id));
    if (r) r.asks.push(ask); else st.requests.unshift({ id: `rq-${Date.now().toString(36)}`, app: id, state: 'waiting', asks: [ask] });
    if (old) old.state = 'withdrawn';
    ST.save(); toast(`Asked. ${admin.short} sees it in Admin${admin.ntfy ? ' and on her phone' : ''}.`, 'ask'); render();
  }
  async function askFree() {
    const p = me();
    const v = await UI.dialog({ title: 'Ask in your own words', sub: `Say what you’d like an app for. ${esc(admin.short)} may answer with an app from the Store.`,
      body: `<div class="field"><label class="field-label" for="ask-what">What would you like an app for?</label><input id="ask-what" class="input" placeholder="Something to plan meals" autocomplete="off"></div>
        <div class="field"><label class="field-label" for="ask-note">Anything else? <span class="muted">(optional)</span></label><textarea id="ask-note" class="input" rows="3"></textarea></div>`,
      actions: [{ label: 'Cancel', value: 'no', kind: 'ghost' }, { label: 'Ask', value: 'yes', kind: 'primary' }] });
    const text = (document.querySelector('#ask-what')?.value || '').trim();
    if (v !== 'yes') return; if (!text) return toast('Say what you’d like an app for, then ask.', 'info');
    ST.state().requests.unshift({ id: `rq-${Date.now().toString(36)}`, text, state: 'waiting', asks: [{ by: p.id, when: `Today ${UI.hhmm(now())}`, note: (document.querySelector('#ask-note')?.value || '').trim() }] });
    ST.save(); toast(`Asked. ${admin.short} sees it in Admin.`, 'ask'); location.hash = '#/ask/requests';
  }
  function withdraw(id) {
    const p = me(), r = ST.state().requests.find((x) => x.id === id);
    r.asks = r.asks.filter((a) => a.by !== p.id); if (!r.asks.length) r.state = 'withdrawn';
    ST.save(); toast('Withdrawn.', 'check'); HL.list(render, document.querySelector('#home'));
  }

  document.addEventListener('click', async (e) => {
    const m = menu();
    const g = e.target.closest('[data-h="go"]'); if (g) { location.hash = g.dataset.to; return; }
    if (e.target.closest('#me-btn')) { e.stopPropagation(); setMenu(!m.classList.contains('is-open')); return; }
    const th = e.target.closest('[data-theme-set]');
    if (th) { const t = th.dataset.themeSet; const apply = () => Proto.set('theme', t, 'auto'); if (!UI.still() && document.startViewTransition) document.startViewTransition(apply); else apply(); return; }
    if (m.classList.contains('is-open') && !m.contains(e.target)) setMenu(false);
    const t = e.target.closest('[data-h]'); if (!t) return;
    if (t.dataset.h === 'me') { setMenu(false); return meSheet(); }
    if (t.dataset.h === 'ask') return askFor(t.dataset.id);
    if (t.dataset.h === 'ask-free') return askFree();
    if (t.dataset.h === 'withdraw') return withdraw(t.dataset.id);
    if (t.dataset.h === 'hide-pick') return hidePick(t.dataset.cat);
    if (t.dataset.h === 'unhide') { setHidden(hidden().filter((id) => !t.dataset.id.split(',').includes(id))); setMenu(false); toast('It’s back on your Home.', 'eye'); return HL.list(render, document.querySelector('#home')); }
    if (t.dataset.h === 'seen') { const r = ST.state().requests.find((x) => x.id === t.dataset.id); r.answer.fresh = false; ST.save(); return HL.list(render, document.querySelector('#home')); }
    if (t.dataset.h === 'out') { setMenu(false); return toast('Prototype: you would be signed out of every app on this device.', 'info'); }
    if (t.dataset.h === 'connect') return connectSheet();
    if (t.dataset.h === 'desktop-app') return toast('Prototype: the family app’s download page would open here.', 'info');
    if (t.dataset.h === 'open') {
      const a = APPS.find((x) => x.id === t.dataset.id), d = cat(a.id), st = stateOf(a);
      if (st && st.kind === 'away') return connectSheet();
      if (st && st.kind === 'updating') return toast(`${esc(d.name)} is updating. It will be back in a few minutes.`, 'refresh');
      if (st && st.kind === 'down') return toast(`${esc(d.name)} isn’t working right now. ${esc(admin.short)} has been told.`, 'alert');
      toast(`In the real product this opens ${esc(host(a.id))}${me().role === 'kid' && d.kidView ? ', in its kid view' : ''}.`, 'external');
    }
  });
  document.addEventListener('keydown', (e) => { if (e.key === 'Escape' && menu().classList.contains('is-open')) { setMenu(false); meBtn()?.focus(); } });
  addEventListener('scroll', () => { if (menu().classList.contains('is-open')) setMenu(false); }, { passive: true });

  Proto.onChange((k) => { if (k === 'theme') { renderMenu(); return; } render(); });
  Proto.state(() => {
    const p = me(), mine = APPS.filter((a) => inAud(a.audience, p));
    return { person: `${p.name} (${p.role})`, layout: p.role === 'kid' ? 'a kid’s own: bigger tiles' : 'panels of rows', where: Proto.get('where', 'mesh'), apps: mine.map((a) => cat(a.id).name).join(', '), 'Home can': 'read apps, audiences and health; nothing else' };
  });
  // the member's Store: in and out of it push and pop; within it, segments cross-fade
  let lastHash = location.hash;
  const depth = (h) => (!h.startsWith('#/ask') ? 0 : h.startsWith('#/ask/app/') || h.startsWith('#/ask/browse') ? 2 : 1);
  addEventListener('hashchange', () => { const a = depth(lastHash), b = depth(location.hash); lastHash = location.hash; setMenu(false); HL.run(() => { render(); scrollTo(0, 0); document.querySelector('#home h1')?.focus({ preventScroll: true }); }, { type: b > a ? 'push' : b < a ? 'pop' : 'fade' }); });
  window.addEventListener('DOMContentLoaded', () => { Proto.start(); render(); });
})();
