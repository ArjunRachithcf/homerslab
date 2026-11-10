// HomersLab prototypes: the Store, shared by Admin (where apps are got) and Home (where members ask for them). PROTOTYPE.
// "Store" is the place you browse; the catalog is the signed collection of app definitions behind it. Buttons say Get,
// Open, Update (Admin) or Ask for it (Home); plans and progress say install.
// Requests and what was installed for them live in this tab's sessionStorage, so an answer given in Admin reaches the
// requester's Home in the same tab. A new tab starts again from the fake household.
(() => {
  const D = HL_DATA, { esc, and, appIcon, avatar } = UI;
  const KEY = 'hl-proto-store';
  const clone = (x) => JSON.parse(JSON.stringify(x));
  let st = null;
  function state() {
    if (st) return st;
    try { const s = JSON.parse(sessionStorage.getItem(KEY) || 'null'); if (s && s.v === 1) return (st = s); } catch (e) {}
    return (st = { v: 1, requests: clone(D.requests), installs: [] });
  }
  const save = () => { try { sessionStorage.setItem(KEY, JSON.stringify(st)); } catch (e) {} };
  const cat = (id) => D.catalog.find((a) => a.id === id);
  const person = (id) => D.people.find((p) => p.id === id);
  const purpose = (id) => D.categories.find((k) => k.id === id);
  const TIER = { verified: 'Verified', community: 'Community', third: 'Third-party' }, TIER_ICON = { verified: 'shield', community: 'people', third: 'key' };
  const tier = (t) => `<span class="tier">${I(TIER_ICON[t])}${TIER[t]}</span>`;
  const who = (r) => and(r.asks.map((a) => person(a.by).short));
  const what = (r) => (r.app ? cat(r.app).name : `“${r.text}”`);
  const open = () => state().requests.filter((r) => r.state === 'waiting');
  // the words a request's answer reaches people by
  const reach = (p) => `on Home${p.mail ? ', by mail' : ''}${p.ntfy ? ' and by ntfy' : ''}`;
  const kidOk = (d) => !!(d.kidView || d.kidOk);

  // ---------- pieces ----------
  // a Get/Open/Update/Ask pill: a link or an action
  const pill = (b) => (b.href ? `<a class="store-get ${b.primary ? 'is-primary' : ''}" href="${b.href}" ${b.label2 ? `aria-label="${esc(b.label2)}"` : ''}>${esc(b.label)}</a>`
    : `<button type="button" class="store-get ${b.primary ? 'is-primary' : ''}" ${b.attrs || ''} ${b.disabled ? 'disabled' : ''} ${b.label2 ? `aria-label="${esc(b.label2)}"` : ''}>${esc(b.label)}</button>`);
  // one app in a shelf or a list: icon, name, purpose, its pill. The row is a link to the app's page; the pill sits beside it
  const item = (d, o) => `<div class="store-item" data-app-id="${d.id}"><a class="store-item-link" href="${o.href(d)}" data-morph>${appIcon(d, 'app-icon-store')}<span class="store-item-text"><b data-name>${esc(d.name)}</b><span>${esc(o.sub ? o.sub(d) : d.purpose)}</span></span></a>${pill(o.btn(d))}</div>`;
  const hero = (h, o) => {
    const d = cat(h.app);
    return `<section class="store-hero glass" aria-labelledby="hero-t"><div class="store-hero-art" aria-hidden="true">${appIcon(d, 'store-hero-icon')}</div>
      <div class="store-hero-text"><p class="store-kicker">${esc(h.kicker)}</p><h2 id="hero-t">${esc(h.title)}</h2><p class="store-hero-sub">${esc(h.sub)}</p>
        <div class="store-hero-app">${appIcon(d, 'app-icon-store')}<span class="store-item-text"><b>${esc(d.name)}</b><span>${esc(d.purpose)}</span></span>${pill(o.btn(d))}<a class="link store-hero-more" href="${o.href(d)}" data-morph>About ${esc(d.name)}</a></div></div></section>`;
  };
  const shelf = (c, apps, o) => (apps.length ? `<section class="store-shelf" aria-labelledby="sh-${c.id}"><div class="store-shelf-head"><div><h2 id="sh-${c.id}">${esc(c.name)}</h2>${c.sub ? `<p>${esc(c.sub)}</p>` : ''}</div>${c.all ? `<a class="link" href="${c.all}">See all</a>` : ''}</div>
      ${apps.length > 6 ? `<div class="card store-scroll-card"><div class="store-scroll" tabindex="0" role="group" aria-label="${esc(c.name)}, scrolls sideways">${apps.map((d) => item(d, o)).join('')}</div></div>` : `<div class="card store-grid">${apps.map((d) => item(d, o)).join('')}</div>`}</section>` : '');
  const cats = (list, href) => `<section class="store-shelf" aria-labelledby="sh-cats"><div class="store-shelf-head"><div><h2 id="sh-cats">By what it is for</h2></div></div>
    <div class="card store-cats">${list.map((k) => `<a class="store-cat" href="${href(k)}"><span class="store-cat-mark" aria-hidden="true">${I(CAT_ICON[k.id] || 'apps')}</span><span>${esc(k.name)}</span>${I('chevronRight', 'icon row-chev')}</a>`).join('')}</div></section>`;
  const CAT_ICON = { films: 'tv', photos: 'eye', music: 'play', books: 'catalog', documents: 'folder', knowledge: 'link', home: 'home', money: 'calendar', passwords: 'key', assistants: 'sparkle' };
  const search = (id, value, label) => `<div class="a-search store-search" role="search">${I('search')}<label class="sr-only" for="${id}">${esc(label)}</label><input id="${id}" type="search" placeholder="${esc(label)}" value="${esc(value || '')}" autocomplete="off"></div>`;

  // an app's page in the Store
  function page(d, o) {
    const k = D.catalogs.find((x) => x.id === d.catalog);
    const r = d.resources;
    const needs = [`${r.memGB} GB of memory`, `${r.diskGB} GB of disk${r.grows ? `, then ${r.grows}` : ''}`, r.gpu === 'required' ? `a GPU (${r.gpuMin})` : r.gpu === 'optional' ? `a GPU if there is one, for ${r.gpuWhy}` : 'no GPU'];
    const info = [
      ['Catalog', `${k.name}, signed by ${k.publisher}`], ['Tier', TIER[d.tier]], ['Version', `${d.version}${d.tested !== '—' ? `, tested ${d.tested}` : ''}`],
      ['Size', d.vendor ? 'Its maker’s own package' : `${d.images.size}, ${d.images.count} ${d.images.count === 1 ? 'image' : 'images'}`], ['Needs', needs.join('; ')],
      ['Extras', d.extras.length ? d.extras.map((x) => x.words.replace(' on {node}', '').replace('{node}', 'its node') + (x.optional ? ' (optional)' : '')).join('; ') : 'None'],
      ['Kids', d.kidView ? `Kid view: ${d.kidView.toLowerCase()}` : d.kidOk ? 'Fine for kids, with the same view as adults' : 'No kid view'],
    ];
    const wired = [
      ['Sign-in', d.signIn.method === 'native' ? 'Native: with the household account, straight into the app' : 'Guarded: the edge asks for the household account first'],
      ['Backups', `Every hour: ${and(d.data.filter((x) => !x.what.startsWith('Not')).map((x) => x.what.toLowerCase()))}. A restore test every Sunday`],
      ['Exposure', d.publicAllowed ? 'Mesh only at first; it can be made public later' : 'Mesh only, always'],
    ];
    const news = D.store.news[d.id] || `Version ${d.version}, tested by the catalog in CI${d.tested !== '—' ? ` on ${d.tested}` : ''}.`;
    const also = D.catalog.filter((x) => x.id !== d.id && x.category === d.category).concat(D.catalog.filter((x) => x.id !== d.id && x.category !== d.category && D.store.collections.some((c) => c.apps.includes(d.id) && c.apps.includes(x.id)))).filter((x) => !o.hide || !o.hide(x)).slice(0, 6);
    return `
      <div class="store-app-head a-ident" data-app-id="${d.id}">${appIcon(d, 'app-icon-xl')}
        <div class="store-app-names"><h1>${esc(d.name)}</h1><p class="store-app-purpose">${esc(d.purpose)} · ${esc(purpose(d.category).name)}</p><div class="store-app-badges">${tier(d.tier)}</div>
          <div class="store-app-act">${pill({ ...o.btn(d), primary: true })}${o.actNote ? `<span class="store-app-note">${o.actNote}</span>` : ''}</div></div></div>
      ${o.notice || ''}
      <section class="store-sec" aria-labelledby="st-shots"><h2 class="sr-only" id="st-shots">Screenshots</h2>
        <div class="store-shots" tabindex="0" role="group" aria-label="Screenshots, scroll sideways">${d.screenshots.map((s, i) => `<figure class="store-shot"><div class="store-shot-pic" aria-hidden="true" style="--i:${i}">${appIcon(d)}<span></span><span></span><span></span></div><figcaption>${esc(s)}</figcaption></figure>`).join('')}</div></section>
      <p class="store-about">${esc(d.blurb)} ${esc(d.about)}</p>
      <div class="store-cols">
        <section class="store-sec" aria-labelledby="st-new"><h2 id="st-new">What’s new</h2><div class="card store-card"><p class="store-new-v">Version ${esc(d.version)}</p><p>${esc(news)}</p></div></section>
        <section class="store-sec" aria-labelledby="st-wired"><h2 id="st-wired">How it’s wired</h2><div class="card store-card"><dl class="store-dl">${wired.map(([a, b]) => `<div><dt>${esc(a)}</dt><dd>${esc(b)}</dd></div>`).join('')}</dl></div></section>
      </div>
      <section class="store-sec" aria-labelledby="st-info"><h2 id="st-info">Information</h2><div class="card store-card"><dl class="store-dl store-dl-2">${info.map(([a, b]) => `<div><dt>${esc(a)}</dt><dd>${esc(b)}</dd></div>`).join('')}</dl></div></section>
      ${also.length ? shelf({ id: 'also', name: 'You might also like' }, also, o) : ''}`;
  }

  // a request, as one line of words: "Tomás and Rita asked for Audiobookshelf"
  const title = (r) => `${who(r)} asked for ${r.app ? cat(r.app).name : `“${r.text}”`}`;
  const STATE = { waiting: ['Waiting', 'neutral'], approved: ['Approved', 'green'], installed: ['Approved', 'green'], notnow: ['Not now', 'orange'] };
  const statePill = (r) => `<span class="pill pill-${STATE[r.state][1]}">${STATE[r.state][0]}</span>`;
  const asks = (r) => `<ul class="store-asks">${r.asks.map((a) => { const p = person(a.by); return `<li>${avatar(p, 'avatar-sm')}<span><b>${esc(p.short)}</b>${p.role === 'kid' ? ` <span class="muted">(${p.age})</span>` : ''} · ${esc(a.when)}${a.note ? `<q>${esc(a.note)}</q>` : ''}</span></li>`; }).join('')}</ul>`;

  window.STORE = { state, save, clone, cat, person, purpose, tier, TIER, who, what, open, reach, kidOk, pill, item, hero, shelf, cats, search, page, title, statePill, asks };
})();
