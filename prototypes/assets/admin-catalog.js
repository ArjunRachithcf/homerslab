// HomersLab Admin prototype: the Store (Discover, Updates, Requests), an app's page, the install plan and the install.
// PROTOTYPE, throwaway. "Store" is where apps are browsed and got; the catalog is the signed collection of app
// definitions behind it. Get opens the install plan, a page of its own whose address carries its id (#/plans/p-…); one
// passkey at Install approves the whole plan, extras included, and any change makes a new plan with a new address
// (decisions of round 1). Members ask for apps on Home; their requests wait here, and approving one only opens its plan.
(() => {
  const { D, S, cat, inst, rem, node, host, meshWords, and, fit, suggest, draft, buildPlan, approvalFor,
    appIcon, avatar, tier, TIER, SIGNIN, chev, meter, route, render, go, soon, act, log, clone } = A;
  const { esc, toast, dialog, stepUp, hhmm, now, gb } = UI;
  const cap = (s) => (s ? s[0].toUpperCase() + s.slice(1) : s);
  const ST = STORE;

  const TIER_WORDS = {
    all: 'Every app here meets the floor, whichever catalog it comes from.',
    verified: 'Verified: native sign-in, restores and updates tested in CI, hardened.',
    community: 'Community: meets the floor. Converted from a template and reviewed; some keep a sign-in of their own.',
    third: 'Third-party: from a catalog you trust by its publisher’s key. HomersLab checks the floor; nobody else vouches for it.',
  };
  const FLOOR = ['Publishes no ports', 'Is never privileged', 'Declares the machine’s network, devices and capabilities as extras', 'Pins every image by digest', 'Declares every folder that holds data, so it is backed up', 'Generates every secret, with no default values', 'Declares a sign-in method and a health check', 'Passes its install test in CI'];

  // ---------- what a button says for an app: Get, Open or Update ----------
  const btn = (d) => {
    const a = inst(d.id);
    if (a && a.update) return { label: 'Update', attrs: `data-act="update" data-id="${d.id}"`, label2: `Update ${d.name} to ${a.update.to}` };
    if (a) return { label: 'Open', href: `#/apps/${d.id}`, label2: `Open ${d.name}’s page in Apps` };
    return { label: 'Get', href: `#/store/app/${d.id}/get`, label2: `Get ${d.name}: see its install plan` };
  };
  const O = { btn, href: (d) => `#/store/app/${d.id}`, sub: (d) => (inst(d.id) ? `${d.purpose} · installed` : rem(d.id) ? `${d.purpose} · removed, data kept` : d.purpose) };
  const waiting = () => ST.open();
  // what the household already has for a purpose: the "you already have" line
  const have = (k) => D.catalog.filter((a) => a.category === k && inst(a.id));
  const haveNote = (k) => { const h = have(k), all = D.catalog.filter((a) => a.category === k); if (h.length > 1) return `You have ${and(h.map((a) => a.name))}, side by side`; if (h.length === 1 && all.length > 1) return `You have ${h[0].name}; another can sit beside it`; return h.length === 1 ? `You have ${h[0].name}` : ''; };

  // ---------- the Store's head: its segments, a search that is always there, and the catalogs you trust ----------
  function head(seg, qv = '') {
    const ups = S.installed.filter((a) => a.update).length, n = waiting().length;
    return `<div class="store-head"><div class="store-head-row"><h1>Store</h1>
        <button type="button" class="btn btn-sm" data-act="store-account" aria-haspopup="dialog">${I('shield')}Catalogs you trust</button></div>
      <div class="store-tools"><div class="segmented" style="--n:3" role="tablist" aria-label="Store" data-seg="store-seg"><span class="segmented-thumb" aria-hidden="true"></span>
        ${[['discover', 'Discover', '#/store', ''], ['updates', 'Updates', '#/store/updates', ups ? `<span class="count">${ups}</span>` : ''], ['requests', 'Requests', '#/store/requests', n ? `<span class="seg-badge" aria-label="${n} waiting">${n}</span>` : '']].map(([k, l, h, c]) => `<button type="button" role="tab" aria-selected="${seg === k}" data-act="store-seg" data-to="${h}">${l}${c ? ' ' + c : ''}</button>`).join('')}</div>
        ${ST.search('store-q', qv, 'Search the Store')}</div></div>`;
  }
  act('store-seg', (t) => go(t.dataset.to));
  // typing anywhere in the Store shows what matches, grouped by purpose, in place of the segment
  const wireSearch = (q) => { const inp = document.querySelector('#store-q'); if (!inp) return; const run = () => drawResults(inp.value, q); inp.addEventListener('input', run); if (inp.value) run(); };
  function drawResults(text, q) {
    const box = document.querySelector('#store-body'), res = document.querySelector('#store-results'); if (!box || !res) return;
    const s = text.trim().toLowerCase();
    box.hidden = !!s; res.hidden = !s; if (!s) { res.innerHTML = ''; return; }
    res.innerHTML = list(D.catalog.filter((a) => (a.name + ' ' + a.purpose + ' ' + a.blurb).toLowerCase().includes(s)), `No app matches “${esc(text)}”. A third-party catalog may have it: add one under Catalogs you trust.`);
    UI.paint(res);
  }
  // apps grouped by purpose, each group with its "you have" line
  function list(apps, none) {
    const groups = D.categories.map((k) => ({ k, apps: apps.filter((a) => a.category === k.id) })).filter((g) => g.apps.length);
    return groups.length ? groups.map(({ k, apps: g }) => `<section class="store-shelf cat-purpose" aria-labelledby="pur-${k.id}"><div class="store-shelf-head"><div><h2 id="pur-${k.id}">${esc(k.name)} <span class="count">${g.length}</span></h2>${haveNote(k.id) ? `<p>${esc(haveNote(k.id))}</p>` : ''}</div></div>
      <div class="card store-grid">${g.map((d) => ST.item(d, O)).join('')}</div></section>`).join('')
      : `<div class="group"><div class="empty">${I('search')}<p class="empty-title">Nothing found</p><p class="empty-text">${none}</p></div></div>`;
  }

  // ---------- Discover ----------
  route(/^#\/store$/, (m, q) => {
    const col = D.store.collections.map((c) => ST.shelf({ ...c, all: `#/store/browse?col=${c.id}` }, c.apps.map(cat), O)).join('');
    return `${head('discover', q.get('q'))}
      <div id="store-body">${ST.hero(D.store.hero, O)}${col}${ST.cats(D.categories, (k) => `#/store/browse?cat=${k.id}`)}</div>
      <div id="store-results" hidden></div>`;
  }, { after: (m, q) => wireSearch(q) });
  // a purpose, a collection or a tier, with every app in it
  route(/^#\/store\/browse$/, (m, q) => {
    const c = q.get('cat'), col = D.store.collections.find((x) => x.id === q.get('col')), t = q.get('tier') || 'all';
    const base = c ? D.catalog.filter((a) => a.category === c) : col ? col.apps.map(cat) : D.catalog;
    const apps = base.filter((a) => t === 'all' || a.tier === t);
    const name = c ? ST.purpose(c).name : col ? col.name : 'Every app';
    const count = (k) => base.filter((a) => k === 'all' || a.tier === k).length;
    return `<div class="page-head"><div><h1>${esc(name)}</h1><p class="page-sub">${c ? `${esc(haveNote(c) || 'Nothing for this yet')}. Several apps for one purpose can run side by side, so each person uses the one they prefer.` : col ? esc(col.sub) : `${D.catalog.length} apps from ${D.catalogs.length} catalogs you trust.`}</p></div></div>
      <div class="a-filter"><div class="segmented seg-tiers" style="--n:4" role="radiogroup" aria-label="Tier" data-seg="tier"><span class="segmented-thumb" aria-hidden="true"></span>
        ${['all', 'verified', 'community', 'third'].map((k) => `<button type="button" role="radio" aria-checked="${t === k}" data-act="store-tier" data-tier="${k}">${k === 'all' ? 'All' : TIER[k]} <span class="count">${count(k)}</span></button>`).join('')}</div>
        <p class="tier-words">${esc(TIER_WORDS[t])}</p></div>
      ${c ? `<div class="card store-grid">${apps.map((d) => ST.item(d, O)).join('') || '<p class="muted" style="padding:14px 0">None in this tier.</p>'}</div>` : list(apps, 'None in this tier.')}`;
  }, { back: { href: '#/store', label: 'Store' } });
  act('store-tier', (t) => { const [p, qs] = location.hash.split('?'), q = new URLSearchParams(qs || ''); if (t.dataset.tier === 'all') q.delete('tier'); else q.set('tier', t.dataset.tier); history.replaceState(null, '', location.pathname + location.search + p + '?' + q); render(); });
  // old addresses keep working
  route(/^#\/catalog$/, () => '', { redirect: (m, q) => (q.get('cat') ? `#/store/browse?cat=${q.get('cat')}` : '#/store') });
  route(/^#\/catalog\/([\w-]+)$/, () => '', { redirect: (m) => `#/store/app/${m[1]}` });

  // ---------- Updates: tested versions, ready ----------
  route(/^#\/store\/updates$/, () => {
    const ups = S.installed.filter((a) => a.update), autos = S.installed.filter((a) => a.auto);
    return `${head('updates')}<div id="store-body">
      ${ups.length ? `<div class="group lead-lg">${ups.map((a) => { const d = cat(a.id); return `<div class="row is-top" data-app-id="${a.id}"><span class="row-lead">${appIcon(d, 'app-icon-store')}</span><span class="row-main"><span class="row-title">${esc(d.name)}</span><span class="row-sub">${esc(a.version)} → ${esc(a.update.to)} · tested ${esc(a.update.tested)}</span><span class="row-sub wrap">${esc(a.update.notes)}</span></span>${ST.pill(btn(d))}</div>`; }).join('')}</div>
        <p class="group-foot">Every update takes a snapshot first, and returns the app to it if its health check fails. Updates need no passkey.</p>`
      : `<div class="group"><div class="empty">${I('check')}<p class="empty-title">Everything is current</p><p class="empty-text">Every app is on the catalog’s latest tested version.</p></div></div>`}
      <p class="subhead" style="margin:18px 4px 0">${autos.length} ${autos.length === 1 ? 'app updates' : 'apps update'} by itself at 03:00: ${esc(and(autos.map((a) => cat(a.id).name)))}. Change that on each app’s page.</p></div>
      <div id="store-results" hidden></div>`;
  }, { after: (m, q) => wireSearch(q) });

  // ---------- Requests: what members asked for ----------
  route(/^#\/store\/requests$/, () => {
    const w = waiting(), done = ST.state().requests.filter((r) => r.state !== 'waiting' && r.state !== 'withdrawn');
    const card = (r) => {
      const d = r.app ? cat(r.app) : null, k = d ? d.category : r.category, hv = k ? have(k) : [];
      const sugg = !d ? (r.suggest ? r.suggest.map(cat) : D.catalog.filter((a) => a.category === k)).filter((a) => !inst(a.id)) : [];
      const kids = r.asks.filter((a) => ST.person(a.by).role === 'kid');
      return `<article class="card store-request" data-row="${r.id}" aria-labelledby="rq-${r.id}">
        <div class="store-request-head">${d ? appIcon(d) : `<span class="store-request-mark" aria-hidden="true">${I('ask')}</span>`}<div class="store-request-title"><b id="rq-${r.id}">${esc(ST.title(r))}</b><span>${d ? `${esc(d.purpose)} · ${tier(d.tier)}` : 'Not a catalog app: in their own words'}</span></div></div>
        ${ST.asks(r)}
        <p class="store-request-have">${!k ? 'Not tied to a purpose yet: answer it with any app in the Store.' : hv.length ? `For ${esc(ST.purpose(k).name.toLowerCase())} the household has ${esc(and(hv.map((a) => a.name)))}.` : `Nothing for ${esc(ST.purpose(k).name.toLowerCase())} yet.`}${kids.length && d ? ` ${esc(d.kidView ? `Kids get its kid view: ${d.kidView.toLowerCase()}.` : 'Fine for kids.')}` : ''}${!d && sugg.length ? ` The catalog has ${esc(and(sugg.map((a) => a.name)))}.` : ''}</p>
        <div class="store-request-actions">${d ? `<button type="button" class="btn btn-primary" data-act="rq-approve" data-id="${r.id}">Approve${I('arrowRight')}</button>` : (sugg.length ? sugg.slice(0, 2).map((a, i) => `<button type="button" class="btn ${i ? '' : 'btn-primary'}" data-act="rq-use" data-id="${r.id}" data-app="${a.id}">Use ${esc(a.name)}</button>`).join('')
            : `<label class="sr-only" for="pick-${r.id}">Answer with an app</label><select id="pick-${r.id}" class="input rq-pick">${D.catalog.filter((a) => !inst(a.id)).map((a) => `<option value="${a.id}">${esc(a.name)}: ${esc(a.purpose)}</option>`).join('')}</select><button type="button" class="btn btn-primary" data-act="rq-use-pick" data-id="${r.id}">Use this app</button>`)}
          <button type="button" class="btn" data-act="rq-notnow" data-id="${r.id}">Not now</button>${d ? `<a class="btn btn-plain" href="#/store/app/${d.id}">About ${esc(d.name)}</a>` : ''}</div>
        <p class="subhead">${d ? `Approve opens ${esc(d.name)}’s install plan with ${esc(ST.who(r))} in its audience; nothing installs until you press Install there.` : 'Answering with an app opens its install plan; nothing installs until you press Install there.'}</p></article>`;
    };
    return `${head('requests')}<div id="store-body">
      ${w.length ? `<div class="store-requests">${w.map(card).join('')}</div>` : `<div class="group"><div class="empty">${I('inbox')}<p class="empty-title">No requests waiting</p><p class="empty-text">When someone asks for an app on Home, it waits here for you.</p></div></div>`}
      ${done.length ? `<div class="group-head" style="margin-top:36px"><h2>Answered</h2></div><div class="group lead-md">${done.map((r) => { const d = r.app ? cat(r.app) : null; return `<div class="row is-top"><span class="row-lead">${d ? appIcon(d, 'app-icon-md') : I('ask', 'icon icon-md')}</span><span class="row-main"><span class="row-title wrap">${esc(ST.title(r))}</span><span class="row-sub wrap">${r.state === 'notnow' ? `Not now, ${esc(r.answer.when)}${r.answer.reason ? `: “${esc(r.answer.reason)}”` : ''}` : r.state === 'approved' ? `Approved ${esc(r.answer.when)}; its plan is waiting for Install` : `Installed ${esc(r.answer.when)}`}</span></span>${ST.statePill(r)}</div>`; }).join('')}</div>` : ''}
      <p class="subhead" style="margin:18px 4px 0">The person who asked hears your answer on Home, and by mail or ntfy where they set it up.</p></div>
      <div id="store-results" hidden></div>`;
  }, { after: (m, q) => wireSearch(q) });
  // approving: the requester joins the plan's audience, and the plan opens. It installs nothing by itself.
  function approve(r, appId) {
    const dr = draft(appId), ids = r.asks.map((a) => a.by).filter((id) => !A.inAud(dr.audience, A.person(id)));
    if (ids.length) dr.audience = { ...dr.audience, people: [...new Set([...(dr.audience.people || []), ...ids])] };
    r.app = appId; r.state = 'approved'; r.answer = { by: 'ines', when: `Today ${hhmm(now())}` }; ST.save();
    log(`request ${r.id} approved: ${appId}`);
    go(`#/store/app/${appId}/get`);
  }
  const rq = (id) => ST.state().requests.find((r) => r.id === id);
  act('rq-approve', (t) => approve(rq(t.dataset.id), rq(t.dataset.id).app));
  act('rq-use', (t) => approve(rq(t.dataset.id), t.dataset.app));
  act('rq-use-pick', (t) => approve(rq(t.dataset.id), document.querySelector(`#pick-${t.dataset.id}`).value));
  act('rq-notnow', async (t) => {
    const r = rq(t.dataset.id), ps = r.asks.map((a) => ST.person(a.by));
    const v = await dialog({ title: `Not now: ${esc(ST.what(r))}`, sub: `${esc(and(ps.map((p) => `${p.short} hears it ${ST.reach(p)}`)))}. They can ask again later.`,
      body: `<div class="field"><label class="field-label" for="rq-reason">Why not now <span class="muted">(optional)</span></label><textarea id="rq-reason" class="input" rows="3" placeholder="We’ll look at it together at the weekend."></textarea></div>`,
      actions: [{ label: 'Cancel', value: 'no', kind: 'ghost' }, { label: 'Not now', value: 'yes', kind: 'primary' }] });
    if (v !== 'yes') return;
    r.state = 'notnow'; r.answer = { by: 'ines', when: `Today ${hhmm(now())}`, reason: (document.querySelector('#rq-reason')?.value || '').trim(), fresh: true }; ST.save();
    log(`request ${r.id}: not now`); toast(`${and(ps.map((p) => p.short))} ${ps.length === 1 ? 'hears' : 'hear'} your answer on Home.`); A.renderList();
  });

  // ---------- the catalogs you trust: the Store's account sheet ----------
  act('store-account', async () => {
    const v = await dialog({ title: 'Catalogs you trust', sub: 'The Store shows the apps of these catalogs. Each is a signed collection of app definitions, trusted by its publisher’s key.', wide: true,
      body: `<div class="group lead-sm">${D.catalogs.map((k) => `<div class="row is-top"><span class="row-lead">${I(k.id === 'official' ? 'shield' : 'key', 'icon icon-md')}</span><span class="row-main"><span class="row-title">${esc(k.name)} · ${k.apps} ${k.apps === 1 ? 'app' : 'apps'}</span><span class="row-sub wrap">${esc(k.url)}</span><span class="row-sub wrap">Signed by ${esc(k.publisher)}, key <span class="mono">${esc(k.key)}</span> · trusted: ${esc(k.trusted)}</span></span></div>`).join('')}</div>
        <p class="subhead">Every update to a catalog is checked against its publisher’s key. If the key changes, the catalog stops updating until you trust the new one, with your passkey.</p>
        <div><p class="group-head">The floor every app meets</p><ul class="lines">${FLOOR.map((f) => `<li>${I('check')}<span>${esc(f)}</span></li>`).join('')}</ul><p class="subhead" style="margin-top:10px">HomersLab checks these itself, for every app from every catalog, and refuses an app that fails one.</p></div>`,
      actions: [{ label: 'Close', value: 'close', kind: 'ghost' }, { label: 'Add a catalog', value: 'add', kind: 'primary', icon: 'plus' }] });
    if (v === 'add') A.actions['add-catalog']();
  });
  act('add-catalog', async () => {
    const v = await dialog({ title: 'Add a catalog', sub: 'A third-party catalog is a signed git repository. You trust it by its publisher’s key; its apps still have to meet the floor.',
      body: `<div class="field"><label class="field-label" for="cat-url">Its address</label><input id="cat-url" class="input" placeholder="codeberg.org/someone/homerslab-catalog" autocomplete="off"></div>
        <div class="field"><label class="field-label" for="cat-key">Its publisher’s key</label><input id="cat-key" class="input input-secret" placeholder="ed25519 …" autocomplete="off" aria-describedby="cat-key-hint"><span class="hint" id="cat-key-hint">Get it from the publisher directly, not from the catalog itself.</span></div>`,
      actions: [{ label: 'Cancel', value: 'no', kind: 'ghost' }, { label: 'Trust this publisher', value: 'yes', kind: 'primary', icon: 'key' }] });
    if (v !== 'yes') return;
    const ok = await stepUp({ plan: { id: UI.planId({ k: 'catalog', t: Date.now() }), summary: 'trust a new catalog' }, title: 'Trust a new catalog', action: 'Trust it', sub: 'Trusting a catalog is a risky change, so HomersLab asks for your passkey.', what: [{ icon: 'key', text: 'Apps signed by this publisher’s key can be installed in your household, once they meet the floor' }] });
    if (ok) { S.approvals.push(ok); toast('Prototype: the catalog would be fetched and checked now. Nothing is fetched here.', 'info'); }
  });

  // ---------- an app's page in the Store ----------
  route(/^#\/store\/app\/([\w-]+)$/, (m) => {
    const id = m[1], d = cat(id); if (!d) return `<div class="page-head"><div><h1>Not in the Store</h1><p class="page-sub"><a class="link" href="#/store">Back to the Store</a></p></div></div>`;
    const installed = inst(id), removed = rem(id), k = ST.purpose(d.category);
    const mates = D.catalog.filter((x) => x.category === d.category && x.id !== id && inst(x.id));
    const asked = ST.state().requests.find((r) => r.app === id && r.state === 'waiting');
    const notes = [];
    if (mates.length) notes.push(installed
      ? `<div class="notice notice-info a-finding" role="note">${I('apps')}<span class="notice-body"><b>You also have ${esc(and(mates.map((x) => x.name)))} for ${esc(k.name.toLowerCase())}.</b>Both are installed side by side; each person opens the one they prefer from Home.</span></div>`
      : `<div class="notice notice-info a-finding" role="note">${I('apps')}<span class="notice-body"><b>You already have ${esc(and(mates.map((x) => x.name)))} for ${esc(k.name.toLowerCase())}; you can add ${esc(d.name)} too.</b>Both run side by side, and each person picks the one they like on Home. ${mates.map((x) => `<a class="link" href="#/apps/${x.id}">See ${esc(x.name)}</a>`).join(' · ')}</span></div>`);
    if (asked) notes.push(`<div class="notice notice-info a-finding" role="note">${I('ask')}<span class="notice-body"><b>${esc(ST.who(asked))} asked for ${esc(d.name)}.</b><a class="link" href="#/store/requests">See the request</a></span></div>`);
    const a = installed;
    const actNote = a ? (a.update ? `${esc(a.version)} → ${esc(a.update.to)} is ready` : `Installed ${esc(a.since)} on ${esc(node(a.node).name)}`) : removed ? `Removed ${esc(removed.removedOn)}; its data is kept, so it comes back with it` : 'Opens its install plan; nothing happens until you press Install';
    return ST.page(d, { ...O, actNote, notice: notes.join('') });
  }, { back: { href: '#/store', label: 'Store' } });
  // Get: to the app's install plan, a page of its own
  route(/^#\/store\/app\/([\w-]+)\/get$/, () => '', { redirect: (m) => (cat(m[1]) && !inst(m[1]) ? `#/plans/${buildPlan(m[1]).id}` : `#/store/app/${m[1]}`) });

  // ---------- the install plan: a page of its own; its id is in the address ----------
  route(/^#\/catalog\/([\w-]+)\/plan$/, () => '', { redirect: (m) => `#/store/app/${m[1]}/get` });
  const findPlanApp = (pid) => { if (S.plans[pid]) return S.plans[pid]; for (const d of D.catalog) if (!inst(d.id) && buildPlan(d.id).id === pid) return d.id; return null; };
  route(/^#\/plans\/(p-[0-9a-f]+)$/, (m) => {
    const pid = m[1], appId = findPlanApp(pid);
    if (!appId || inst(appId)) return `<div class="page-head"><div><h1>No such plan</h1><p class="page-sub">Plans live only until something changes or the app is installed. <a class="link" href="#/store">Back to the Store</a>.</p></div></div>`;
    const p = buildPlan(appId), d = p.def;
    const asked = ST.state().requests.find((r) => r.app === appId && (r.state === 'approved' || r.state === 'waiting'));
    if (p.id !== pid) return `<div class="page-head"><div class="a-ident">${appIcon(d, 'app-icon-lg')}<div><h1>This plan was replaced</h1><p class="page-sub">Plan <span class="plan-id">${esc(pid)}</span> changed, so it became plan <span class="plan-id">${esc(p.id)}</span>. A plan never changes in place: an approval only ever covers the plan it was given for.</p></div></div><div class="page-actions"><a class="btn btn-primary" href="#/plans/${p.id}">Open the current plan</a></div></div>`;
    const hist = S.planHistory[appId] || [], prev = hist.length > 1 ? hist[hist.length - 2] : null;
    return `
      <div class="page-head"><div class="a-ident" data-app-id="${d.id}">${appIcon(d, 'app-icon-lg')}<div><h1>Install ${esc(d.name)}</h1><p class="page-sub">What installing ${esc(d.name)} will do, in plain words. Nothing happens until you press Install.</p></div></div></div>
      <p class="plan-meta">Plan <span class="plan-id">${esc(p.id)}</span> · made ${esc(p.madeAt)} · ${esc(d.name)} ${esc(d.version)} from the ${esc(D.catalogs.find((k) => k.id === d.catalog).name.toLowerCase())}</p>
      ${asked ? `<div class="notice notice-info a-finding" role="note">${I('ask')}<span class="notice-body"><b>${esc(ST.who(asked))} asked for ${esc(d.name)}${asked.text ? `, for “${esc(asked.text)}”` : ''}.</b>${asked.state === 'approved' ? `You approved it, so ${asked.asks.length === 1 ? 'they are' : 'they are all'} in its audience below. Approving installs nothing: Install does.` : 'Installing it answers the request.'}</span></div>` : ''}
      ${prev ? `<div class="notice notice-info a-finding" role="status">${I('info')}<span class="notice-body">You changed the plan, so it is a new one with its own address: <span class="plan-id">${esc(p.id)}</span>, which replaces <span class="plan-id">${esc(prev)}</span>.</span></div>` : ''}
      <div class="split">
        <div><div class="plan-sections">${planSections(p)}</div>
          <div class="a-dock">${p.risky ? `<p class="a-dock-line">Your passkey approves: ${esc(and(p.chosen.map((x) => x.text.replace(/^Use (the )?/, 'the ').replace(/ on .*$/, ''))))}, for plan ${esc(p.id)} only</p>` : ''}${installButton(p)}</div></div>
        <aside class="rail plan-rail" aria-label="Install">${planCard(p)}</aside>
      </div>`;
  }, { back: (m) => { const id = findPlanApp(m[1]); return id ? { href: `#/store/app/${id}`, label: cat(id).name } : { href: '#/store', label: 'Store' }; } });

  const q = (title, sub) => `<div class="plan-q"><h2>${title}</h2>${sub ? `<span>${sub}</span>` : ''}</div>`;
  function planSections(p) {
    const d = p.def, n = p.node, nodes = S.nodes.map((x) => ({ x, f: fit(d, x) })), best = suggest(d);
    const tpm = n.tpm ? `sealed to ${esc(n.name)}’s TPM` : `sealed with a key only root can read (${esc(n.name)} has no TPM)`;
    const disk = A.appsDisk(n), used = A.memUsed(n);
    const out = [];
    out.push(`<section>${q('Where it runs', 'Its node')}<div class="group" role="radiogroup" aria-label="Node">${nodes.map(({ x, f }) => `
      <label class="row row-choice"><input type="radio" name="pl-node" value="${x.id}" data-change="plan-node" data-app="${p.appId}" ${x.id === n.id ? 'checked' : ''} ${f.ok ? '' : 'disabled'}>
        <span class="row-main"><span class="row-title">${esc(x.name)}${x.id === best?.id ? ' · suggested' : ''}</span><span class="row-sub wrap">${f.ok ? esc(cap(f.good.join(', '))) : esc(f.why.join('; '))} · ${esc(x.os)}, ${esc(x.arch)}</span></span>
        <span class="row-trail">${I('check2', 'icon checkmark')}</span></label>`).join('')}</div></section>`);
    out.push(`<section>${q('Who will see it', 'Its audience')}<div class="group"><div class="audience-box">${A.audienceEditor(p.audience, d, 'plan:' + p.appId)}</div></div></section>`);
    out.push(`<section>${q('How people sign in')}<div class="group"><div class="row is-top"><span class="row-main"><span class="row-label wrap">${esc(d.signIn.words)}</span><span class="row-sub wrap">${d.signIn.method === 'native' ? `Creates a sign-in client, “${esc(d.name)}”, in ${esc(D.household.idp.name)}.` : `The edge on ${esc(n.name)} asks for the household account before ${esc(d.name)} sees a request.`}${d.signIn.second ? ' People will meet a second sign-in, the app’s own.' : ''}</span></span></div></div></section>`);
    out.push(`<section>${q('Its name')}<div class="group"><div class="row is-top"><span class="row-main"><span><span class="host">${esc(p.host)}</span></span><span class="row-sub wrap">On ${esc(meshWords())}, pointing at ${esc(n.name)}. ${d.publicAllowed ? 'Mesh only: you can make it public later, from its page, with your passkey.' : `Mesh only, always. ${esc(d.publicWhy || '')}`}</span></span></div></div></section>`);
    out.push(`<section>${q('What is backed up', 'Every hour')}<div class="group lead-sm">${d.data.map((x) => `<div class="row"><span class="row-lead">${I(x.what.startsWith('Not') ? 'minus' : 'check', 'icon icon-md')}</span><span class="row-main"><span class="row-label wrap">${esc(x.what)}</span>${x.detail ? `<span class="row-sub wrap">${esc(x.detail)}</span>` : ''}</span></div>`).join('')}</div>
      <p class="group-foot">Every hour, to Attic NAS, Backblaze B2 and Proton Drive; the first at 15:00. Its restore is tested every Sunday, the first on 15 Nov. Its database and secrets also go into the weekly archive.${d.resources.grows ? ` ${esc(d.resources.grows[0].toUpperCase() + d.resources.grows.slice(1))} will grow your backups: Attic NAS has 3.97 TB free, Proton Drive 2.9 TB.` : ''}</p></section>`);
    out.push(`<section>${q('Resources', `Checked against ${esc(n.name)}`)}<div class="group"><div class="audience-box">
      ${A.meter({ label: 'Memory', used: +used.toFixed(1), add: d.resources.memGB, total: n.memGB, fmt: (v) => v.toFixed(1), unit: ' GB', note: `${esc(d.name)} needs ${d.resources.memGB} GB; ${(n.memGB - used - d.resources.memGB).toFixed(1)} GB stays free` })}
      ${A.meter({ label: disk.name, used: disk.usedGB, add: d.resources.diskGB, total: disk.sizeGB, note: d.resources.grows ? `${d.resources.diskGB} GB at first, then ${esc(d.resources.grows)}` : `${d.resources.diskGB} GB for the app` })}
      ${d.resources.gpu !== 'none' ? `<ul class="lines"><li>${I('gpu')}<span>${n.gpu ? (p.chosen.some((x) => x.needs === 'gpu') ? `Uses ${esc(n.gpu.name)}` : `${esc(n.gpu.name)} is left out`) : `${esc(n.name)} has no GPU`}<small>${d.resources.gpu === 'required' ? 'Required' : `Optional, for ${esc(d.resources.gpuWhy)}`}</small></span></li></ul>` : ''}
    </div></div></section>`);
    out.push(`<section>${q('Extras', p.chosen.length ? `<span class="needs-key">${I('key')}Needs your passkey</span>` : 'Beyond the floor')}<div class="group">${p.extras.length ? p.extras.map((x) => !x.applicable
      ? `<div class="row is-top"><span class="row-main"><span class="row-title">${esc(x.text.replace(' on ' + n.name, ''))}: not on ${esc(n.name)}</span><span class="row-sub wrap">${x.needs === 'gpu' ? `${esc(n.name)} has no GPU. ` : `${esc(n.name)} has no such device. `}${esc(x.without)}</span></span></div>`
      : `<label class="row is-top extra-row ${x.chosen ? '' : 'is-off'}"><span class="row-main"><span class="row-title">${esc(x.text)}</span><span class="row-sub wrap">${esc(x.detailText)}</span><span class="row-sub wrap">${esc(x.without)}</span></span>${x.optional ? `<input type="checkbox" class="switch" data-change="plan-extra" data-app="${p.appId}" data-extra="${x.id}" ${x.chosen ? 'checked' : ''} aria-label="Include: ${esc(x.text)}">` : '<span class="row-value">Required</span>'}</label>`).join('')
      : '<div class="row"><span class="row-main"><span class="row-label">None. This plan needs no passkey.</span></span></div>'}</div></section>`);
    out.push(`<section>${q('Secrets')}<div class="group"><div class="row is-top"><span class="row-main"><span class="row-label wrap">${d.secrets.length ? `${d.secrets.length} ${d.secrets.length === 1 ? 'secret' : 'secrets'}, made on ${esc(n.name)} and ${tpm}: ${esc(and(d.secrets))}. Nobody sees them, you included.` : 'None: it keeps nothing secret.'}</span></span></div></div></section>`);
    return out.join('');
  }
  function creates(p) {
    const d = p.def;
    return `<ul class="lines">
      <li>${I('user')}<span>${d.signIn.method === 'native' ? `A sign-in client, “${esc(d.name)}”, in ${esc(D.household.idp.name)}` : `A guard at the edge on ${esc(p.node.name)}`}</span></li>
      <li>${I('mesh')}<span>The name <span class="host">${esc(p.host)}</span></span></li>
      ${d.secrets.length ? `<li>${I('lock')}<span>${d.secrets.length} sealed ${d.secrets.length === 1 ? 'secret' : 'secrets'}</span></li>` : ''}
      <li>${I('backups')}<span>Hourly backups and a Sunday restore test</span></li>
      <li>${I('home')}<span>A place on Home for ${esc(and(p.people.map((x) => x.short)))}</span></li></ul>`;
  }
  const installButton = (p) => `<button class="btn btn-primary btn-lg btn-block" data-act="install" data-id="${p.appId}">${p.risky ? `${I('key')}Install with your passkey` : 'Install'}</button>`;
  function planCard(p) {
    return `<div class="card plan-card"><h2 class="card-title">What it creates</h2>${creates(p)}
      ${p.risky ? `<div class="approves"><p class="group-head">Your passkey approves</p><ul class="lines">${p.chosen.map((x) => `<li>${I(x.kind === 'network' ? 'globe' : x.needs === 'gpu' ? 'gpu' : 'key')}<span>${esc(x.text)}<small>${esc(x.detailText)}</small></span></li>`).join('')}<li>${I('check')}<span>Installing ${esc(p.def.name)} on ${esc(p.node.name)}, as above</span></li></ul></div>` : `<p class="subhead">Nothing in this plan is risky, so it needs no passkey.</p>`}
      ${installButton(p)}
      <p class="stepup-bound">${I('lock')}<span>${p.risky ? `One passkey approves plan <span class="plan-id">${esc(p.id)}</span> and nothing else.` : `Plan <span class="plan-id">${esc(p.id)}</span>.`} Change anything and it becomes a new plan.</span></p></div>`;
  }

  // ---------- changing the plan makes a new plan, with a new address ----------
  const replan = (appId) => { const p = buildPlan(appId); history.replaceState(null, '', location.pathname + location.search + '#/plans/' + p.id); render(); };
  act('plan-node', (t) => { draft(t.dataset.app).node = t.value; log(`plan for ${t.dataset.app}: node ${t.value}`); replan(t.dataset.app); });
  act('plan-extra', (t) => { draft(t.dataset.app).extras[t.dataset.extra] = t.checked; log(`plan for ${t.dataset.app}: extra ${t.dataset.extra} ${t.checked ? 'in' : 'out'}`); soon(() => replan(t.dataset.app)); });
  A.replan = replan; // audience chips on a plan page change the plan too (admin-apps.js)
  act('install', async (t) => {
    const p = buildPlan(t.dataset.id);
    if (p.risky && !approvalFor(p.id)) {
      const ok = await stepUp({ plan: p, title: `Install ${esc(p.def.name)}`, action: 'Install with my passkey', sub: `This plan gives ${esc(p.def.name)} ${p.chosen.length === 1 ? 'an extra' : 'extras'}, so HomersLab asks for your passkey: once, for the whole plan.`,
        what: [...p.chosen.map((x) => ({ icon: x.kind === 'network' ? 'globe' : 'gpu', text: `${esc(x.text)}<small>${esc(x.detailText)}</small>` })), { icon: 'check', text: `Install ${esc(p.def.name)} on ${esc(p.node.name)}, for ${esc(and(p.people.map((x) => x.short)))}` }] });
      if (!ok) return; S.approvals.push(ok);
    }
    if (t.isConnected) A.morphTo([[t, 'cta', '.a-progress'], [document.querySelector('.a-ident .app-icon'), 'hero', '.a-ident .app-icon']]);
    startInstall(p);
  });

  // ---------- the install, with progress ----------
  function steps(p) {
    const d = p.def, n = p.node, ap = approvalFor(p.id);
    const out = [
      { t: 'Checked the plan', s: `${D.catalogs.find((k) => k.id === d.catalog).name}’s signature is good; it meets the floor` },
      d.vendor ? { t: 'Installed Plex’s own package, checked against Plex’s key', s: d.vendor } : { t: `Pulled ${d.images.count} ${d.images.count === 1 ? 'image' : 'images'}, pinned by digest`, s: d.images.size },
    ];
    if (d.secrets.length) out.push({ t: `Made ${d.secrets.length} ${d.secrets.length === 1 ? 'secret' : 'secrets'}`, s: n.tpm ? `Sealed to ${n.name}’s TPM; nobody sees them` : 'Sealed with a root-only key; nobody sees them' });
    out.push(d.signIn.method === 'native' ? { t: `Created the sign-in client “${d.name}”`, s: `In ${D.household.idp.name}` } : { t: 'Set the edge to ask for the household account', s: `On ${n.name}` });
    out.push({ t: `Named it ${p.host}`, s: `On ${meshWords()}, pointing at ${n.name}` });
    for (const x of p.chosen) out.push({ t: x.text.replace(/^Use/, 'Gave it'), s: ap ? `Approved by ${ap.by} at ${ap.at}, plan ${p.id}` : '' });
    out.push({ t: `Started ${d.name} on ${n.name}`, s: '' });
    out.push({ t: 'First health check', s: 'Its web page answers', health: true });
    out.push({ t: 'Backups scheduled', s: 'Every hour, the first at 15:00; the first restore test on Sunday 15 Nov' });
    out.push({ t: 'On Home', s: `For ${and(p.people.map((x) => x.short))}` });
    return out;
  }
  function startInstall(p) {
    const fail = Proto.get('outcome', 'ok') === 'fail';
    S.installs[p.id] = { appId: p.appId, step: 0, steps: steps(p), plan: { id: p.id, node: p.node.id, people: p.people.map((x) => x.short), chosen: p.chosen.map((x) => ({ id: x.id, words: x.text })) }, fail, done: false, failed: false, t0: hhmm(now()) };
    log(`install ${p.appId} started (${p.id})`);
    go(`#/install/${p.id}`);
    const job = S.installs[p.id];
    const tick = () => {
      const cur = job.steps[job.step];
      if (cur.health && job.fail) { job.failed = true; job.done = true; log(`install ${p.appId} failed its first health check; undone`); if (location.hash === `#/install/${p.id}`) render(); return; }
      cur.at = hhmm(now()); job.step++;
      if (job.step >= job.steps.length) { job.done = true; finishInstall(p); }
      if (location.hash === `#/install/${p.id}`) render();
      if (!job.done) setTimeout(tick, 700);
    };
    setTimeout(tick, 600);
  }
  function finishInstall(p) {
    const d = p.def, was = rem(p.appId), ap = approvalFor(p.id);
    if (was) S.removed = S.removed.filter((x) => x !== was);
    S.installed.push({ id: p.appId, node: p.node.id, audience: clone(draft(p.appId).audience), exposure: 'mesh', version: d.version, since: UI.dayMonth(now()), memNowGB: +(d.resources.memGB * 0.55).toFixed(1), dataGB: was ? was.dataGB : 0.1,
      update: null, auto: false, lastBackup: null, health: { state: 'working', since: UI.dayMonth(now()), check: hhmm(now()), ms: 210 }, restore: Array(12).fill(null), restoreMin: 5,
      extrasApproved: p.chosen.map((x) => ({ id: x.id, words: x.text, by: ap?.by, when: ap?.at })), history: [] });
    delete S.drafts[p.appId];
    // the Store remembers it for this tab, and a request for it is answered: the requester hears it on Home
    const st = ST.state(); st.installs.push({ record: S.installed[S.installed.length - 1], people: p.people.map((x) => x.id) });
    const asked = st.requests.filter((r) => r.app === p.appId && (r.state === 'approved' || r.state === 'waiting'));
    for (const r of asked) { r.state = 'installed'; r.answer = { by: 'ines', when: `Today ${hhmm(now())}`, fresh: true }; }
    ST.save();
    log(`install ${p.appId} done`); toast(`${d.name} is installed. ${and(p.people.map((x) => x.short))} see it on Home now.${asked.length ? ` ${and(asked.flatMap((r) => r.asks.map((a) => ST.person(a.by).short)))} also hear that you answered.` : ''}`);
  }
  route(/^#\/install\/(p-[0-9a-f]+)$/, (m) => {
    const job = S.installs[m[1]];
    if (!job) return `<div class="page-head"><div><h1>No install running</h1><p class="page-sub">This page shows an install while it runs. <a class="link" href="#/store">Back to the Store</a>.</p></div></div>`;
    const d = cat(job.appId), n = node(job.plan.node), ap = approvalFor(job.plan.id), total = job.steps.length;
    const done = job.done && !job.failed;
    const failText = 'Its web page didn’t answer within 4 minutes';
    return `
      <div class="page-head"><div class="a-ident" data-app-id="${d.id}">${appIcon(d, 'app-icon-lg')}<div><h1>${job.failed ? `${esc(d.name)} wasn’t installed` : done ? `${esc(d.name)} is ready` : `Installing ${esc(d.name)}`}</h1>
        <p class="page-sub">Plan <span class="plan-id">${esc(job.plan.id)}</span>${ap ? `, approved by ${esc(ap.by)} with her passkey at ${esc(ap.at)}` : ', no passkey needed'} · started ${esc(job.t0)} on ${esc(n.name)}</p></div></div>
        <div class="page-actions">${done ? `<button class="btn" data-act="open-app" data-host="${esc(host(d.id))}">${I('external')}Open ${esc(d.name)}</button><a class="btn btn-primary" href="#/apps/${d.id}">Go to its page${I('arrowRight')}</a>` : ''}</div></div>
      <div class="split"><section class="card stack a-progress" aria-label="Progress" style="gap:12px">
        <div class="progress-row"><div class="progress-head"><b>${job.failed ? 'Undone' : done ? 'Done' : 'Installing'}</b><span>${Math.min(job.step, total)} of ${total}</span></div><div class="progress" role="progressbar" aria-label="Install progress" aria-valuemin="0" aria-valuemax="${total}" aria-valuenow="${job.step}"><span style="--value:${(job.step / total) * 100}%"></span></div></div>
        <ol class="steps" aria-live="polite">${job.steps.map((s, i) => { const st = job.failed && i === job.step ? 'fail' : i < job.step ? (job.failed ? 'undone' : 'done') : i === job.step && !job.done ? 'now' : 'todo'; const sub = st === 'fail' ? failText : st === 'undone' ? 'Taken away again' : s.s; return `<li class="step is-${st}${st === 'done' && i === job.step - 1 ? ' is-new' : ''}"><span class="step-mark">${st === 'done' ? I('check2') : st === 'fail' ? I('x') : st === 'undone' ? I('rollback') : st === 'now' ? '<span class="spinner"></span>' : ''}</span><span class="step-text"><span class="step-title">${esc(s.t)}</span>${sub ? `<span class="step-sub">${esc(sub)}</span>` : ''}<span class="sr-only">${st === 'done' ? 'Done' : st === 'fail' ? 'Failed' : st === 'undone' ? 'Undone' : st === 'now' ? 'In progress' : 'Not started'}</span></span><span class="step-time">${s.at || ''}</span></li>`; }).join('')}</ol></section>
        <aside class="rail">${job.failed ? `<div class="card stack" style="gap:12px"><h2 class="card-title">HomersLab undid the install</h2><p class="muted">Its first health check failed, so everything the plan made was taken away again: the name, the sign-in client, the secrets and the containers. Nothing else in the household changed.</p><div class="btn-row"><a class="btn btn-primary" href="#/store/app/${d.id}">Back to ${esc(d.name)}</a><button class="btn" data-act="toast-logs">See its logs</button></div></div>`
          : done ? `<div class="card stack" style="gap:12px"><h2 class="card-title">Next</h2><ul class="lines"><li>${I('home')}<span>${esc(and(job.plan.people))} see ${esc(d.name)} on Home now</span></li><li>${I('backups')}<span>First backup at 15:00, then every hour</span></li><li>${I('globe')}<span>To make it public, go to its page</span></li></ul></div>`
          : `<div class="card stack" style="gap:12px"><h2 class="card-title">While it installs</h2><p class="muted">You can leave this page; the install carries on, and Apps shows it when it is done. If its first health check fails, HomersLab undoes everything the plan made.</p></div>`}</aside></div>`;
  }, { back: (m) => { const job = S.installs[m[1]]; return job ? { href: `#/store/app/${job.appId}`, label: cat(job.appId).name } : { href: '#/store', label: 'Store' }; } });
  act('toast-logs', () => toast('Logs aren’t part of this prototype.', 'info'));
})();
