// HomersLab Admin prototype: disks (ADR 0017). Each node's disks and the household's; preparing, adopting and erasing a
// disk; a failed disk in a mirror and its guided replacement; moving an app's data; low space; encrypting the system
// disk. PROTOTYPE, throwaway. Every write is a plan; the destructive ones need the passkey (erase: the serial too).
(() => {
  const { D, S, cat, inst, node, and, appIcon, route, render, go, act, log } = A;
  const { esc, toast, dialog, stepUp, hhmm, now } = UI;
  const size = HL.size;
  const sec = (id, title, words, body) => `<section class="sec" aria-labelledby="h-${id}"><div class="sec-head"><h2 id="h-${id}">${title}</h2><p>${words}</p></div><div class="sec-body">${body}</div></section>`;
  const R4 = (S.r4 = S.r4 || { jobs: {}, prepared: {}, erased: {}, adopted: {}, replaced: false, moved: {} });

  // ---------- prototype states: the disk in Bay 1, the media pool ----------
  Proto.option({ key: 'disk', label: 'Attic NAS, Bay 1', def: 'warning', values: [{ v: 'warning', name: 'SMART warnings' }, { v: 'failed', name: 'Failed' }], note: 'The mirror of App data: one disk reports reallocated sectors, or has failed.' });
  Proto.option({ key: 'media', label: 'Media pool', def: '91', values: [{ v: '91', name: '91% full' }, { v: '96', name: '96%: apps pause' }] });
  Proto.onChange((k) => { if (k === 'disk' || k === 'media') render(); });
  const failed = () => Proto.get('disk', 'warning') === 'failed' && !R4.replaced;
  const mediaPct = () => (Proto.get('media', '91') === '96' ? 96 : 91);

  // every volume, with its node; the facts that depend on the prototype's state
  const vols = () => S.nodes.flatMap((n) => n.disks.map((d) => ({ n, d })));
  const vol = (id) => vols().find((x) => x.d.id === id);
  const usedOf = (d) => (d.id === 'an-media' ? Math.round((d.sizeGB * mediaPct()) / 100) : d.usedGB);
  const members = (d) => d.members.map((m, i) => {
    if (d.id !== 'an-apps' || i !== 0) return m;
    if (R4.replaced) return { ...m, serial: 'S6PTNM0T611042A', smart: 'ok', hours: 1, realloc: 0, pending: 0, trend: null, fresh: true };
    return failed() ? { ...m, smart: 'failed', tempC: null } : m;
  });
  const fullIn = (d) => { if (!d.growth) return null; const u = usedOf(d); return { to95: Math.max(0, Math.round((d.sizeGB * 0.95 - u) / d.growth)), toFull: Math.round((d.sizeGB - u) / d.growth) }; };
  const ROLE = { system: 'The system', apps: 'App data', media: 'Media and storage locations', backups: 'Backups', empty: 'Not in use', foreign: 'Not HomersLab’s' };
  const encWords = (d) => (d.enc === 'luks-tpm' ? 'LUKS2, unlocked by the TPM at boot; its recovery key is in the kit' : d.encWhy ? `Not encrypted. ${d.encWhy}` : d.role === 'system' ? 'Not encrypted yet' : '—');
  const smartPill = (m) => (m.smart === 'failed' ? '<span class="pill pill-red">Failed</span>' : m.smart === 'warning' ? '<span class="pill pill-orange">SMART warnings</span>' : m.fresh ? '<span class="pill pill-green">New</span>' : '<span class="pill pill-green">Healthy</span>');
  const volState = (d) => {
    const pct = (usedOf(d) / d.sizeGB) * 100;
    if (d.state === 'empty' && !R4.prepared[d.id]) return { pill: '<span class="pill">Empty</span>', words: 'Empty: ready to prepare' };
    if (d.state === 'foreign' && !R4.erased[d.id]) return { pill: R4.adopted[d.id] ? '<span class="pill pill-green">Adopted</span>' : '<span class="pill">Not HomersLab’s</span>', words: R4.adopted[d.id] ? 'A storage location, read-only, untouched' : `${d.fs}, “${d.label}”: adopt it or erase it` };
    if (d.state === 'foreign') return { pill: '<span class="pill">Empty</span>', words: 'Erased: ready to prepare' };
    if (d.id === 'an-apps' && failed()) return { pill: '<span class="pill pill-red">At risk</span>', words: 'Running on Bay 2 alone' };
    if (d.id === 'an-apps' && !R4.replaced) return { pill: '<span class="pill pill-orange">Watch Bay 1</span>', words: 'Mirrored; Bay 1 reports SMART warnings' };
    if (pct >= 95) return { pill: '<span class="pill pill-red">Nearly full</span>', words: `${Math.round(pct)}% full: pausable apps paused` };
    if (pct >= 90) return { pill: '<span class="pill pill-orange">90% full</span>', words: `${Math.round(pct)}% full` };
    return { pill: '<span class="pill pill-green">Healthy</span>', words: d.layout === 'mirror' ? 'Mirrored, both disks healthy' : 'Healthy' };
  };
  // what fills a volume, from the analyzer's tree: apps, storage locations, HomersLab's leftovers, the system
  function segs(d) {
    const tree = D.analyzer[d.id];
    if (!tree) return d.role === 'backups' ? [{ label: 'Backups', gb: usedOf(d), tone: 2 }] : d.state === 'foreign' && !R4.erased[d.id] ? [{ label: `${d.label} (${d.fs})`, gb: d.usedGB, tone: 5 }] : usedOf(d) ? [{ label: 'The system', gb: usedOf(d), tone: 5 }] : [];
    const by = new Map(), add = (k, label, gb, tone) => { const x = by.get(k) || { label, gb: 0, tone }; x.gb += gb; by.set(k, x); };
    for (const k of tree.kids) {
      if (!k.owner || k.owner === 'system') add('system', 'System and snapshots', k.gb, 5);
      else if (k.owner === 'leftover') add('left', 'HomersLab’s leftovers', k.gb, 4);
      else if (k.owner.startsWith('st:')) add(k.owner, S.storage.find((s) => 'st:' + s.id === k.owner).name, k.gb, 2);
      else add(k.owner, cat(k.owner).name, k.gb, 1 + (by.size % 2 ? 2 : 0));
    }
    const out = [...by.values()];
    if (d.id === 'an-media') { const extra = usedOf(d) - d.usedGB; if (extra) out[0].gb += extra; }
    return out;
  }

  // ---------- the Nodes section's segments: Machines, Disks, Uptime ----------
  A.nodesSeg = (seg) => `<div class="a-tabbar-row" style="margin-bottom:24px"><div class="segmented" style="--n:3" role="tablist" aria-label="Nodes" data-seg="nodes-seg"><span class="segmented-thumb" aria-hidden="true"></span>${[['machines', 'Machines', '#/nodes'], ['disks', 'Disks', '#/nodes/disks'], ['uptime', 'Uptime', '#/nodes/uptime']].map(([k, l, h]) => `<button type="button" role="tab" aria-selected="${seg === k}" data-act="nodes-seg" data-to="${h}">${l}</button>`).join('')}</div></div>`;
  act('nodes-seg', (t) => go(t.dataset.to));
  // the alerts that disks raise, shown wherever they matter
  A.diskAlerts = () => {
    const out = [];
    if (failed()) out.push(`<div class="notice notice-error a-finding" role="alert">${I('alert')}<span class="notice-body"><b>A disk failed on Attic NAS: Bay 1.</b>App data keeps running on Bay 2 alone, so it is at risk until the disk is replaced. Nothing is lost; backups ran at 14:00.</span><span class="notice-actions"><a class="btn btn-sm" href="#/disks/an-apps/replace">Replace it</a></span></div>`);
    const m = vol('an-media').d, f = fullIn(m), pct = mediaPct();
    out.push(pct >= 95 ? `<div class="notice notice-error a-finding" role="status">${I('disk')}<span class="notice-body"><b>Media pool on Attic NAS is ${pct}% full.</b>Jellyfin’s library scans and transcodes are paused until there is room; backups and sign-in keep running.</span><span class="notice-actions"><a class="btn btn-sm" href="#/analyzer?disk=an-media">Free some space</a></span></div>`
      : `<div class="notice notice-warn a-finding" role="status">${I('disk')}<span class="notice-body"><b>Media pool on Attic NAS is ${pct}% full.</b>At this rate, about ${f.to95} days until 95%, when Jellyfin’s library scans and transcodes pause.</span><span class="notice-actions"><a class="btn btn-sm" href="#/disks/an-media">See the disk</a></span></div>`);
    return out.join('');
  };
  A.mediaPaused = () => mediaPct() >= 95;
  A.diskFailed = failed;

  // ---------- Disks, household-wide ----------
  route(/^#\/nodes\/disks$/, () => {
    const all = vols(), mirrors = all.filter((x) => x.d.layout === 'mirror').length;
    const total = all.filter((x) => !['empty', 'foreign'].includes(x.d.role)).reduce((s, x) => s + x.d.sizeGB, 0), used = all.reduce((s, x) => s + (['empty', 'foreign'].includes(x.d.role) ? 0 : usedOf(x.d)), 0);
    return `<div class="page-head"><div><h1>Disks</h1><p class="page-sub">${all.length} disks on ${S.nodes.length} nodes: ${size(used)} of ${size(total)} used. App data is mirrored where a node has two disks for it (${mirrors} mirror); media disks are single. A mirror is not a backup: backups still run every hour.</p></div>
        <div class="page-actions"><a class="btn" href="#/analyzer">${I('grid')}Disk analyzer</a></div></div>
      ${A.nodesSeg('disks')}${A.diskAlerts()}
      ${S.nodes.map((n) => `<section class="a-block" aria-labelledby="dn-${n.id}" style="margin-bottom:32px"><div class="a-block-head"><div><h2 id="dn-${n.id}">${esc(n.name)}</h2><p>${n.disks.length} ${n.disks.length === 1 ? 'disk' : 'disks'} · ${n.tpm ? 'TPM 2.0' : 'No TPM'}${n.bays ? ` · ${n.bays} bays` : ''}</p></div><a class="link" href="#/nodes/${n.id}">${esc(n.name)}’s page</a></div>
        <div class="group lead-md">${n.disks.map((d) => { const st = volState(d), u = usedOf(d); return `<a class="row row-link is-top" href="#/disks/${d.id}" data-row="${d.id}"><span class="row-lead">${I('disk', 'icon icon-md')}</span><span class="row-main"><span class="row-title">${esc(d.name)} · ${esc(ROLE[d.role])}</span><span class="row-sub wrap">${d.members.map((m) => esc(m.model)).join(' + ')}${d.layout === 'mirror' ? ', mirrored' : ''} · ${['empty', 'foreign'].includes(d.role) ? esc(st.words) : `${size(u)} of ${size(d.sizeGB)} · ${esc(st.words)}`}</span></span>${st.pill}${I('chevronRight', 'icon row-chev')}</a>`; }).join('')}</div></section>`).join('')}`;
  });

  // ---------- one disk ----------
  route(/^#\/disks\/([\w-]+)$/, (m) => {
    const x = vol(m[1]); if (!x) return '<div class="page-head"><div><h1>No such disk</h1></div></div>';
    const { n, d } = x, st = volState(d), u = usedOf(d), f = fullIn(d), ms = members(d);
    const unprepared = (d.state === 'empty' && !R4.prepared[d.id]) || (d.state === 'foreign' && (R4.erased[d.id] ? !R4.prepared[d.id] : false));
    const foreign = d.state === 'foreign' && !R4.erased[d.id] && !R4.adopted[d.id];
    const apps = S.installed.filter((a) => a.node === n.id && d.apps);
    const head = `<div class="page-head"><div><div class="a-h1-line"><h1>${esc(d.name)}</h1>${st.pill}</div><p class="page-sub">${esc(n.name)} · ${esc(ROLE[d.role])} · ${ms.map((y) => esc(y.model)).join(' + ')}</p></div>
      <div class="page-actions">${D.analyzer[d.id] ? `<a class="btn" href="#/analyzer?disk=${d.id}">${I('grid')}Open in the analyzer</a>` : ''}${unprepared ? `<a class="btn btn-primary" href="#/disks/${d.id}/prepare">Prepare this disk</a>` : ''}${foreign ? `<button class="btn" data-act="disk-adopt" data-id="${d.id}">Adopt it</button><a class="btn" href="#/disks/${d.id}/erase">Erase this disk</a>` : ''}</div></div>`;
    const alerts = d.id === 'an-apps' && failed() ? `<div class="notice notice-error a-finding" role="alert">${I('alert')}<span class="notice-body"><b>Bay 1 failed at 13:41: it stopped answering.</b>App data is at risk: it runs on Bay 2 alone, and the apps on it keep working. Replace the disk soon; a mirror is not a backup, and backups ran at 14:00.</span><span class="notice-actions"><a class="btn btn-sm" href="#/disks/an-apps/replace">Replace it</a></span></div>`
      : d.id === 'an-apps' && !R4.replaced ? `<div class="notice notice-warn a-finding" role="status">${I('alert')}<span class="notice-body"><b>Bay 1 reports SMART warnings.</b>24 reallocated sectors and 8 pending, up from 0 on 9 Oct. The mirror is fine, and Sunday’s scrub repaired 2 blocks from Bay 2. Order a replacement; when Bay 1 fails, App data keeps running on Bay 2.</span><span class="notice-actions"><a class="btn btn-sm" href="#/disks/an-apps/replace">Replace it now</a></span></div>`
      : d.id === 'an-media' ? A.diskAlerts().split('</div>').filter((s) => s.includes('Media pool')).map((s) => s + '</div>').join('') : '';
    if (unprepared || foreign) return `${head}${sec('dk', 'What is on it', foreign ? `It holds a ${esc(d.fs)} filesystem HomersLab didn’t make. Adopt it to use it as it is, or erase it to prepare it.` : 'Nothing: no partitions, no filesystem. Preparing it encrypts and formats it.',
      `${foreign ? `<div class="group">${d.contents.map((c) => `<div class="row"><span class="row-main"><span class="row-label">${esc(c.what)}</span></span><span class="row-value num">${esc(c.size)}</span></div>`).join('')}</div>` : ''}${memberRows(d, ms)}`)}`;
    return `${head}${alerts}
      ${sec('du', 'What fills it', f ? `Growing about ${d.growth >= 1 ? Math.round(d.growth) + ' GB' : size(d.growth)} a day: ${f.to95 > 730 ? 'years of room at this rate' : f.to95 > 0 ? `about ${f.to95} days until 95%, ${f.toFull} until full` : 'past 95%'}.` : 'What is on it, by app and storage location.',
        `<div class="card">${HL.usage(segs(d), d.sizeGB, { label: d.name })}</div><p class="subhead">${size(u)} of ${size(d.sizeGB)} used. Warnings at 80% and 90%; at 95%, apps that can pause (downloads, imports, transcodes) pause, and backups and sign-in keep running.</p>`)}
      ${sec('dh', d.layout === 'mirror' ? 'The mirror' : 'The disk', d.layout === 'mirror' ? 'Each disk holds a full copy, so one can fail without a restore. A mirror is not a backup.' : d.role === 'media' ? 'Single, for all the space: media can be fetched again, and the backups hold the rest.' : 'One disk. With a second disk of at least its size, it can become a mirror.',
        memberRows(d, ms) + (d.layout === 'mirror' && !failed() && !R4.replaced ? `<p class="subhead">Replacing a disk keeps the apps up: the mirror runs on the other disk while the new one is rebuilt.</p>` : ''))}
      ${sec('dx', 'Checks and encryption', 'Checksums are verified by a scrub in the maintenance window.',
        `<div class="card"><dl class="disk-facts"><div><dt>Encryption</dt><dd>${esc(encWords(d))}</dd></div><div><dt>Filesystem</dt><dd>${esc(d.fs)}${d.fs === 'btrfs' ? ', compressed (zstd), with snapshots' : ''}</dd></div><div><dt>Last scrub</dt><dd>${d.scrub ? `${esc(d.scrub.last)}: ${d.scrub.errors ? `${d.scrub.errors} errors` : 'no errors'}${d.scrub.repaired ? `, ${d.scrub.repaired} blocks repaired from the other disk` : ''}` : 'None: ext4 has no checksums'}</dd></div><div><dt>Layout</dt><dd>${d.layout === 'mirror' ? 'Mirror of 2 (btrfs raid1)' : 'Single'}</dd></div></dl></div>
        ${d.role === 'system' && !d.enc && n.tpm ? `<div class="group"><a class="row row-link" href="#/nodes/${n.id}/encrypt"><span class="row-main"><span class="row-title">Encrypt the system disk</span><span class="row-sub wrap">A guided migration in place, after a verified full copy and a rehearsal in a VM</span></span>${I('chevronRight', 'icon row-chev')}</a></div>` : ''}`)}
      ${apps.length ? sec('dm', 'Apps’ data here', 'Move an app’s data to another disk or node: a plan that copies, verifies and switches, and keeps the old copy for 7 days.',
        `<div class="group lead-sm">${apps.map((a) => `<div class="row"><span class="row-lead">${appIcon(cat(a.id), 'app-icon-sm')}</span><span class="row-main"><span class="row-title">${esc(cat(a.id).name)}</span><span class="row-sub">${size(a.dataGB)}${R4.moved[a.id] ? ` · moved to ${esc(R4.moved[a.id])}` : ''}</span></span><a class="btn btn-sm" href="#/apps/${a.id}/move">Move its data</a></div>`).join('')}</div>`) : ''}`;
  }, { back: (m) => { const x = vol(m[1]); return { href: '#/nodes/disks', label: 'Disks' }; } });
  function memberRows(d, ms) {
    return `<div class="disk-members">${ms.map((y) => `<div class="disk-member ${y.smart === 'failed' ? 'is-failed' : y.smart === 'warning' ? 'is-warning' : ''}">${I('disk', 'icon icon-md')}<span class="dm-text"><b>${esc(y.model)}</b><span>${esc(y.slot)} · serial <span class="mono">${esc(y.serial)}</span> · ${esc(y.kind)}${y.tempC ? ` · ${y.tempC} °C` : ''}${y.hours ? ` · ${y.hours.toLocaleString('en-GB')} hours` : ''}${y.wear ? ` · ${esc(y.wear)}` : ''}${y.realloc ? ` · ${y.realloc} reallocated, ${y.pending} pending (${esc(y.trend)})` : ''}${y.smart === 'failed' ? ' · stopped answering at 13:41' : ''}</span></span>${smartPill(y)}</div>`).join('')}</div>`;
  }

  // ---------- adopt: use what is on it, untouched ----------
  act('disk-adopt', async (t) => {
    const { n, d } = vol(t.dataset.id);
    const v = await dialog({ title: `Adopt ${esc(d.name)}`, sub: `HomersLab uses “${esc(d.label)}” as it is. Nothing on it is changed, moved or encrypted.`,
      body: `<div class="field"><label class="field-label" for="ad-name">Name it</label><input id="ad-name" class="input" value="${esc(d.label)}" autocomplete="off"></div>
        <div><p class="group-head">As</p><div class="group" role="radiogroup" aria-label="As"><label class="row row-choice"><input type="radio" name="ad-as" checked><span class="row-main"><span class="row-label">A storage location</span><span class="row-sub wrap">Apps may read it; read-only unless you say otherwise. It stays ${esc(d.fs)}, so it has no snapshots or checksums.</span></span><span class="row-trail">${I('check2', 'icon checkmark')}</span></label>
          <label class="row row-choice"><input type="radio" name="ad-as"><span class="row-main"><span class="row-label">Space for app data</span><span class="row-sub wrap">Only on btrfs or ext4: an ${esc(d.fs)} disk can’t hold app data</span></span><span class="row-trail">${I('check2', 'icon checkmark')}</span></label></div></div>`,
      actions: [{ label: 'Cancel', value: 'no', kind: 'ghost' }, { label: 'Adopt it', value: 'yes', kind: 'primary' }] });
    if (v !== 'yes') return;
    R4.adopted[d.id] = true; S.storage.push({ id: 'usb-2019', name: d.label, node: n.id, path: '/srv/usb-backup-2019', disk: d.id, kind: `${d.fs} disk, adopted untouched`, sizeGB: d.sizeGB, usedGB: d.usedGB, access: 'Read-only for apps', usedBy: [] });
    log(`adopted ${d.id}`); toast(`“${d.label}” is a storage location now, untouched.`); render();
  });

  // ---------- prepare a new disk: a plan, then the passkey ----------
  const prep = (S.prep = S.prep || {});
  route(/^#\/disks\/([\w-]+)\/prepare$/, (m) => {
    const job = R4.jobs['prep-' + m[1]];
    if (job) return progressPage(job);
    if (!vol(m[1])) return '<div class="page-head"><div><h1>No such disk</h1></div></div>';
    const { n, d } = vol(m[1]);
    const o = (prep[d.id] = prep[d.id] || { use: 'apps', layout: 'mirror' });
    const mate = n.disks.find((x) => x.apps);
    const mirrorApps = o.use === 'apps' && mate && mate.layout !== 'mirror';
    const usable = o.use === 'apps' && mirrorApps ? Math.min(mate.sizeGB, d.sizeGB) : d.sizeGB;
    const pid = UI.planId({ k: 'prep', d: d.id, o });
    const steps = prepSteps(d, n, o, mate);
    return `<div class="page-head"><div class="a-ident">${I('disk', 'icon disk-plan-glyph')}<div><h1>Prepare ${esc(d.members[0].model)}</h1><p class="page-sub">${esc(n.name)}, ${esc(d.members[0].slot)} · serial <span class="mono">${esc(d.members[0].serial)}</span> · empty. Nothing happens until you press Prepare.</p></div></div></div>
      <p class="plan-meta">Plan <span class="plan-id">${esc(pid)}</span> · made ${hhmm(now())}</p>
      <div class="split"><div class="plan-sections">
        <section><div class="plan-q"><h2>What it is for</h2></div><div class="group" role="radiogroup" aria-label="What it is for">
          <label class="row row-choice"><input type="radio" name="pr-use" value="apps" data-change="prep-use" data-id="${d.id}" ${o.use === 'apps' ? 'checked' : ''}><span class="row-main"><span class="row-title">App data, mirrored with ${esc(mate.name)}</span><span class="row-sub wrap">The default with two disks: either can fail without a restore. ${size(Math.min(mate.sizeGB, d.sizeGB))} usable${d.sizeGB > mate.sizeGB ? `; the other ${size(d.sizeGB - mate.sizeGB)} stays unused until a bigger partner joins` : ''}.</span></span><span class="row-trail">${I('check2', 'icon checkmark')}</span></label>
          <label class="row row-choice"><input type="radio" name="pr-use" value="media" data-change="prep-use" data-id="${d.id}" ${o.use === 'media' ? 'checked' : ''}><span class="row-main"><span class="row-title">Media, or a storage location</span><span class="row-sub wrap">Films, music, a share: things that can be fetched again or are backed up elsewhere</span></span><span class="row-trail">${I('check2', 'icon checkmark')}</span></label></div></section>
        ${o.use === 'media' ? `<section><div class="plan-q"><h2>Mirror or single</h2><span>For media, you choose</span></div><div class="group" role="radiogroup" aria-label="Mirror or single">
          <label class="row row-choice"><input type="radio" name="pr-lay" value="single" data-change="prep-layout" data-id="${d.id}" ${o.layout !== 'mirror' ? 'checked' : ''}><span class="row-main"><span class="row-title">Single: all ${size(d.sizeGB)}</span><span class="row-sub wrap">If the disk fails, what is on it is fetched again or restored</span></span><span class="row-trail">${I('check2', 'icon checkmark')}</span></label>
          <label class="row row-choice"><input type="radio" name="pr-lay" value="mirror" disabled><span class="row-main"><span class="row-title">Mirror: not possible yet</span><span class="row-sub wrap">It needs a second empty disk of at least ${size(d.sizeGB)} on ${esc(n.name)}</span></span><span class="row-trail">${I('check2', 'icon checkmark')}</span></label></div></section>` : ''}
        <section><div class="plan-q"><h2>What happens</h2><span>In this order</span></div><ol class="group stepup-list plan-steps">${steps.map((s) => `<li>${I(s.icon)}<span>${esc(s.t)}${s.s ? `<small>${esc(s.s)}</small>` : ''}</span></li>`).join('')}</ol></section>
        <div class="a-dock"><p class="a-dock-line">Your passkey approves preparing this disk, for plan ${esc(pid)} only</p><button class="btn btn-primary btn-lg btn-block" data-act="prep-go" data-id="${d.id}">${I('key')}Prepare with your passkey</button></div></div>
        <aside class="rail plan-rail"><div class="card plan-card stack" style="gap:12px"><h2 class="card-title">After it</h2><ul class="lines"><li>${I('disk')}<span>${size(usable)} usable, ${o.use === 'apps' ? 'mirrored' : 'single'}</span></li><li>${I('lock')}<span>Encrypted; Study PC’s TPM opens it at boot</span></li><li>${I('key')}<span>A new page for the recovery kit<small>Print it, or add it to the kit in your password manager</small></span></li></ul>
          <button class="btn btn-primary btn-block" data-act="prep-go" data-id="${d.id}">${I('key')}Prepare with your passkey</button><p class="stepup-bound">${I('lock')}<span>One passkey approves plan <span class="plan-id">${esc(pid)}</span>. Change anything and it becomes a new plan.</span></p></div></aside></div>`;
  }, { back: (m) => ({ href: `#/disks/${m[1]}`, label: vol(m[1])?.d.name || 'Disk' }) });
  function prepSteps(d, n, o, mate) {
    const out = [{ icon: 'check', t: 'Check that it is empty and not in use', s: `${d.members[0].model}, serial ${d.members[0].serial}` },
      { icon: 'disk', t: 'Partition it (one GPT partition)' },
      { icon: 'lock', t: 'Encrypt it with LUKS2', s: 'A new key, enrolled in the TPM (PCRs 0+7), so it opens at boot without a prompt' },
      { icon: 'key', t: 'Add its recovery key to the recovery kit', s: 'A recovery key works if the TPM ever refuses; it is shown once, in your browser' },
      { icon: 'disk', t: 'Format it as btrfs, compressed', s: 'Snapshots, reflinks, checksums and scrubs' }];
    if (o.use === 'apps') out.push({ icon: 'copy', t: `Join it to ${mate.name} as a mirror`, s: `btrfs device add, then a balance that copies ${size(mate.usedGB)} onto it; apps keep running` });
    else out.push({ icon: 'folder', t: 'Mount it and offer it as a storage location', s: 'You name it and choose who reads it' });
    out.push({ icon: 'check', t: 'A first scrub', s: 'In tonight’s maintenance window' });
    return out;
  }
  act('prep-use', (t) => { prep[t.dataset.id].use = t.value; render(); });
  act('prep-layout', (t) => { prep[t.dataset.id].layout = t.value; render(); });
  act('prep-go', async (t) => {
    const { n, d } = vol(t.dataset.id), o = prep[d.id], mate = n.disks.find((x) => x.apps);
    const pid = UI.planId({ k: 'prep', d: d.id, o });
    const ok = await stepUp({ plan: { id: pid, summary: `prepare ${d.members[0].model}` }, title: `Prepare ${esc(d.members[0].model)}`, action: 'Prepare it', sub: 'Preparing a disk writes over it, so HomersLab asks for your passkey.',
      what: [{ icon: 'disk', text: `Encrypt and format the disk in ${esc(d.members[0].slot)}<small>Serial ${esc(d.members[0].serial)}, ${size(d.sizeGB)}</small>` }, { icon: o.use === 'apps' ? 'copy' : 'folder', text: o.use === 'apps' ? `Make it a mirror of ${esc(mate.name)}` : 'Offer it as a storage location, single' }] });
    if (!ok) return; S.approvals.push(ok); log(`prepare ${d.id} approved (${pid})`);
    run('prep-' + d.id, prepSteps(d, n, o, mate).slice(0, -1).map((s) => ({ t: s.t, s: s.s })).concat([{ t: 'Ready', s: o.use === 'apps' ? `${mate.name} is mirrored now` : 'Name it and choose who reads it on the Nodes page' }]),
      { title: `Preparing ${d.members[0].model}`, done: `${d.members[0].model} is ready`, back: o.use === 'apps' ? `#/disks/${mate.id}` : `#/disks/${d.id}`, plan: pid, approved: ok }, () => {
        R4.prepared[d.id] = true;
        if (o.use === 'apps') { mate.layout = 'mirror'; mate.members.push({ ...d.members[0] }); n.disks.splice(n.disks.indexOf(d), 1); }
        else { d.role = 'media'; d.state = null; d.layout = 'single'; d.fs = 'btrfs'; d.enc = 'luks-tpm'; d.growth = 0; d.scrub = null; d.name = 'Storage disk'; }
        toast(o.use === 'apps' ? `${mate.name} on ${n.name} is a mirror now.` : 'The disk is ready as a storage location.');
      });
    go(`#/disks/${d.id}/prepare`);
  });

  // a job with steps and progress, re-rendered while its page is open
  function run(key, steps, meta, onDone) {
    const job = (R4.jobs[key] = { key, steps: steps.map((s) => ({ ...s })), step: 0, done: false, meta, t0: hhmm(now()), pct: 0 });
    const page = location.hash;
    const tick = () => { const cur = job.steps[job.step]; cur.at = hhmm(now()); job.step++; if (job.step >= job.steps.length) { job.done = true; onDone?.(); } if (location.hash.split('?')[0] === page.split('?')[0] || location.hash === meta.here) render(); if (!job.done) setTimeout(tick, UI.still() ? 300 : 650); };
    setTimeout(tick, 500);
    return job;
  }
  A.run4 = run;
  function progressPage(job, d, n, extra = '') {
    const total = job.steps.length, m = job.meta;
    return `<div class="page-head"><div class="a-ident">${I(m.icon || 'disk', 'icon disk-plan-glyph')}<div><h1>${job.done ? esc(m.done) : esc(m.title)}</h1><p class="page-sub">Plan <span class="plan-id">${esc(m.plan)}</span>${m.approved ? `, approved by ${esc(m.approved.by)} with her passkey at ${esc(m.approved.at)}` : ''} · started ${esc(job.t0)}</p></div></div>
      <div class="page-actions">${job.done ? `<a class="btn btn-primary" href="${m.back}">${esc(m.backLabel || 'See the disk')}${I('arrowRight')}</a>` : ''}</div></div>
      <div class="split"><section class="card stack a-progress" aria-label="Progress" style="gap:12px"><div class="progress-row"><div class="progress-head"><b>${job.done ? 'Done' : 'Working'}</b><span>${Math.min(job.step, total)} of ${total}</span></div><div class="progress" role="progressbar" aria-label="Progress" aria-valuemin="0" aria-valuemax="${total}" aria-valuenow="${job.step}"><span style="--value:${(job.step / total) * 100}%"></span></div></div>
        <ol class="steps" aria-live="polite">${job.steps.map((s, i) => { const st = i < job.step ? 'done' : i === job.step && !job.done ? 'now' : 'todo'; return `<li class="step is-${st}"><span class="step-mark">${st === 'done' ? I('check2') : st === 'now' ? '<span class="spinner"></span>' : ''}</span><span class="step-text"><span class="step-title">${esc(s.t)}</span>${s.s ? `<span class="step-sub">${esc(s.s)}</span>` : ''}<span class="sr-only">${st === 'done' ? 'Done' : st === 'now' ? 'In progress' : 'Not started'}</span></span><span class="step-time">${s.at || ''}</span></li>`; }).join('')}</ol></section>
        <aside class="rail">${extra || `<div class="card stack" style="gap:12px"><h2 class="card-title">While it runs</h2><p class="muted">You can leave this page; it carries on, and the disk’s page shows it when it is done. The apps keep running.</p></div>`}</aside></div>`;
  }
  A.progressPage4 = progressPage;

  // ---------- erase this disk: what is on it, then the passkey plus its serial, typed ----------
  route(/^#\/disks\/([\w-]+)\/erase$/, (m) => {
    const { n, d } = vol(m[1]); const sn = d.members[0].serial;
    if (R4.erased[d.id]) return `<div class="page-head"><div><h1>${esc(d.name)} is erased</h1><p class="page-sub">It is empty now. <a class="link" href="#/disks/${d.id}/prepare">Prepare it</a> to use it.</p></div></div>`;
    return `<div class="page-head"><div class="a-ident">${I('disk', 'icon disk-plan-glyph')}<div><h1>Erase ${esc(d.members[0].model)}</h1><p class="page-sub">${esc(n.name)}, ${esc(d.members[0].slot)} · serial <span class="mono">${esc(sn)}</span> · ${esc(d.fs)}, “${esc(d.label)}”</p></div></div></div>
      <div class="split"><div class="plan-sections">
        <section><div class="plan-q"><h2>What is on it</h2><span>${size(d.usedGB)} of ${size(d.sizeGB)}</span></div><div class="group">${d.contents.map((c) => `<div class="row"><span class="row-lead">${I('folder', 'icon icon-md')}</span><span class="row-main"><span class="row-label">${esc(c.what)}</span></span><span class="row-value num">${esc(c.size)}</span></div>`).join('')}</div>
          <p class="group-foot">None of it is in HomersLab’s backups: it was never HomersLab’s. Once erased, it is gone for good. To keep it, <button type="button" class="link" data-act="disk-adopt" data-id="${d.id}">adopt the disk instead</button>.</p></section>
        <section><div class="plan-q"><h2>Confirm with its serial</h2><span>Printed on its label</span></div><div class="group"><div class="audience-box"><div class="field"><label class="field-label" for="er-serial">Type the serial, <span class="mono">${esc(sn)}</span></label><input id="er-serial" class="input mono" autocomplete="off" spellcheck="false" data-serial="${esc(sn)}" aria-describedby="er-hint"><span class="hint" id="er-hint">So the disk you erase is the one in your hand.</span></div></div></div></section>
        <div class="plan-go"><button class="btn btn-danger btn-lg btn-block" data-act="erase-go" data-id="${d.id}" disabled>${I('trash')}Erase with your passkey</button></div></div>
        <aside class="rail"><div class="card stack" style="gap:12px"><h2 class="card-title">Never offered</h2><p class="muted">A system disk, or any disk with app data, backups or a storage location in use, can’t be erased here. Remove what uses it first.</p></div></aside></div>`;
  }, { back: (m) => ({ href: `#/disks/${m[1]}`, label: vol(m[1])?.d.name || 'Disk' }), after: () => { const i = document.querySelector('#er-serial'), b = document.querySelector('[data-act="erase-go"]'); i?.addEventListener('input', () => { b.disabled = i.value.trim().toUpperCase() !== i.dataset.serial; }); } });
  act('erase-go', async (t) => {
    const { d } = vol(t.dataset.id), m = d.members[0];
    const ok = await stepUp({ plan: { id: UI.planId({ k: 'erase', d: d.id }), summary: `erase ${m.model} ${m.serial}` }, title: `Erase ${esc(m.model)}`, action: 'Erase it', sub: 'Erasing a disk destroys what is on it, so HomersLab asks for your passkey.', what: [{ icon: 'trash', text: `Destroy ${size(d.usedGB)} on the disk with serial <span class="mono">${esc(m.serial)}</span><small>${esc(d.contents.map((c) => c.what).join(', '))}</small>` }] });
    if (!ok) return; S.approvals.push(ok); R4.erased[d.id] = true; log(`erased ${d.id}`); toast(`${m.model} is erased. Prepare it to use it.`, 'trash'); go(`#/disks/${d.id}`);
  });

  // ---------- a failed disk in a mirror: which to pull, the new one, the rebuild ----------
  const rep = (S.rep = S.rep || { step: 'pull' });
  route(/^#\/disks\/an-apps\/replace$/, () => {
    const { n, d } = vol('an-apps'), bad = d.members[0], good = d.members[1], job = R4.jobs.rebuild;
    if (job) return progressPage(job, d, n, `<div class="card stack" style="gap:12px"><h2 class="card-title">The apps stay up</h2><p class="muted">Jellyfin, Navidrome and Kavita keep running from Bay 2 while the new disk is filled. The rebuild runs at a gentle pace so nobody notices.</p></div>`);
    const bays = Array.from({ length: n.bays }, (_, i) => { const m = n.disks.flatMap((x) => x.members).find((y) => y.bay === i + 1); return `<div class="bay ${i === 0 && rep.step === 'pull' ? 'is-pull' : ''} ${i === 0 && rep.step !== 'pull' ? (rep.step === 'found' ? '' : 'is-empty') : ''}"><span class="bay-led" aria-hidden="true"></span><b>Bay ${i + 1}</b><span>${i === 0 ? (rep.step === 'pull' ? 'Pull this one' : rep.step === 'found' ? 'New disk' : 'Empty') : esc(m ? m.model.replace(' 1 TB', '').replace(' 22 TB', '').replace(' 4 TB', '') : '')}</span></div>`; }).join('');
    const body = rep.step === 'pull'
      ? `<section class="card stack" style="gap:16px"><h2 class="card-title">1. Pull the disk in Bay 1</h2><div class="bays" style="--n:${n.bays}" role="img" aria-label="Attic NAS’s four bays; Bay 1’s light blinks red">${bays}</div>
          <div class="group"><div class="row"><span class="row-main"><span class="row-label">Model</span></span><span class="row-value">${esc(bad.model)}</span></div><div class="row"><span class="row-main"><span class="row-label">Serial, on its label</span></span><span class="row-value mono">${esc(bad.serial)}</span></div><div class="row"><span class="row-main"><span class="row-label">Slot</span></span><span class="row-value">${esc(bad.slot)}: its light blinks red</span></div></div>
          <p class="muted">Attic NAS’s bays are hot-swap: leave it on. The apps keep running on Bay 2 (serial <span class="mono">${esc(good.serial)}</span>); don’t touch that one.</p>
          <div class="btn-row"><button class="btn btn-primary" data-act="rep-pulled">I pulled it</button></div></section>`
      : rep.step === 'insert'
        ? `<section class="card stack" style="gap:16px"><h2 class="card-title">2. Put the new disk in Bay 1</h2><div class="bays" style="--n:${n.bays}" role="img" aria-label="Attic NAS’s four bays; Bay 1 is empty">${bays}</div>
          <p class="muted">At least ${size(d.sizeGB)}. It may be bigger; the extra space stays unused until both disks are bigger. Anything on it is erased.</p><div class="btn-row"><button class="btn btn-primary" data-act="rep-check">Look for it</button></div></section>`
        : `<section class="card stack" style="gap:16px"><h2 class="card-title">3. Add it to the mirror</h2><div class="bays" style="--n:${n.bays}" role="img" aria-label="Attic NAS’s four bays; a new disk is in Bay 1">${bays}</div>
          <div class="group"><div class="row is-top"><span class="row-lead">${I('disk', 'icon icon-md')}</span><span class="row-main"><span class="row-title">Samsung 870 EVO 1 TB, in Bay 1</span><span class="row-sub">Serial <span class="mono">S6PTNM0T611042A</span> · empty · healthy</span></span><span class="pill pill-green">Found</span></div></div>
          <ol class="group stepup-list plan-steps">${[['lock', 'Encrypt it with LUKS2, enrolled in the TPM; its recovery key into the kit'], ['disk', 'Replace the failed disk in the mirror (btrfs replace)'], ['copy', `Rebuild: copy ${size(d.usedGB)} from Bay 2`], ['check', 'Scrub both disks']].map(([i, x]) => `<li>${I(i)}<span>${x}</span></li>`).join('')}</ol>
          <div class="btn-row"><button class="btn btn-primary" data-act="rep-go">${I('key')}Rebuild with your passkey</button></div></section>`;
    return `<div class="page-head"><div><div class="a-h1-line"><h1>Replace Bay 1’s disk</h1>${failed() ? '<span class="pill pill-red">At risk</span>' : '<span class="pill pill-orange">SMART warnings</span>'}</div><p class="page-sub">App data on ${esc(n.name)} is a mirror of two disks. ${failed() ? 'Bay 1 failed; Bay 2 holds everything and keeps the apps up.' : 'Bay 1 is wearing out; replacing it now keeps the mirror whole.'}</p></div></div>
      <div class="split">${body}<aside class="rail"><div class="card stack" style="gap:12px"><h2 class="card-title">Steps</h2><ol class="lines">${[['pull', 'Pull the disk in Bay 1'], ['insert', 'Put the new disk in'], ['found', 'Rebuild the mirror']].map(([k, l], i) => `<li>${I(['pull', 'insert', 'found'].indexOf(rep.step) > i ? 'check2' : 'disk')}<span>${l}</span></li>`).join('')}</ol><p class="subhead">A mirror is not a backup: backups ran at 14:00, to Attic NAS’s backups disk, Backblaze B2 and Proton Drive.</p></div></aside></div>`;
  }, { back: { href: '#/disks/an-apps', label: 'App data' } });
  act('rep-pulled', () => { rep.step = 'insert'; log('Bay 1 pulled'); HL.run(render, { type: 'fade' }); });
  act('rep-check', () => { rep.step = 'found'; log('new disk found in Bay 1'); HL.run(render, { type: 'fade' }); });
  act('rep-go', async () => {
    const ok = await stepUp({ plan: { id: UI.planId({ k: 'replace', d: 'an-apps' }), summary: 'rebuild App data’s mirror on Attic NAS' }, title: 'Rebuild the mirror', action: 'Rebuild it', sub: 'Adding a disk writes over it, so HomersLab asks for your passkey.', what: [{ icon: 'disk', text: 'Encrypt the new disk in Bay 1 and make it half of App data’s mirror<small>Serial S6PTNM0T611042A</small>' }] });
    if (!ok) return; S.approvals.push(ok);
    const { d } = vol('an-apps');
    run('rebuild', [{ t: 'Encrypted the new disk', s: 'LUKS2, enrolled in Attic NAS’s TPM; recovery key added to the kit' }, { t: 'Swapped it into the mirror', s: 'btrfs replace, from the missing disk to Bay 1' }, { t: 'Rebuilding: 12 of 46 GB', s: 'From Bay 2' }, { t: 'Rebuilding: 31 of 46 GB', s: 'From Bay 2' }, { t: 'Rebuilt: 46 GB on both disks' }, { t: 'Scrubbed both disks', s: 'No errors' }, { t: 'The mirror is whole again' }],
      { title: 'Rebuilding the mirror', done: 'The mirror is whole again', back: '#/disks/an-apps', plan: UI.planId({ k: 'replace', d: 'an-apps' }), approved: ok, backLabel: 'See App data' }, () => { R4.replaced = true; delete R4.jobs.rebuild; rep.step = 'pull'; toast('App data on Attic NAS is mirrored again. Both disks are healthy.'); go('#/disks/an-apps'); });
    render();
  });

  // ---------- move an app's data to another disk or node ----------
  route(/^#\/apps\/([\w-]+)\/move$/, (m) => {
    const a = inst(m[1]); if (!a) return '<div class="page-head"><div><h1>Not installed</h1></div></div>';
    const d = cat(a.id), from = node(a.node), here = from.disks.find((x) => x.apps), job = R4.jobs['move-' + a.id];
    if (job) return progressPage(job, here, from, `<div class="card stack" style="gap:12px"><h2 class="card-title">7 days to roll back</h2><p class="muted">The old copy on ${esc(from.name)} stays until ${esc(job.meta.until)}. If anything looks wrong, roll back in one action from ${esc(d.name)}’s page.</p>${job.done ? `<button class="btn" data-act="move-back" data-id="${a.id}">${I('rollback')}Roll back now</button>` : ''}</div>`);
    const targets = S.nodes.filter((n) => n.id !== a.node).map((n) => ({ n, disk: n.disks.find((x) => x.apps), f: A.fit(d, n) }));
    const pick = (S.movePick = S.movePick || {})[a.id] || targets.find((x) => x.f.ok)?.n.id;
    return `<div class="page-head"><div class="a-ident" data-app-id="${a.id}">${appIcon(d, 'app-icon-lg')}<div><h1>Move ${esc(UI.poss(d.name))} data</h1><p class="page-sub">${size(a.dataGB)} on ${esc(from.name)}, ${esc(here.name)}. Nothing happens until you press Move.</p></div></div></div>
      <div class="split"><div class="plan-sections">
        <section><div class="plan-q"><h2>Where to</h2><span>A disk with room, on a node that fits it</span></div><div class="group" role="radiogroup" aria-label="Where to">${targets.map(({ n, disk, f }) => `<label class="row row-choice"><input type="radio" name="mv-to" value="${n.id}" data-change="move-pick" data-id="${a.id}" ${pick === n.id ? 'checked' : ''} ${f.ok ? '' : 'disabled'}><span class="row-main"><span class="row-title">${esc(n.name)}, ${esc(disk.name)}</span><span class="row-sub wrap">${f.ok ? `${size(disk.sizeGB - disk.usedGB)} free${disk.layout === 'mirror' ? ', mirrored' : ''}` : esc(f.why.join('; '))}</span></span><span class="row-trail">${I('check2', 'icon checkmark')}</span></label>`).join('')}</div></section>
        <section><div class="plan-q"><h2>What happens</h2><span>About 3 minutes; ${esc(d.name)} is away for under a minute</span></div><ol class="group stepup-list plan-steps">${[['copy', 'A snapshot of its data'], ['move', 'Copy it across (btrfs send and receive)', 'While it keeps running'], ['check', 'Verify every checksum'], ['pause', `A short stop, then a final sync`, 'Under a minute'], ['nodes', `Start it on ${esc(node(pick).name)}, and a health check`], ['clock', 'Keep the old copy for 7 days', 'Rollback in one action until then']].map(([i, t, s]) => `<li>${I(i)}<span>${t}${s ? `<small>${s}</small>` : ''}</span></li>`).join('')}</ol></section>
        <div class="plan-go"><button class="btn btn-primary btn-lg btn-block" data-act="move-go" data-id="${a.id}">Move it</button></div></div>
        <aside class="rail"><div class="card stack" style="gap:12px"><h2 class="card-title">No passkey</h2><p class="muted">Moving data destroys nothing: the old copy stays for 7 days. Its name, sign-in and backups follow it.</p></div></aside></div>`;
  }, { back: (m) => ({ href: `#/apps/${m[1]}`, label: cat(m[1])?.name || 'App' }) });
  act('move-pick', (t) => { S.movePick[t.dataset.id] = t.value; render(); });
  act('move-go', (t) => {
    const a = inst(t.dataset.id), d = cat(a.id), to = node(S.movePick?.[a.id] || S.nodes.find((n) => n.id !== a.node && A.fit(d, n).ok).id), from = node(a.node);
    const until = UI.dayMonth(new Date(now().getTime() + 7 * 86400000));
    run('move-' + a.id, [{ t: 'Took a snapshot' }, { t: `Copied ${size(a.dataGB)} to ${to.name}` }, { t: 'Verified every checksum' }, { t: 'Stopped it for 40 seconds; final sync' }, { t: `Started it on ${to.name}` }, { t: 'Health check passed' }, { t: `Old copy kept on ${from.name} until ${until}` }],
      { title: `Moving ${d.name}`, done: `${d.name} runs on ${to.name} now`, back: `#/apps/${a.id}`, plan: UI.planId({ k: 'move', a: a.id, to: to.id }), until, backLabel: `Go to ${d.name}` }, () => { R4.moved[a.id] = `${to.name}, until ${until}`; a.node = to.id; a.movedFrom = { node: from.id, until }; toast(`${d.name} runs on ${to.name}. The old copy stays until ${until}.`); });
    log(`move ${a.id} → ${to.id}`); render();
  });
  act('move-back', (t) => { const a = inst(t.dataset.id); if (a.movedFrom) { a.node = a.movedFrom.node; delete a.movedFrom; } delete R4.jobs['move-' + a.id]; delete R4.moved[a.id]; toast(`${cat(a.id).name} is back where it was.`, 'rollback'); go(`#/apps/${a.id}`); });

  // ---------- encrypt the system disk: the guided migration's start page ----------
  route(/^#\/nodes\/([\w-]+)\/encrypt$/, (m) => {
    const n = node(m[1]), d = n.disks.find((x) => x.role === 'system'), pre = (S.encPre = S.encPre || { copy: false, vm: false, phrases: 1 });
    const ready = pre.copy && pre.vm && pre.phrases >= 2;
    const row = (done, title, sub, btn) => `<div class="row is-top row-wrap"><span class="row-lead">${done ? I('check2', 'icon icon-md is-done') : I('clock', 'icon icon-md')}</span><span class="row-main"><span class="row-title">${title}</span><span class="row-sub wrap">${sub}</span></span>${btn || ''}</div>`;
    return `<div class="page-head"><div><h1>Encrypt ${esc(UI.poss(n.name))} system disk</h1><p class="page-sub">${esc(d.members[0].model)} · ${size(d.usedGB)} of ${size(d.sizeGB)} used · not encrypted. Encrypting it in place is the riskiest thing HomersLab does, so it asks for three things first.</p></div></div>
      <div class="split"><div class="stack" style="gap:24px">
        <section aria-labelledby="h-pre"><div class="group-head"><h2 id="h-pre">Before it can start</h2></div><div class="group lead-md">
          ${row(pre.copy, 'A full copy, verified', pre.copy ? 'Copied to the Seagate Expansion USB disk at 14:20; every file’s checksum matches' : 'Every byte of the system disk, to an external disk, checked file by file. It is what you boot from if anything goes wrong.', pre.copy ? '' : '<button class="btn btn-sm" data-act="enc-pre" data-k="copy">Make the copy</button>')}
          ${row(pre.vm, 'A rehearsal in a VM', pre.vm ? 'Rehearsed at 14:31 on a copy of this disk’s layout: 29 of 29 checks passed' : 'The whole migration, run first in a virtual machine with this disk’s exact layout, until every check passes.', pre.vm ? '' : '<button class="btn btn-sm" data-act="enc-pre" data-k="vm">Rehearse it</button>')}
          ${row(pre.phrases >= 2, 'Two recovery passphrases', `${pre.phrases} of 2 set: ${pre.phrases >= 2 ? 'Inês’s and Tomás’s' : 'Inês’s. Tomás sets his on his own device, so nobody else ever sees it'}`, pre.phrases >= 2 ? '' : '<button class="btn btn-sm" data-act="enc-pre" data-k="phrases">Ask Tomás</button>')}
          ${row(false, 'A time at the console', 'About two hours with someone at Study PC’s keyboard: it reboots twice, and the first boot asks for a passphrase once.')}</div></section>
        <section aria-labelledby="h-how"><div class="group-head"><h2 id="h-how">How the TPM opens it</h2></div><div class="card stack" style="gap:10px">
          <p>${esc(n.name)} boots with UEFI and Secure Boot, so the TPM binds the key to the firmware and the Secure Boot state (PCRs 0 and 7). If someone changes the firmware or boots something else, the TPM refuses, and a recovery passphrase opens the disk.</p>
          <p class="muted">On a machine that boots in legacy BIOS mode there is no Secure Boot state to bind, so the TPM binds weaker measurements (PCRs 0 and 4). The disk is still encrypted, but someone with the machine could more easily get the TPM to open it. HomersLab says so on that machine’s page, and suggests switching to UEFI first where the firmware allows.</p></div></section></div>
        <aside class="rail"><div class="card stack" style="gap:12px"><h2 class="card-title">Start</h2><p class="muted">${ready ? 'Everything is ready. Pick a time when someone can be at the console.' : 'The start opens once the copy is verified, the rehearsal has passed and both passphrases are set.'}</p><button class="btn btn-primary btn-block" data-act="enc-start" ${ready ? '' : 'disabled'}>${I('key')}Start with your passkey</button><p class="subhead">It can be stopped before the first reboot; after it, the copy is the way back.</p></div></aside></div>`;
  }, { back: (m) => ({ href: `#/nodes/${m[1]}`, label: node(m[1])?.name || 'Node' }) });
  act('enc-pre', (t) => { const k = t.dataset.k; if (k === 'phrases') S.encPre.phrases = 2; else S.encPre[k] = true; toast(k === 'copy' ? 'Prototype: the copy and its check would take about 40 minutes.' : k === 'vm' ? 'Prototype: the rehearsal would take about 25 minutes.' : 'Tomás set his passphrase on his phone.', 'info'); render(); });
  act('enc-start', async () => { const ok = await stepUp({ plan: { id: UI.planId({ k: 'encsys' }), summary: 'encrypt Study PC’s system disk' }, title: 'Encrypt the system disk', action: 'Start', sub: 'This changes how Study PC boots, so HomersLab asks for your passkey.', what: [{ icon: 'lock', text: 'Encrypt Study PC’s system disk in place, with two reboots<small>Bound to the TPM (PCRs 0+7); two recovery passphrases</small>' }] }); if (ok) { S.approvals.push(ok); toast('Prototype: the migration would start now, step by step at the console.', 'info'); } });
})();
