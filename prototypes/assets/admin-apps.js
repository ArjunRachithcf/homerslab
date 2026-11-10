// HomersLab Admin prototype: the installed apps and an app's page. PROTOTYPE, throwaway.
// Apps go by their product names (Immich, Paperless-ngx) with what they are for beside them, as on Home.
(() => {
  const { D, S, cat, inst, rem, node, host, mesh, and, audPeople, audWords, appIcon, avatar, tier, SIGNIN, ROLE, chev, weeks, weeksAxis, weeksLegend, reach, route, render, go, soon, act, log } = A;
  const { esc, toast, dialog, stepUp, hhmm, now, gb } = UI;

  const lastResult = (a) => a.restore[11];
  const tested = () => S.installed.filter((a) => lastResult(a) !== null);
  // Memos is built from its feedstock; a security release whose patch doesn't apply yet installs guarded at the edge
  Proto.option({ key: 'feedstock', label: 'Memos (a feedstock)', def: 'patched', values: [{ v: 'patched', name: 'Patched build' }, { v: 'guarded', name: 'Guarded fallback' }], note: 'Guarded: a security release went out before HomersLab’s sign-in patch was rebased.' });
  const guardMemos = () => { const a = inst('memos'); if (a && !a.guarded) { a.history.unshift({ date: '9 Nov', from: a.version, to: '0.26.1', result: 'updated' }); a.version = '0.26.1'; a.update = null; a.guarded = true; } };
  if (Proto.get('feedstock', 'patched') === 'guarded') guardMemos();
  Proto.onChange((k) => { if (k === 'feedstock') { if (Proto.get('feedstock', 'patched') === 'guarded') guardMemos(); render(); } });
  const guardNote = (d) => `<div class="notice notice-warn a-finding" role="status">${I('shield')}<span class="notice-body"><b>Security update installed; signed in at the edge until HomersLab’s patch is ready.</b>${esc(d.name)} 0.26.1 fixes a security bug, and HomersLab’s sign-in patch doesn’t apply to it yet, so it went out without it. The household account still signs people in, at the edge; ${esc(UI.poss(d.name))} own login may appear as well. The patch is being rebased; the next build drops the guard by itself.</span></div>`;
  const statusOf = (a) => {
    if (a.health.state !== 'working') return { pill: 'pill-red', text: 'Not working' };
    if (a.id === 'jellyfin' && A.inWindow?.()) return { pill: 'pill-orange', text: 'Updating until 03:00' };
    if (a.id === 'jellyfin' && A.mediaPaused?.()) return { pill: 'pill-orange', text: 'Paused: disk nearly full' };
    if (a.guarded) return { pill: 'pill-orange', text: 'Guarded at the edge' };
    if (lastResult(a) === 'fail') return { pill: 'pill-red', text: 'Restore test failed' };
    if (S.jobs[a.id]?.kind === 'update') return { pill: 'pill-orange', text: 'Updating' };
    if (a.update) return { pill: '', text: 'Update ready' };
    if (lastResult(a) === null) return { pill: '', text: 'First test on Sunday' };
    return null;
  };
  const exposureWords = (a) => (a.exposure === 'public' ? 'Public' : 'Mesh only');
  const sinceBackup = (at) => { const [h, m] = at.split(':').map(Number), n = now(); const mins = Math.max(0, n.getHours() * 60 + n.getMinutes() - (h * 60 + m)); return mins < 1 ? 'Just now' : mins < 60 ? `${mins} min ago` : `${Math.floor(mins / 60)} h ${mins % 60} min ago`; };
  const list = (items) => `<ul class="group stepup-list">${items.map(([icon, text, small]) => `<li>${I(icon)}<span>${text}${small ? `<small>${small}</small>` : ''}</span></li>`).join('')}</ul>`;
  const ring = (share, bad) => `<svg class="ring" viewBox="0 0 64 64" aria-hidden="true" style="--ring:var(${bad ? '--red' : '--green'})"><circle class="ring-track" cx="32" cy="32" r="24"/><circle class="ring-arc" cx="32" cy="32" r="24" stroke-dasharray="150.8" stroke-dashoffset="${(150.8 * (1 - share)).toFixed(1)}"/></svg>`;
  A.list = list;

  // ---------- the installed apps (the landing) ----------
  route(/^#\/apps$/, (m, q) => {
    const tab = q.get('tab') || 'installed';
    const apps = S.installed, working = apps.filter((a) => a.health.state === 'working').length;
    const t = tested(), passed = t.filter((a) => lastResult(a) === 'pass').length;
    const failed = t.filter((a) => lastResult(a) === 'fail'), updates = apps.filter((a) => a.update);
    const last = apps.map((a) => a.lastBackup?.at).filter(Boolean).sort().pop() || '14:00';
    const head = `
      <div class="page-head"><div><h1>Apps</h1>
        <p class="page-sub">${apps.length} apps on ${S.nodes.length} nodes. ${failed.length === 1 ? `${esc(UI.poss(cat(failed[0].id).name))} restore test failed on Sunday; the rest are working and backed up.` : failed.length ? `${esc(and(failed.map((a) => cat(a.id).name)))} failed their restore tests on Sunday.` : 'All working, backed up and restore-tested.'}</p></div>
        <div class="page-actions"><a class="btn btn-primary" href="#/store">${I('store')}Store</a></div></div>
      ${STORE.open().length ? (() => { const w = STORE.open(); return `<div class="notice notice-info a-finding" role="status">${I('ask')}<span class="notice-body"><b>${w.length === 1 ? 'New request' : `${w.length} new requests`}: ${esc(and(w.map((r) => `${STORE.who(r)} asked for ${r.app ? cat(r.app).name : 'something new'}`)))}.</b>Approving one opens its install plan; nothing installs until you press Install.</span><span class="notice-actions"><a class="btn btn-sm" href="#/store/requests">See requests</a></span></div>`; })() : ''}
      ${A.windowNotice ? A.windowNotice() : ''}${A.diskFailed?.() ? A.diskAlerts().split('</div>')[0] + '</div>' : ''}
      ${failed.length ? `<div class="notice notice-error a-finding" role="status">${I('alert')}<span class="notice-body"><b>${esc(cat(failed[0].id).name)}: Sunday’s restore test failed.</b>${esc(failed[0].restoreFail?.why || '')}</span><span class="notice-actions"><a class="btn btn-sm" href="#/apps/${failed[0].id}">See ${esc(cat(failed[0].id).name)}</a></span></div>` : ''}
      <div class="widgets a-widgets">
        <div class="widget ${working < apps.length ? 'is-bad' : ''}"><span class="widget-label">Working now</span><div class="widget-row">${ring(working / apps.length, working < apps.length)}<span class="widget-value">${working}<small>of ${apps.length}</small></span></div><span class="widget-caption">Health checked every minute</span></div>
        <a class="widget" href="#/backups"><span class="widget-label">Last backup${I('chevronRight', 'icon icon-sm chev')}</span><span class="widget-value">${sinceBackup(last)}</span><span class="widget-caption">Every hour, to 3 places · ${apps.length} of ${apps.length} at ${last}</span></a>
        <a class="widget ${failed.length ? 'is-bad' : ''}" href="#/backups"><span class="widget-label">Restore tests${I('chevronRight', 'icon icon-sm chev')}</span><span class="widget-value">${passed}<small>of ${t.length} passed</small></span><span class="widget-caption">${failed.length ? `${esc(and(failed.map((a) => cat(a.id).name)))} failed on Sunday` : 'All passed on Sunday'}</span></a>
        <a class="widget" href="#/store/updates"><span class="widget-label">Updates ready${I('chevronRight', 'icon icon-sm chev')}</span><span class="widget-value">${updates.length}</span><span class="widget-caption">${updates.length ? esc(and(updates.map((a) => cat(a.id).name))) : 'Everything is current'}</span></a>
      </div>
      <div class="a-tabbar-row"><div class="segmented" style="--n:2" role="tablist" aria-label="Apps" data-seg="apps-tab"><span class="segmented-thumb" aria-hidden="true"></span>
        ${[['installed', 'Installed', apps.length], ['removed', 'Removed', S.removed.length]].map(([k, l, n]) => `<button type="button" role="tab" aria-selected="${tab === k}" data-act="apps-tab" data-tab="${k}">${l} <span class="count">${n}</span></button>`).join('')}
      </div></div>`;
    if (tab === 'removed') return head + removedTable();
    return head + `
      <div class="group"><table class="rtable has-lead"><thead><tr><th>App</th><th>Audience</th><th>Exposure</th><th>Node</th><th class="n">Backed up</th><th class="n">State</th><th class="chev-cell"><span class="sr-only">Open</span></th></tr></thead><tbody>
      ${apps.map((a) => { const d = cat(a.id), st = statusOf(a); return `<tr class="link-row" data-act="row" data-href="#/apps/${a.id}" data-app-id="${a.id}">
        <td><div class="ident">${appIcon(d)}<div class="names"><a href="#/apps/${a.id}"><b data-name>${esc(d.name)}</b></a><span>${esc(d.purpose)} · ${esc(a.version)}</span></div></div></td>
        <td class="hide-sm">${esc(audWords(a.audience))}</td>
        <td class="hide-sm">${a.exposure === 'public' ? `<span class="cell-state" style="color:var(--text)">${I('globe', 'icon icon-sm')}Public</span>` : 'Mesh only'}</td>
        <td class="hide-sm">${esc(node(a.node).name)}</td>
        <td class="hide-sm n">${a.lastBackup ? esc(a.lastBackup.at) : '<span class="muted-cell">—</span>'}</td>
        <td class="line2">${esc(audWords(a.audience))} · ${exposureWords(a)} · ${esc(node(a.node).name)} · backed up ${a.lastBackup ? esc(a.lastBackup.at) : 'at the hour'}</td>
        <td class="end">${st ? `<span class="pill ${st.pill}">${esc(st.text)}</span>` : `<span class="cell-state"><span class="dot dot-green"></span>Working</span>`}</td>
        <td class="chev-cell">${chev()}</td></tr>`; }).join('')}
      </tbody></table></div>
      <p class="group-foot">Every app is backed up every hour and restore-tested every Sunday.</p>`;
  }, { redirect: (m, q) => (q.get('tab') === 'updates' ? '#/store/updates' : null) });
  act('apps-tab', (t) => { const k = t.dataset.tab; go(k === 'installed' ? '#/apps' : `#/apps?tab=${k}`); });

  function removedTable() {
    if (!S.removed.length) return `<div class="group"><div class="empty">${I('remove')}<p class="empty-title">No removed apps</p><p class="empty-text">Removing an app keeps its data; it would wait here until you install it again or delete its data.</p></div></div>`;
    return `<div class="group"><table class="rtable has-lead"><thead><tr><th>App</th><th>Removed</th><th class="n">Data kept</th><th class="chev-cell"><span class="sr-only">Open</span></th></tr></thead><tbody>
      ${S.removed.map((a) => { const d = cat(a.id); return `<tr class="link-row" data-act="row" data-href="#/apps/${a.id}" data-app-id="${a.id}">
        <td><div class="ident">${appIcon(d)}<div class="names"><a href="#/apps/${a.id}"><b data-name>${esc(d.name)}</b></a><span>${esc(d.purpose)} · ${esc(a.version)} · was on ${esc(node(a.node).name)}</span></div></div></td>
        <td class="line2" style="display:table-cell">${esc(a.removedOn)} by ${esc(a.by)}; its backups are kept too</td><td class="end n">${gb(a.dataGB)}</td><td class="chev-cell">${chev()}</td></tr>`; }).join('')}
      </tbody></table></div>`;
  }

  // ---------- an app's page ----------
  route(/^#\/apps\/([\w-]+)$/, (m) => {
    const id = m[1];
    if (rem(id)) return removedPage(rem(id));
    const a = inst(id); if (!a) return `<div class="page-head"><div><h1>Not installed</h1><p class="page-sub">This app isn’t in the household. <a class="link" href="#/store/app/${esc(id)}">Find it in the Store</a>.</p></div></div>`;
    const d = cat(id), n = node(a.node), people = audPeople(a.audience), job = S.jobs[id];
    const r = lastResult(a);
    const lastPass = [...a.restore].reverse().findIndex((x) => x === 'pass');
    const lastPassDate = lastPass >= 0 ? UI.dayMonth(D.SUNDAYS[11 - lastPass]) : '—';
    return `
      <div class="page-head"><div class="a-ident" data-app-id="${id}">${appIcon(d, 'app-icon-xl')}<div><div class="a-h1-line"><h1>${esc(d.name)}</h1>${a.health.state === 'working' ? '<span class="pill pill-green"><span class="dot dot-green dot-sm"></span>Working</span>' : '<span class="pill pill-red"><span class="dot dot-red dot-sm"></span>Not working</span>'}</div>
        <p class="page-sub">${esc(d.purpose)} · ${esc(a.version)} on ${esc(n.name)} · <span class="host">${esc(host(id))}</span></p></div></div>
        <div class="page-actions"><button class="btn" data-act="open-app" data-host="${esc(host(id))}">${I('external')}Open</button><a class="btn" href="#/apps/${id}/move">${I('move')}Move its data</a>
          ${a.update && !job ? `<button class="btn btn-primary" data-act="update" data-id="${id}">${I('update')}Update to ${esc(a.update.to)}</button>` : ''}</div></div>
      ${r === 'fail' ? `<div class="notice notice-error a-finding" role="status">${I('alert')}<span class="notice-body"><b>Sunday’s restore test failed.</b>${esc(a.restoreFail.why)} ${esc(a.restoreFail.note)}</span><span class="notice-actions"><button class="btn btn-sm" data-act="retest" data-id="${id}" ${job ? 'disabled' : ''}>${job?.kind === 'retest' ? '<span class="spinner"></span>Testing…' : 'Run the test again'}</button></span></div>` : ''}
      ${a.guarded ? guardNote(d) : ''}
      ${a.id === 'jellyfin' && A.inWindow?.() ? `<div class="notice notice-info a-finding" role="status">${I('clock')}<span class="notice-body"><b>Updating until about 03:00, in the maintenance window.</b>To 10.11.3; Home says “Updating until 03:00”. Its own alerts wait for the morning digest.</span></div>` : ''}
      ${a.id === 'jellyfin' && A.mediaPaused?.() ? `<div class="notice notice-error a-finding" role="status">${I('pause')}<span class="notice-body"><b>Paused because the Media pool is nearly full (96%).</b>Its library scans and transcodes wait until there is room again; playing what is already there still works.</span><span class="notice-actions"><a class="btn btn-sm" href="#/analyzer?disk=an-media">Free some space</a></span></div>` : ''}
      ${a.rerun ? `<div class="notice notice-ok a-finding" role="status">${I('check')}<span class="notice-body">Run again today at ${esc(a.rerun)}: the restored copy started, passed its health check and signed in as a test person.</span></div>` : ''}
      <div class="a-facts">
        <div class="widget"><span class="widget-label">Health</span><span class="widget-value">${a.health.state === 'working' ? 'Working' : 'Not working'}</span><span class="widget-caption">Since ${esc(a.health.since)} · ${a.health.ms} ms</span></div>
        <div class="widget"><span class="widget-label">Audience</span><span class="widget-value">${people.length} ${people.length === 1 ? 'person' : 'people'}</span><span class="widget-caption">${esc(audWords(a.audience))}</span></div>
        <div class="widget"><span class="widget-label">Exposure</span><span class="widget-value">${exposureWords(a)}</span><span class="widget-caption">${a.exposure === 'public' ? 'From the internet too' : 'Over ' + esc(A.meshWords())}</span></div>
        <div class="widget"><span class="widget-label">Last backup</span><span class="widget-value">${a.lastBackup ? esc(a.lastBackup.at) : '—'}</span><span class="widget-caption">${a.lastBackup ? `+${esc(a.lastBackup.added)} · ${gb(a.dataGB)} kept` : 'At the top of the hour'}</span></div>
        <div class="widget"><span class="widget-label">Restore test</span><span class="widget-value">${r === 'pass' ? 'Passed' : r === 'fail' ? 'Failed' : 'Not yet'}</span><span class="widget-caption ${r === 'fail' ? 'is-bad' : ''}">${r === null ? 'First one on Sunday' : `Sunday 8 Nov${r === 'fail' ? ` · last passed ${lastPassDate}` : ''}`}</span></div>
      </div>
      ${secAudience(a, d)}
      ${secExposure(a, d, n)}
      ${secBackups(a, d, n)}
      ${secUpdates(a, d)}
      ${secHealth(a, d, n)}
      ${secBuild(a, d)}
      ${secDanger(a, d, n)}`;
  }, { back: (m) => (rem(m[1]) ? { href: '#/apps?tab=removed', label: 'Removed' } : { href: '#/apps', label: 'Apps' }) });

  // ---------- how an app is built: a HomersLab build from its feedstock (shown here, never in the Store) ----------
  function secBuild(a, d) {
    const f = a.feedstock; if (!f) return '';
    return sec('build', 'How it’s built', `${esc(d.name)} can’t reach Verified sign-in through its own settings, so HomersLab builds it from source with a small patch. Every patch is offered upstream, and dropped once merged.`,
      `<div class="group"><div class="row is-top row-wrap"><span class="row-lead">${I('box', 'icon icon-md')}</span><span class="row-main"><span class="row-title">Built by HomersLab from ${esc(f.upstream)} ${esc(a.guarded ? '0.26.1' : f.version)}, ${a.guarded ? 'without its sign-in change for now' : `with ${f.changes} ${f.changes === 1 ? 'change' : 'changes'} for sign-in`}</span><span class="row-sub wrap">Signed keyless with Sigstore by <span class="mono">${esc(f.signer)}</span>; the catalog pins the digest and this signer, and the node refuses anything else · SBOM: ${esc(f.sbom)}</span></span><button class="btn btn-sm" data-act="feedstock" data-id="${a.id}">The patches</button></div></div>`);
  }
  act('feedstock', (t) => {
    const a = inst(t.dataset.id), f = a.feedstock, d = cat(a.id);
    dialog({ title: `${esc(UI.poss(d.name))} feedstock`, sub: `<span class="host">${esc(f.repo)}</span>: the recipe, the patches and their tests, in HomersLab’s public feedstocks repository.`, wide: true,
      body: `<div class="group lead-sm">${f.patches.map((x) => `<div class="row is-top row-wrap"><span class="row-lead">${I('copy', 'icon icon-md')}</span><span class="row-main"><span class="row-title mono">${esc(x.file)}</span><span class="row-sub wrap">${esc(x.what)}</span><span class="row-sub wrap">${esc(x.upstream)}</span></span></div>`).join('')}</div>
        <div class="group">${[['Upstream', `${f.upstream}, ${a.guarded ? '0.26.1 (security release)' : f.version}`], ['This build', a.guarded ? '0.26.1, unpatched, guarded at the edge' : f.build], ['Next', a.guarded ? 'The patch is being rebased for 0.26.1; the guard goes when the patched build is out' : `${f.next.version}: ${f.next.why}`], ['Licence', 'The patch carries Memos’s own licence (MIT); the tooling is Apache-2.0']].map(([k, v]) => `<div class="row is-top row-wrap"><span class="row-main"><span class="row-label">${esc(k)}</span><span class="row-sub wrap">${esc(v)}</span></span></div>`).join('')}</div>`,
      actions: [{ label: 'Close', value: 'ok', kind: 'primary' }] });
  });

  const sec = (id, title, words, body) => `<section class="sec" aria-labelledby="h-${id}"><div class="sec-head"><h2 id="h-${id}">${title}</h2><p>${words}</p></div><div class="sec-body">${body}</div></section>`;

  function secHealth(a, d, n) {
    return sec('health', 'Health', 'Checked every minute: its web page answers and its database takes connections. After an update, a failed check returns it to its snapshot.',
      `<div class="group">
        <div class="row"><span class="row-main"><span class="row-label">Last check</span></span><span class="row-value">${esc(a.health.check)} · ${a.health.ms} ms</span></div>
        <div class="row"><span class="row-main"><span class="row-label">Working since</span></span><span class="row-value">${esc(a.health.since)}${a.history[0] && a.history[0].date === a.health.since ? `, after ${esc(a.history[0].to)}` : ''}</span></div>
        <div class="row"><span class="row-main"><span class="row-label">Memory now</span></span><span class="row-value num">${gb(a.memNowGB)} on ${esc(n.name)}</span></div>
        <div class="row"><span class="row-main"><span class="row-label">Sign-in</span></span><span class="row-value">${SIGNIN[d.signIn.method]}${d.signIn.second ? ', plus its own' : ''}</span></div>
        <div class="row"><span class="row-main"><span class="row-label">Tier</span></span>${tier(d.tier)}</div>
      </div>`);
  }

  // the audience: roles and named people; kids as a role only where the app has a kid view. No passkey.
  function audienceEditor(aud, d, key) {
    const P = S.people, ev = !!aud.everyone;
    const chip = (attrs, label, pressed, disabled, title) => `<button type="button" class="chip" ${attrs} aria-pressed="${pressed}" ${disabled ? 'disabled' : ''} ${title ? `title="${esc(title)}"` : ''}>${label}${I('check2', 'icon chip-check')}</button>`;
    const viaRole = (p) => ev || (aud.adults && p.role !== 'kid') || (aud.admins && p.role === 'admin') || (aud.kids && p.role === 'kid');
    const named = P.filter((p) => (aud.people || []).includes(p.id) && !viaRole(p));
    const kidsNoView = named.filter((p) => p.role === 'kid' && !d.kidView);
    const ppl = audPeople(aud);
    const role = (k, label) => chip(`data-act="aud-role" data-key="${key}" data-role="${k}"`, label, !!aud[k] || (k !== 'everyone' && ev), (k === 'kids' && !d.kidView) || (ev && k !== 'everyone'), k === 'kids' && !d.kidView ? `${d.name} has no kid view, so it can’t go to kids as a role` : '');
    return `<div class="chips" role="group" aria-label="Roles">${role('everyone', 'Everyone')}${role('admins', 'Admins')}${role('adults', 'Adults')}${role('kids', 'Kids')}</div>
      <div class="chips" role="group" aria-label="Named people">${P.map((p) => chip(`data-act="aud-person" data-key="${key}" data-person="${p.id}"`, `${avatar(p)}${esc(p.short)}`, A.inAud(aud, p), viaRole(p), viaRole(p) ? `Included as ${ev ? 'everyone' : ROLE[p.role].toLowerCase()}` : '')).join('')}</div>
      <p>${ppl.length ? `${esc(and(ppl.map((p) => p.short)))} ${ppl.length === 1 ? 'sees' : 'see'} ${esc(d.name)} on Home and can open it.` : 'Nobody will see it on Home.'}${d.kidView && ppl.some((p) => p.role === 'kid') ? ` Kids get its kid view: ${esc(d.kidView.toLowerCase())}.` : ''}</p>
      ${kidsNoView.length ? `<div class="notice notice-warn">${I('alert')}<span class="notice-body">${esc(and(kidsNoView.map((p) => p.short)))} will see ${esc(d.name)} as adults do: it has no kid view.</span></div>` : ''}
      ${!d.kidView && !kidsNoView.length ? `<p class="subhead">No kid view: kids see it only if you name them.</p>` : ''}`;
  }
  A.audienceEditor = audienceEditor;
  function applyAud(aud, role) {
    if (role === 'everyone') { if (aud.everyone) { delete aud.everyone; } else { Object.keys(aud).forEach((k) => delete aud[k]); aud.everyone = true; } return; }
    if (aud[role]) delete aud[role]; else aud[role] = true;
  }
  const dirty = {};
  const audTarget = (key) => (key.startsWith('plan:') ? A.draft(key.slice(5)).audience : inst(key).audience);
  const after = (key) => (key.startsWith('plan:') ? soon(() => A.replan(key.slice(5))) : (dirty[key] = true, soon()));
  act('aud-role', (t) => { const aud = audTarget(t.dataset.key); applyAud(aud, t.dataset.role); t.setAttribute('aria-pressed', String(t.getAttribute('aria-pressed') !== 'true')); after(t.dataset.key); });
  act('aud-person', (t) => { const aud = audTarget(t.dataset.key); aud.people = aud.people || []; const i = aud.people.indexOf(t.dataset.person); if (i >= 0) aud.people.splice(i, 1); else aud.people.push(t.dataset.person); if (!aud.people.length) delete aud.people; t.setAttribute('aria-pressed', String(t.getAttribute('aria-pressed') !== 'true')); after(t.dataset.key); });
  function secAudience(a, d) {
    return sec('aud', 'Audience', `Who sees ${esc(d.name)} on Home and may open it. A change takes effect at once and needs no passkey; nobody is told.`,
      `<div class="group"><div class="audience-box">${audienceEditor(a.audience, d, a.id)}</div>
        <div class="row row-sep"><span class="row-main"><span class="stamp">${a.audSaved ? `${I('check')}Saved at ${esc(a.audSaved)}` : dirty[a.id] ? 'Not saved yet' : ''}</span></span><button class="btn btn-sm ${dirty[a.id] ? 'btn-primary' : ''}" data-act="aud-save" data-id="${a.id}" ${dirty[a.id] ? '' : 'disabled'}>Save audience</button></div></div>`);
  }
  act('aud-save', (t) => { const a = inst(t.dataset.id); a.audSaved = hhmm(now()); delete dirty[a.id]; log(`audience of ${a.id}: ${audWords(a.audience)}`); toast(`${cat(a.id).name}: ${audWords(a.audience)} can open it now.`); render(); });

  // exposure: mesh only by default; public is a risky change (the passkey), and not every mesh has a public path
  function secExposure(a, d, n) {
    const m = mesh(), canPub = d.publicAllowed && !!m.pub;
    const why = !d.publicAllowed ? (d.publicWhy || 'Its definition allows mesh only.') : !m.pub ? 'Headscale has no public path: Tailscale’s Funnel isn’t part of it. To make an app public, add NetBird as a second mesh in Settings; Headscale stays for everything else.' : '';
    const pubName = m.pub === 'funnel' ? `${A.host(a.id).split('.')[0]}.${D.household.meshes[1].tailnet}` : host(a.id);
    return sec('exp', 'Exposure', `Mesh only is the default. Public means anyone on the internet reaches its sign-in page; your passkey approves it.${a.exposure === 'public' ? ` Public at <span class="host">https://${esc(pubName)}</span>.` : ''}`,
      `<div class="segmented" style="--n:2" role="radiogroup" aria-label="Exposure" data-seg="exposure-${a.id}"><span class="segmented-thumb" aria-hidden="true"></span><button type="button" role="radio" aria-checked="${a.exposure !== 'public'}" data-act="exposure" data-id="${a.id}" data-to="mesh">${I('mesh', 'icon icon-sm')}Mesh only</button><button type="button" role="radio" aria-checked="${a.exposure === 'public'}" data-act="exposure" data-id="${a.id}" data-to="public" ${canPub ? '' : 'disabled'}>${I('globe', 'icon icon-sm')}Public${canPub ? '' : ': not possible'}</button></div>
        ${why ? `<div class="notice">${I('info')}<span class="notice-body">${esc(why)}</span></div>` : m.pub === 'funnel' ? `<div class="notice">${I('info')}<span class="notice-body">With Tailscale, public apps go through Funnel and use Tailscale’s name, <span class="host">${esc(pubName)}</span>, not your own domain. TLS still ends on ${esc(n.name)}.</span></div>` : ''}
        ${reach({ name: d.name, nodeName: n.name, exposure: a.exposure, publicAllowed: d.publicAllowed, lanExtra: (a.extrasApproved || []).some((x) => x.id === 'hostnet') })}`);
  }
  act('exposure', async (t) => {
    const a = inst(t.dataset.id), d = cat(a.id), n = node(a.node), to = t.dataset.to; if (a.exposure === to) return;
    const m = mesh(), pubName = m.pub === 'funnel' ? `${host(a.id).split('.')[0]}.${D.household.meshes[1].tailnet}` : host(a.id);
    if (to === 'public') {
      const plan = { id: UI.planId({ k: 'public', a: a.id, m: m.pub }), summary: `make ${d.name} public` };
      const ok = await stepUp({ plan, title: `Make ${esc(d.name)} public`, action: 'Make it public', sub: 'Anything public is a risky change, so HomersLab asks for your passkey.', what: [
        { icon: 'globe', text: `<b>https://${esc(pubName)}</b> answers from the internet, ${m.pub === 'netbird' ? 'through NetBird’s public path' : 'through Tailscale Funnel'}` },
        { icon: 'lock', text: `Encrypted all the way: TLS ends on ${esc(n.name)}; ${m.pub === 'netbird' ? 'NetBird' : 'Tailscale'} can’t read the traffic` },
        { icon: 'user', text: `Strangers reach only the sign-in page: ${esc(d.name)} still needs the household account, with a passkey` },
        { icon: 'shield', text: 'Admin stays mesh only. No router port is opened.' }] });
      if (!ok) return;
      S.approvals.push(ok); a.exposure = 'public'; log(`${a.id} public, approved ${ok.planId}`); toast(`${d.name} is public at ${pubName}. Approved with your passkey at ${ok.at}.`, 'globe');
    } else {
      const v = await dialog({ title: `Make ${esc(d.name)} mesh only?`, sub: 'Taking an app off the internet is never risky, so it needs no passkey.', body: list([['x', 'Its public path closes at once; links people shared from outside stop working'], ['mesh', `Devices on ${esc(A.meshWords())} reach it as before`]]), actions: [{ label: 'Cancel', value: 'no', kind: 'ghost' }, { label: 'Make it mesh only', value: 'yes', kind: 'primary' }] });
      if (v !== 'yes') return; a.exposure = 'mesh'; log(`${a.id} mesh only`); toast(`${d.name} is mesh only now.`);
    }
    render();
  });

  function secBackups(a, d, n) {
    const r = a.restore;
    return sec('bk', 'Backups', `Every hour, deduplicated and encrypted, to Attic NAS, Backblaze B2 and Proton Drive. Its restore is tested every Sunday.`,
      `<div class="group lead-sm">${d.data.map((x) => `<div class="row"><span class="row-lead">${I(x.what.startsWith('Not ') ? 'minus' : 'check', 'icon icon-md')}</span><span class="row-main"><span class="row-label wrap">${esc(x.what)}</span>${x.detail ? `<span class="row-sub wrap">${esc(x.detail)}</span>` : ''}</span></div>`).join('')}
          <div class="row"><span class="row-lead">${I('lock', 'icon icon-md')}</span><span class="row-main"><span class="row-label wrap">Its secrets and database also go into the weekly archive</span><span class="row-sub wrap">Encrypted to your recovery key, which isn’t on any machine</span></span></div></div>
        <div class="group"><div class="row"><span class="row-main"><span class="row-title">Restore tests, last 12 Sundays</span><span class="row-sub">${r.filter((x) => x === 'pass').length} passed · ${a.restoreMin} min each</span></span></div>
          <div class="audience-box" style="padding-top:0">${weeks(r)}${weeksAxis()}${weeksLegend(r)}</div></div>
        <div class="btn-row"><a class="btn" href="#/backups/restore?app=${a.id}">${I('rollback')}Restore to an earlier point</a><button class="btn btn-plain" data-act="retest" data-id="${a.id}" ${S.jobs[a.id] ? 'disabled' : ''}>${S.jobs[a.id]?.kind === 'retest' ? 'Testing…' : 'Run the restore test now'}</button></div>`);
  }
  act('retest', (t) => {
    const a = inst(t.dataset.id); S.jobs[a.id] = { kind: 'retest' }; log(`restore test ${a.id} started`); render();
    setTimeout(() => { delete S.jobs[a.id]; a.restore[11] = 'pass'; a.rerun = hhmm(now()); log(`restore test ${a.id} passed`); toast(`${cat(a.id).name}: the restore test passed. The finding is cleared.`); render(); }, 2600);
  });

  function secUpdates(a, d) {
    const job = S.jobs[a.id];
    return sec('up', 'Updates', 'Only to versions the catalog has tested. A snapshot comes first; if the app comes back unhealthy, it returns to the snapshot by itself. No passkey.',
      `<div class="group">
        ${job?.kind === 'update' ? `<div class="row"><div class="row-main progress-row"><div class="progress-head"><b>Updating to ${esc(a.update.to)}</b><span>${esc(job.label)}</span></div><div class="progress" role="progressbar" aria-valuemin="0" aria-valuemax="4" aria-valuenow="${job.step}" aria-label="Update progress"><span style="--value:${(job.step / 4) * 100}%"></span></div></div></div>`
          : a.update ? `<div class="row"><span class="row-main"><span class="row-title">${esc(a.update.to)} is ready</span><span class="row-sub wrap">Tested by the catalog on ${esc(a.update.tested)}. ${esc(a.update.notes)}</span></span><button class="btn btn-sm" data-act="update" data-id="${a.id}">Update</button></div>`
          : `<div class="row"><span class="row-main"><span class="row-label">Version</span></span><span class="row-value">${esc(a.version)}, the latest tested</span></div>`}
        <label class="row"><span class="row-main"><span class="row-label">Update automatically</span><span class="row-sub wrap">${a.auto ? 'On: it updates at 03:00 when the catalog has a tested version.' : 'Off: you decide when.'}</span></span><input type="checkbox" class="switch" data-change="auto" data-id="${a.id}" ${a.auto ? 'checked' : ''} aria-label="Update ${esc(d.name)} automatically"></label>
      </div>
      ${a.history.length ? `<div><div class="group-head"><h3>History</h3></div><ul class="group history-list">${a.history.map((h) => `<li><span class="h-date">${esc(h.date)}</span><span><b>${h.result === 'rolledback' ? `${esc(h.from)} → ${esc(h.to)}, rolled back` : `${esc(h.from)} → ${esc(h.to)}`}</b>${h.result === 'rolledback' ? `<span>${esc(h.why)}</span>` : h.auto ? '<span>Automatic</span>' : ''}</span></li>`).join('')}</ul></div>` : ''}`);
  }
  act('auto', (t) => { const a = inst(t.dataset.id); a.auto = t.checked; log(`${a.id} automatic updates ${a.auto ? 'on' : 'off'}`); toast(`${cat(a.id).name}: automatic updates ${a.auto ? 'on' : 'off'}.`); soon(); });
  act('update', async (t) => {
    const a = inst(t.dataset.id), d = cat(a.id), n = node(a.node);
    const fs = a.feedstock && a.update.security && !a.feedstock.next.applies;
    const guardUpd = !fs ? '' : `<div class="notice notice-warn">${I('shield')}<span class="notice-body"><b>It installs guarded at the edge.</b>HomersLab’s sign-in patch doesn’t apply to ${esc(a.update.to)} yet (${esc(a.feedstock.next.why.toLowerCase())}). Security releases never wait: the household account signs people in at the edge, and ${esc(UI.poss(d.name))} own login may appear as well, until the patched build is out.</span></div>`;
    const v = await dialog({ title: `Update ${esc(d.name)} to ${esc(a.update.to)}`, sub: `Tested by the catalog on ${esc(a.update.tested)}. ${esc(a.update.notes)}`,
      body: `${guardUpd}${list([['copy', `A snapshot of ${esc(d.name)}: its data and database, on ${esc(n.name)}`, 'Kept for a rollback'], ['update', `${esc(a.version)} → ${esc(a.update.to)}, pinned by digest`], ['check', 'Its health check', `If it fails, ${esc(d.name)} returns to the snapshot by itself`]])}<p class="subhead">${esc(d.name)} is away for about two minutes. Updates aren’t risky, so no passkey.</p>`,
      actions: [{ label: 'Cancel', value: 'no', kind: 'ghost' }, { label: 'Update now', value: 'yes', kind: 'primary' }] });
    if (v !== 'yes') return;
    const labels = ['Taking the snapshot', `Updating to ${a.update.to}`, 'Checking its health', 'Done'];
    S.jobs[a.id] = { kind: 'update', step: 0, label: labels[0] }; log(`update ${a.id} started`);
    if (!location.hash.startsWith('#/apps/' + a.id)) go('#/apps/' + a.id); else render();
    let i = 0; const tick = () => { i++; if (i < 4) { S.jobs[a.id] = { kind: 'update', step: i, label: labels[i] }; render(); setTimeout(tick, 900); } else {
      if (a.feedstock && a.update.security && !a.feedstock.next.applies) a.guarded = true;
      a.history.unshift({ date: UI.dayMonth(now()), from: a.version, to: a.update.to, result: 'updated' }); a.version = a.update.to; a.update = null; a.health.since = UI.dayMonth(now()); delete S.jobs[a.id];
      log(`update ${a.id} done`); toast(`${d.name} is on ${a.version}. Its health check passed; the snapshot is kept for 7 days.`); render(); } };
    setTimeout(tick, 900);
  });

  function secDanger(a, d, n) {
    return sec('danger', 'Remove, delete data, eject', 'Removing keeps everything, so nothing is lost by accident. Deleting data is a separate step after Remove, and asks for your passkey; so does Eject.',
      `<div class="group">
        <div class="row is-top row-wrap"><span class="row-main"><span class="row-title">Remove</span><span class="row-sub wrap">Stops ${esc(d.name)} and takes away its sign-in, its name and its place on Home. Its data (${gb(a.dataGB)}) and backups stay, so installing it again brings it back as it was. No passkey.</span></span><button class="btn btn-sm" data-act="remove" data-id="${a.id}">Remove…</button></div>
        <div class="row is-top row-wrap"><span class="row-main"><span class="row-title">Delete data</span><span class="row-sub wrap">Destroys its data and its backups. You type its name and confirm with your passkey.</span><span class="btn-why" id="why-del-${a.id}">Remove ${esc(d.name)} first.</span></span><button class="btn btn-sm btn-destructive" disabled aria-describedby="why-del-${a.id}">${I('trash')}Delete data</button></div>
        <div class="row is-top row-wrap"><span class="row-main"><span class="row-title">Eject</span><span class="row-sub wrap">Writes ${esc(d.name)} out as a plain compose project with its data, settings and secrets, so it keeps running without HomersLab. Your passkey approves it.</span></span><button class="btn btn-sm" data-act="eject" data-id="${a.id}">${I('eject')}Eject…</button></div>
      </div>`);
  }
  act('remove', async (t) => {
    const a = inst(t.dataset.id), d = cat(a.id), n = node(a.node), ppl = audPeople(a.audience);
    const v = await dialog({ title: `Remove ${esc(d.name)}?`, sub: 'Removing takes nothing away for good, so it needs no passkey.',
      body: list([['x', `Stops ${esc(d.name)} on ${esc(n.name)}`], ['x', `Takes away its sign-in client, its name <span class="host">${esc(host(a.id))}</span>${a.exposure === 'public' ? ', its public path' : ''} and its place on Home for ${esc(and(ppl.map((p) => p.short)))}`], ['check', `Keeps its data (${gb(a.dataGB)}) and its backups`, 'Installing it again brings it back as it was']]),
      actions: [{ label: 'Cancel', value: 'no', kind: 'ghost' }, { label: `Remove ${esc(d.name)}`, value: 'yes', kind: 'destructive' }] });
    if (v !== 'yes') return;
    S.installed = S.installed.filter((x) => x !== a);
    S.removed.unshift({ id: a.id, node: a.node, removedOn: UI.dayMonth(now()), by: 'Inês', dataGB: a.dataGB, lastBackup: `${UI.dayMonth(now())}, ${a.lastBackup?.at || '—'}`, version: a.version });
    log(`removed ${a.id}, data kept`); toast(`${d.name} is removed. Its data and backups are kept.`); go('#/apps/' + a.id);
  });
  act('eject', async (t) => {
    const a = inst(t.dataset.id), d = cat(a.id), n = node(a.node), path = `/srv/ejected/${a.id}`;
    const plan = { id: UI.planId({ k: 'eject', a: a.id }), summary: `eject ${d.name} to ${path} on ${n.name}` };
    const v = await dialog({ title: `Eject ${esc(d.name)}`, sub: 'Eject is the way out: it keeps running, without HomersLab.', wide: true,
      body: `${list([['folder', `Writes <span class="host">${esc(path)}</span> on ${esc(n.name)}`, 'compose.yaml, its data, its settings, and its secrets in files only root can read'], ['check', `Its sign-in keeps working while the household keeps ${esc(D.household.idp.name)}`], ['x', 'HomersLab stops looking after it', 'No more updates, backups, restore tests or health checks; it leaves Home'], ['info', `Nobody can reach it until an edge is in front of it again`, `HomersLab’s edge stops serving ${esc(host(a.id))}; a ready edge snippet is written beside it`]])}
        <div class="notice">${I('key')}<span class="notice-body">Writing secrets out of their sealed store is a risky change: your passkey approves it.</span></div>`,
      actions: [{ label: 'Cancel', value: 'no', kind: 'ghost' }, { label: 'Continue', value: 'yes', kind: 'primary' }] });
    if (v !== 'yes') return;
    const ok = await stepUp({ plan, title: `Eject ${esc(d.name)}`, action: 'Eject it', sub: 'Eject is a risky change: secrets leave their sealed store.', what: [{ icon: 'eject', text: `Write ${esc(d.name)} with its secrets to <span class="host">${esc(path)}</span> on ${esc(n.name)}` }, { icon: 'x', text: 'End HomersLab’s updates, backups and restore tests for it' }] });
    if (!ok) return;
    S.approvals.push(ok); S.installed = S.installed.filter((x) => x !== a); S.ejected.push(a.id); log(`ejected ${a.id} (${ok.planId})`);
    await dialog({ title: `${esc(d.name)} is ejected`, sub: `Its files are on ${esc(n.name)}. From now on it is yours to look after.`,
      body: `<div class="cmd"><code>cd ${esc(path)} && docker compose up -d</code><button class="btn btn-icon btn-sm" data-act="copy" aria-label="Copy the command">${I('copy')}</button></div><p class="subhead">Put an edge in front of it with the snippet beside it. Restoring from HomersLab’s older backups still works by hand: the recovery kit’s one-page how-to covers it.</p>`,
      actions: [{ label: 'Back to Apps', value: 'ok', kind: 'primary' }] });
    go('#/apps');
  });

  // ---------- a removed app: its data waits; Delete data is the separate, typed, passkey-confirmed step ----------
  function removedPage(r) {
    const d = cat(r.id), n = node(r.node);
    return `
      <div class="page-head"><div class="a-ident" data-app-id="${r.id}">${appIcon(d, 'app-icon-xl')}<div><div class="a-h1-line"><h1>${esc(d.name)}</h1><span class="pill">Removed, data kept</span></div>
        <p class="page-sub">${esc(d.purpose)} · ${esc(r.version)} · was on ${esc(n.name)} · removed ${esc(r.removedOn)} by ${esc(r.by)}</p></div></div>
        <div class="page-actions"><a class="btn btn-primary" href="#/store/app/${r.id}/get">Get it again</a></div></div>
      ${sec('kept', 'What is kept', `Nothing of ${esc(d.name)} runs, and nobody sees it. Installing it again brings it back as it was.`,
        `<div class="group"><div class="row"><span class="row-main"><span class="row-label">Its data</span></span><span class="row-value num">${gb(r.dataGB)} on ${esc(n.name)}</span></div><div class="row"><span class="row-main"><span class="row-label">Its last backup</span></span><span class="row-value">${esc(r.lastBackup)}</span></div><div class="row"><span class="row-main"><span class="row-label">Restore tests</span></span><span class="row-value">Stopped when it was removed</span></div></div>`)}
      ${sec('kb', 'Its backups', 'Nothing new is backed up while it is removed. Its last backup stays for as long as it is removed; older ones age out as usual.',
        `<div class="group"><div class="row"><span class="row-main"><span class="row-label">Newest</span></span><span class="row-value">${esc(r.lastBackup)}</span></div><div class="row"><span class="row-main"><span class="row-label">Oldest</span></span><span class="row-value">Sunday 6 July</span></div><div class="row"><span class="row-main"><span class="row-label">Kept</span></span><span class="row-value num">74 backups of ${gb(r.dataGB)}</span></div><div class="row"><span class="row-main"><span class="row-label">Where</span></span><span class="row-value">Attic NAS, Backblaze B2, Proton Drive</span></div><div class="row"><span class="row-main"><span class="row-label wrap">Restorable by hand with plain restic and the recovery kit, even without HomersLab</span></span></div></div>`)}
      ${sec('del', 'Delete data', 'The only step that loses anything. You type its name and confirm with your passkey.',
        `<div class="group"><div class="row is-top row-wrap"><span class="row-main"><span class="row-title">Delete ${esc(UI.poss(d.name))} data for good</span><span class="row-sub wrap">Its data on ${esc(n.name)} and its backups. Backblaze B2 keeps its copies until they are 30 days old, as it does for everything.</span></span><button class="btn btn-sm btn-destructive" data-act="delete-data" data-id="${r.id}">${I('trash')}Delete data…</button></div></div>`)}`;
  }
  act('delete-data', async (t) => {
    const r = rem(t.dataset.id), d = cat(r.id), n = node(r.node);
    const plan = { id: UI.planId({ k: 'delete', a: r.id }), summary: `delete ${UI.poss(d.name)} data` };
    dialog.onOpen = (dlg) => { const inp = dlg.querySelector('#confirm-name'), btn = dlg.querySelector('[data-dv="yes"]'); if (!inp) return; inp.addEventListener('input', () => { btn.disabled = inp.value.trim() !== d.name; }); };
    const v = await dialog({ title: `Delete ${esc(UI.poss(d.name))} data for good`, sub: 'This can’t be undone.',
      body: `${list([['trash', `${gb(r.dataGB)} of data on ${esc(n.name)}`], ['trash', 'Its backups on Attic NAS and Proton Drive'], ['clock', 'Its backups on Backblaze B2, once they are 30 days old', 'They can’t be deleted sooner, by anyone'], ['clock', 'Its copies in older weekly archives, as those age out']])}
        <div class="field"><label class="field-label" for="confirm-name">Type <b>${esc(d.name)}</b> to confirm</label><input id="confirm-name" class="input" autocomplete="off" spellcheck="false" aria-describedby="confirm-hint"><span class="hint" id="confirm-hint">Then your passkey approves it.</span></div>`,
      actions: [{ label: 'Cancel', value: 'no', kind: 'ghost' }, { label: 'Delete data', value: 'yes', kind: 'danger', icon: 'trash', disabled: true }] });
    dialog.onOpen = null;
    if (v !== 'yes') return;
    const ok = await stepUp({ plan, title: `Delete ${esc(UI.poss(d.name))} data`, action: 'Delete it', sub: 'Deleting data is a risky change, so HomersLab asks for your passkey.', what: [{ icon: 'trash', text: `Destroy ${gb(r.dataGB)} of ${esc(UI.poss(d.name))} data on ${esc(n.name)}, and its backups` }] });
    if (!ok) return;
    S.approvals.push(ok); S.removed = S.removed.filter((x) => x !== r); log(`deleted data of ${r.id} (${ok.planId})`);
    toast(`${UI.poss(d.name)} data is deleted. Approved with your passkey at ${ok.at}.`, 'trash'); go('#/apps?tab=removed');
  });
})();
