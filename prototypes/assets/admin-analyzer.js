// HomersLab Admin prototype: the disk analyzer (ADR 0017), for admins only. PROTOTYPE, throwaway.
// Opening it asks for the passkey (it shows file names across the household's data); the session lasts 10 minutes.
// It deletes only in storage locations and HomersLab's own leftovers; inside an app's data it offers the app's own
// cleanup. Every delete asks for the passkey again, bound to the exact items and their size. Deleted items stay in
// "Recently deleted" for 7 days, kept by a btrfs snapshot.
(() => {
  const { D, S, cat, node, and, route, render, go, act, log } = A;
  const { esc, toast, dialog, stepUp, hhmm, now } = UI;
  const size = HL.size;
  const AZ = (S.az = S.az || { until: 0, gone: {}, deleted: [], sel: {}, shared: {} });
  const SESSION = 10 * 60 * 1000;
  const open = () => AZ.until > Date.now();
  const disks = () => S.nodes.flatMap((n) => n.disks.map((d) => ({ n, d }))).filter((x) => D.analyzer[x.d.id]);
  const OWNER = (o) => (!o ? '—' : o === 'leftover' ? 'HomersLab’s leftovers' : o === 'system' ? 'The system' : o.startsWith('st:') ? S.storage.find((s) => 'st:' + s.id === o)?.name || 'A storage location' : cat(o)?.name || o);
  const TONE_TYPE = { Video: 2, Audio: 3, Books: 6, Documents: 6, Mixed: 4, Cache: 1, Snapshots: 5, 'App data': 1, Logs: 4, 'Move copy': 4, 'Removed app': 4, Images: 4, System: 5, 'Disk image': 4 };
  const toneOf = (x) => (x.owner === 'leftover' ? 4 : x.owner === 'system' ? 5 : x.owner && !x.owner.startsWith('st:') ? 1 : TONE_TYPE[x.type] || 2);

  // the tree, with each item's path, inherited owner, and what may be done with it
  function walk(root, diskId) {
    const all = [];
    const go1 = (x, path, owner, parent) => {
      const o = x.owner || owner, p = path ? `${path}/${x.name}` : x.name;
      const item = { ...x, owner: o, path: p, key: `${diskId}:${p}`, parent };
      item.canDelete = !!(o && (o === 'leftover' || o.startsWith('st:')) && !x.group && !x.inApp && !x.note && !x.kids); // a folder: choose inside it
      item.why = x.inApp ? `Inside ${OWNER(o)}’s own data: deleting behind its back would corrupt it. Use its own cleanup.` : o === 'system' ? 'The system and the snapshots backups are made from: HomersLab looks after these itself.' : x.group ? `${x.name.replace(/^Other /, '').replace(/ \(.*/, '')}: open the storage location in a file manager to choose among them.` : '';
      if (AZ.gone[item.key]) return null;
      item.kids = x.kids ? x.kids.map((k) => go1(k, p, o, item)).filter(Boolean) : null;
      if (item.kids) item.gb = item.kids.reduce((s, k) => s + k.gb, 0);
      all.push(item); return item;
    };
    const top = go1(root, '', null, null);
    return { top, all };
  }
  const leaves = (all) => all.filter((x) => !x.kids);

  // ---------- the session: a ring with the minutes left ----------
  let timer = null;
  const left = () => Math.max(0, AZ.until - Date.now());
  const pill = () => { const s = Math.ceil(left() / 1000), p = left() / SESSION; return `<span class="session-pill ${s < 120 ? 'is-low' : ''}" role="timer" aria-live="off" aria-label="${Math.ceil(s / 60)} minutes left before the analyzer locks"><span class="session-ring" style="--p:${p.toFixed(3)}" aria-hidden="true"></span><span data-az-left>${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}</span> left</span>`; };
  function tick() {
    clearInterval(timer);
    timer = setInterval(() => {
      if (!location.hash.startsWith('#/analyzer')) return clearInterval(timer);
      if (!open()) { clearInterval(timer); toast('The analyzer locked: its 10 minutes are up.', 'lock'); render(); return; }
      const el = document.querySelector('.session-pill'); if (el) el.outerHTML = pill();
    }, 1000);
  }

  route(/^#\/analyzer$/, (m, q) => {
    if (!open()) return `<div class="page-head"><div><h1>Disk analyzer</h1><p class="page-sub">What fills each disk, down to the file. It shows file names from across the household’s data, so it opens with your passkey, for up to 10 minutes.</p></div></div>
      <div class="card stack az-lock" style="gap:14px"><span class="az-lock-mark" aria-hidden="true">${I('lock')}</span><h2 class="card-title">Open the analyzer</h2>
        <p class="muted">For admins only. Every delete asks for your passkey again, bound to exactly what you chose, and deleted items can be restored for 7 days.</p>
        <div class="btn-row"><button class="btn btn-primary" data-act="az-open">${I('key')}Open with your passkey</button></div></div>`;
    const id = q.get('disk') || 'an-media', x = disks().find((y) => y.d.id === id) || disks()[0], view = q.get('view') || 'map', sort = q.get('sort') || 'size';
    const { top, all } = walk(D.analyzer[x.d.id], x.d.id);
    const here = (q.get('path') && all.find((y) => y.path === q.get('path') && y.kids)) || top;
    const sel = Object.values(AZ.sel).filter((s) => s.disk === x.d.id);
    const crumbs = []; for (let c = here; c; c = c.parent) crumbs.unshift(c);
    const url = (o) => { const p = new URLSearchParams({ disk: x.d.id, view, sort, ...(here !== top && view === 'map' ? { path: here.path } : {}), ...o }); for (const [k, v] of [...p]) if (v === '' || v == null) p.delete(k); return `#/analyzer?${p}`; };
    const views = [['map', 'Space map'], ['biggest', 'Biggest'], ['oldest', 'Oldest'], ['dups', 'Duplicates'], ['people', 'Per person'], ['deleted', `Recently deleted${AZ.deleted.length ? ` <span class="count">${AZ.deleted.length}</span>` : ''}`]];
    const row = (y, depth = true) => `<tr data-row="${esc(y.key)}" class="${y.kids && depth ? 'link-row' : ''}" ${y.kids && depth ? `data-act="row" data-href="${url({ path: y.path })}"` : ''}>
        <td class="az-sel">${y.canDelete ? `<input type="checkbox" class="tick" data-change="az-sel" data-key="${esc(y.key)}" ${AZ.sel[y.key] ? 'checked' : ''} aria-label="Choose ${esc(y.name)}">` : ''}</td>
        <td><div class="ident"><span class="usage-key tone-${toneOf(y)}" aria-hidden="true"></span><div class="names">${y.kids && depth ? `<a href="${url({ path: y.path })}"><b>${esc(y.name)}</b></a>` : `<b>${esc(y.name)}</b>`}<span>${y.dup || y.dupOf ? 'Has a duplicate · ' : ''}${!y.canDelete && y.why ? esc(y.why.split(':')[0]) : y.leftover ? esc(y.leftover) : esc(y.path.split('/').slice(1, -1).join(' › ') || OWNER(y.owner))}</span></div></div></td>
        <td class="n num">${size(y.gb)}</td><td class="hide-sm">${esc(y.age || '—')}</td><td class="hide-sm">${esc(y.type || 'Folder')}</td><td class="hide-sm">${esc(OWNER(y.owner))}</td>
        <td class="end">${y.inApp && y.cleanup ? `<button class="btn btn-sm" data-act="az-cleanup" data-what="${esc(y.cleanup)}">Its own cleanup</button>` : ''}</td></tr>`;
    const by = { size: (a, b) => b.gb - a.gb, age: (a, b) => (b.old ? 1 : 0) - (a.old ? 1 : 0) || String(a.age).localeCompare(String(b.age)), type: (a, b) => String(a.type).localeCompare(String(b.type)), owner: (a, b) => OWNER(a.owner).localeCompare(OWNER(b.owner)) };
    const table = (items, depth = true) => `<div class="group"><table class="rtable has-lead az-table"><thead><tr><th class="az-sel"><span class="sr-only">Choose</span></th><th>Name</th><th class="n">Size</th><th class="hide-sm">Age</th><th class="hide-sm">Type</th><th class="hide-sm">Owner</th><th class="end"><span class="sr-only">Cleanup</span></th></tr></thead><tbody>${items.map((y) => row(y, depth)).join('')}</tbody></table></div>`;
    let body = '';
    if (view === 'map') {
      const kids = [...(here.kids || [])].sort(by[sort]);
      body = `<nav class="az-crumbs" aria-label="Where you are">${crumbs.map((c, i) => i === crumbs.length - 1 ? `<span aria-current="location">${esc(c.name)}</span>` : `<a class="link" href="${url({ path: c === top ? '' : c.path })}">${esc(c.name)}</a>`).join(' › ')}</nav>
        ${HL.treemap(kids.map((k) => ({ id: k.key, label: k.name, value: k.gb, tone: toneOf(k), attrs: k.kids ? `data-act="az-drill" data-to="${esc(url({ path: k.path }))}"` : `data-act="az-tile" data-key="${esc(k.key)}"` })), { label: `What fills ${here.name}` })}
        <div class="az-sort"><span class="subhead" id="az-sort-l">Sort by</span><div class="segmented" style="--n:4" role="radiogroup" aria-labelledby="az-sort-l" data-seg="az-sort"><span class="segmented-thumb" aria-hidden="true"></span>${['size', 'age', 'type', 'owner'].map((k) => `<button type="button" role="radio" aria-checked="${sort === k}" data-act="az-go" data-to="${esc(url({ sort: k }))}">${k[0].toUpperCase() + k.slice(1)}</button>`).join('')}</div></div>
        ${table(kids)}`;
    } else if (view === 'biggest') body = `<p class="az-lead">The biggest single items on ${esc(x.d.name)}.</p>${table(leaves(all).filter((y) => !y.group).sort((a, b) => b.gb - a.gb).slice(0, 10), false)}`;
    else if (view === 'oldest') body = `<p class="az-lead">Not opened in a year or more.</p>${table(leaves(all).filter((y) => y.old).sort((a, b) => b.gb - a.gb), false)}`;
    else if (view === 'dups') {
      const pairs = []; for (const y of all) if (y.dup) { const o = all.find((z) => z.dupOf === y.dup); if (o) pairs.push([o, y]); }
      body = `<p class="az-lead">The same bytes, stored twice. Free one copy, or share them as reflinks: both names stay, the space is counted once.</p>${pairs.length ? `<div class="stack" style="gap:var(--gap)">${pairs.map(([o, y]) => `<div class="card stack" style="gap:12px" data-row="${esc(y.key)}"><div class="group">${[o, y].map((z) => `<div class="row"><span class="row-main"><span class="row-title">${esc(z.name)}</span><span class="row-sub">${esc(z.path.split('/').slice(1, -1).join(' › '))} · ${esc(OWNER(z.owner))}</span></span><span class="row-value num">${size(z.gb)}</span></div>`).join('')}</div>
          <div class="btn-row">${AZ.shared[y.key] ? `<span class="pill pill-green">Shared as reflinks: ${size(y.gb)} freed</span>` : `<button class="btn" data-act="az-reflink" data-key="${esc(y.key)}">Share as reflinks, free ${size(y.gb)}</button><button class="btn" data-act="az-del-one" data-key="${esc(y.key)}">${I('trash')}Delete the copy in ${esc(y.path.split('/').slice(-2, -1)[0])}</button>`}</div></div>`).join('')}</div>` : `<div class="group"><div class="empty">${I('check')}<p class="empty-title">No duplicates left</p></div></div>`}`;
    } else if (view === 'people') {
      const max = Math.max(...D.perPerson.map((p) => p.gb), 1);
      body = `<p class="az-lead">Space per person, as far as apps report it. Only Paperless-ngx reports owners in this household.</p><div class="group">${D.perPerson.map((p) => { const ps = S.people.find((x) => x.id === p.id); return `<div class="row"><span class="row-lead">${A.avatar(ps, 'avatar-sm')}</span><span class="row-main"><span class="row-title">${esc(ps.name)}</span><span class="row-sub">${esc(p.from)}</span></span><span class="az-bar" aria-hidden="true"><span style="width:${(p.gb / max) * 100}%"></span></span><span class="row-value num">${p.gb ? size(p.gb) : '—'}</span></div>`; }).join('')}</div>`;
    } else {
      const total = AZ.deleted.reduce((s, y) => s + y.gb, 0);
      body = `<p class="az-lead">Deleted items stay for 7 days, kept by a snapshot, so their space comes back only after that, or now.</p>${AZ.deleted.length ? `<div class="group lead-sm">${AZ.deleted.map((y, i) => `<div class="row is-top row-wrap" data-row="del-${esc(y.key)}"><span class="row-lead">${I('trash', 'icon icon-md')}</span><span class="row-main"><span class="row-title">${esc(y.name)}</span><span class="row-sub wrap">${size(y.gb)} · from ${esc(y.where)} · deleted ${esc(y.when)} by Inês · kept until ${esc(y.until)}</span></span><button class="btn btn-sm" data-act="az-restore" data-i="${i}">Restore</button></div>`).join('')}</div>
        <div class="btn-row" style="margin-top:16px"><button class="btn btn-danger" data-act="az-free">${I('trash')}Free the space now, ${size(total)}</button></div>` : `<div class="group"><div class="empty">${I('trash')}<p class="empty-title">Nothing recently deleted</p><p class="empty-text">What you delete here waits 7 days before its space comes back.</p></div></div>`}`;
    }
    const selGb = sel.reduce((s, y) => s + y.gb, 0);
    return `<div class="page-head"><div><h1>Disk analyzer</h1><p class="page-sub">${esc(x.n.name)} · ${esc(x.d.name)} · ${size(x.d.id === 'an-media' ? Math.round(x.d.sizeGB * (A.mediaPaused() ? 0.96 : 0.91)) : x.d.usedGB)} of ${size(x.d.sizeGB)} used. Deletes only in storage locations and HomersLab’s leftovers.</p></div><div class="page-actions">${pill()}<button class="btn" data-act="az-lock">${I('lock')}Lock</button></div></div>
      <div class="az-tools"><label class="sr-only" for="az-disk">Disk</label><select id="az-disk" class="input az-disk" data-change="az-disk">${disks().map((y) => `<option value="${y.d.id}" ${y.d.id === x.d.id ? 'selected' : ''}>${esc(y.n.name)}: ${esc(y.d.name)}</option>`).join('')}</select>
        <label class="sr-only" for="az-view-sel">View</label><select id="az-view-sel" class="input az-view-sel" data-change="az-view">${views.map(([k, l]) => `<option value="${k}" ${view === k ? 'selected' : ''}>${l.replace(/<[^>]+>/g, '').replace(/\s+\d+$/, '')}</option>`).join('')}</select>
        <div class="segmented az-views" style="--n:6" role="tablist" aria-label="Views" data-seg="az-view"><span class="segmented-thumb" aria-hidden="true"></span>${views.map(([k, l]) => `<button type="button" role="tab" aria-selected="${view === k}" data-act="az-go" data-to="${esc(`#/analyzer?disk=${x.d.id}&view=${k}`)}">${l}</button>`).join('')}</div></div>
      ${body}
      ${sel.length ? `<div class="az-dock"><p class="a-dock-line">${sel.length} chosen · ${size(selGb)}</p><div class="btn-row"><button class="btn" data-act="az-clear">Clear</button><button class="btn btn-danger" data-act="az-delete">${I('trash')}Delete with your passkey</button></div></div>` : ''}`;
  }, { after: () => { if (open()) { tick(); requestAnimationFrame(() => HL.treemapFit(document.querySelector('#main'))); } } });

  act('az-open', async () => {
    const ok = await stepUp({ plan: { id: UI.planId({ k: 'analyzer', t: Math.floor(Date.now() / 60000) }), summary: 'open the disk analyzer for 10 minutes' }, title: 'Open the disk analyzer', action: 'Open it', sub: 'It shows file names across the household’s data, so HomersLab asks for your passkey.', what: [{ icon: 'eye', text: 'See what fills every disk, down to file names, for 10 minutes' }] });
    if (!ok) return; AZ.until = Date.now() + SESSION; log('analyzer opened for 10 minutes'); HL.run(render, { type: 'fade' });
  });
  act('az-lock', () => { AZ.until = 0; AZ.sel = {}; HL.run(render, { type: 'fade' }); });
  act('az-go', (t) => go(t.dataset.to));
  act('az-drill', (t) => go(t.dataset.to));
  act('az-disk', (t) => go(`#/analyzer?disk=${t.value}`));
  act('az-view', (t) => go(`#/analyzer?disk=${new URLSearchParams(location.hash.split('?')[1] || '').get('disk') || 'an-media'}&view=${t.value}`));
  act('az-cleanup', (t) => toast(`Prototype: “${t.dataset.what}” would run inside the app, the way it does it itself.`, 'info'));
  const findItem = (key) => { const disk = key.split(':')[0]; return walk(D.analyzer[disk], disk).all.find((y) => y.key === key); };
  act('az-tile', (t) => {
    const y = findItem(t.dataset.key); if (!y) return;
    if (!y.canDelete) return toast(y.why || 'This one can’t be deleted here.', 'info');
    if (AZ.sel[y.key]) delete AZ.sel[y.key]; else AZ.sel[y.key] = { key: y.key, disk: y.key.split(':')[0], name: y.name, gb: y.gb, where: y.path.split('/').slice(0, -1).join(' › ') };
    render();
  });
  act('az-sel', (t) => { const y = findItem(t.dataset.key); if (t.checked) AZ.sel[y.key] = { key: y.key, disk: y.key.split(':')[0], name: y.name, gb: y.gb, where: y.path.split('/').slice(0, -1).join(' › ') }; else delete AZ.sel[y.key]; render(); });
  act('az-clear', () => { AZ.sel = {}; render(); });
  async function del(items) {
    const gb = items.reduce((s, y) => s + y.gb, 0), pid = UI.planId({ k: 'az-del', items: items.map((y) => y.key) });
    const ok = await stepUp({ plan: { id: pid, summary: `delete ${items.length} ${items.length === 1 ? 'item' : 'items'}, ${size(gb)}` }, title: `Delete ${items.length === 1 ? esc(items[0].name) : `${items.length} items`}`, action: 'Delete them', sub: 'HomersLab asks for your passkey for every delete, bound to exactly these items.',
      what: [...items.map((y) => ({ icon: 'trash', text: `${esc(y.name)}<small>${size(y.gb)} · ${esc(y.where)}</small>` })), { icon: 'clock', text: `Kept in Recently deleted for 7 days<small>${size(gb)} comes back after that, or when you free it</small>` }] });
    if (!ok) return false;
    const until = UI.dayMonth(new Date(now().getTime() + 7 * 86400000));
    for (const y of items) { AZ.gone[y.key] = true; AZ.deleted.unshift({ ...y, when: `today ${hhmm(now())}`, until }); delete AZ.sel[y.key]; }
    S.approvals.push(ok); log(`analyzer: deleted ${items.length} items (${pid})`); toast(`${items.length === 1 ? items[0].name : `${items.length} items`} ${items.length === 1 ? 'is' : 'are'} in Recently deleted until ${until}.`, 'trash');
    return true;
  }
  act('az-delete', async () => { if (await del(Object.values(AZ.sel))) A.renderList(); });
  act('az-del-one', async (t) => { const y = findItem(t.dataset.key); if (await del([{ key: y.key, disk: y.key.split(':')[0], name: y.name, gb: y.gb, where: y.path.split('/').slice(0, -1).join(' › ') }])) A.renderList(); });
  act('az-reflink', async (t) => {
    const y = findItem(t.dataset.key);
    const v = await dialog({ title: 'Share them as reflinks', sub: 'Both files stay where they are, with their names. Their bytes are stored once; if either is changed later, only the change takes new space.', body: A.list([['copy', `${esc(y.name)} and its twin point at the same ${size(y.gb)}`], ['check', 'Checked byte for byte first'], ['info', 'Nothing is deleted, so no passkey']]), actions: [{ label: 'Cancel', value: 'no', kind: 'ghost' }, { label: `Free ${size(y.gb)}`, value: 'yes', kind: 'primary' }] });
    if (v !== 'yes') return; AZ.shared[y.key] = true; log(`analyzer: reflinked ${y.name}`); toast(`${size(y.gb)} freed: the two copies share their bytes now.`); render();
  });
  act('az-restore', async (t) => {
    const y = AZ.deleted[+t.dataset.i];
    const ok = await stepUp({ plan: { id: UI.planId({ k: 'az-restore', key: y.key }), summary: `restore ${y.name}` }, title: `Restore ${esc(y.name)}`, action: 'Restore it', sub: 'Restoring puts files back where people can see them, so HomersLab asks for your passkey.', what: [{ icon: 'rollback', text: `${esc(y.name)}<small>${size(y.gb)}, back to ${esc(y.where)}</small>` }] });
    if (!ok) return; delete AZ.gone[y.key]; AZ.deleted.splice(+t.dataset.i, 1); S.approvals.push(ok); log(`analyzer: restored ${y.name}`); toast(`${y.name} is back in ${y.where}.`, 'rollback'); A.renderList();
  });
  act('az-free', async () => {
    const gb = AZ.deleted.reduce((s, y) => s + y.gb, 0);
    const ok = await stepUp({ plan: { id: UI.planId({ k: 'az-free', items: AZ.deleted.map((y) => y.key) }), summary: `free ${size(gb)} for good` }, title: 'Free the space now', action: 'Free it', sub: 'After this, nothing in Recently deleted can be restored.', what: [{ icon: 'trash', text: `Drop the snapshot that keeps ${AZ.deleted.length} deleted ${AZ.deleted.length === 1 ? 'item' : 'items'}<small>${size(gb)} comes back now, for good</small>` }] });
    if (!ok) return; AZ.freed = (AZ.freed || 0) + gb; AZ.deleted = []; S.approvals.push(ok); toast(`${size(gb)} is free again.`); A.renderList();
  });
})();
