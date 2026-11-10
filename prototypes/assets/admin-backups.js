// HomersLab Admin prototype: backups (every hour), restore tests (every Sunday), the weekly archive, and restoring
// one app. PROTOTYPE, throwaway. Restoring an app replaces its data, a risky change: the passkey.
(() => {
  const { D, S, cat, inst, node, and, appIcon, chev, weeks, weeksAxis, weeksLegend, route, render, go, act, log } = A;
  const { esc, toast, dialog, stepUp, hhmm, now, gb } = UI;

  const sinceTop = () => { const n = now(); const mins = n.getMinutes(); return mins < 1 ? 'Just now' : `${mins} min ago`; };
  route(/^#\/backups$/, () => {
    const apps = S.installed, t = apps.filter((a) => a.restore[11] !== null), failed = t.filter((a) => a.restore[11] === 'fail');
    const last = apps.map((a) => a.lastBackup?.at).filter(Boolean).sort().pop() || '14:00';
    const row = (lead, name, sub, results, mins, href, extra = '') => {
      const lr = results[11];
      return `<tr>
        <td><div class="ident">${lead}<div class="names">${href ? `<a href="${href}"><b>${esc(name)}</b></a>` : `<b>${esc(name)}</b>`}<span>${esc(sub)}</span></div></div></td>
        <td class="line2" style="display:table-cell">${weeks(results)}</td>
        <td class="end">${lr === 'fail' ? '<span class="pill pill-red">Failed</span>' : lr === 'pass' ? '<span class="cell-state"><span class="dot dot-green"></span>Passed</span>' : '<span class="muted-cell">On Sunday</span>'}</td>
        <td class="n hide-sm">${lr === null ? '<span class="muted-cell">—</span>' : `${mins} min`}</td>
        <td class="n hide-sm">${extra}</td></tr>`;
    };
    return `
      <div class="page-head"><div><h1>Backups</h1><p class="page-sub">Every app was backed up at ${esc(last)}, to all three places. On Sunday ${t.length - failed.length} of ${t.length} apps passed their restore test${failed.length ? `; ${esc(and(failed.map((a) => cat(a.id).name)))} didn’t` : ''}.</p></div>
        <div class="page-actions"><button class="btn" data-act="backup-now">${I('backups')}Back up now</button><a class="btn btn-primary" href="#/backups/restore">${I('rollback')}Restore an app</a></div></div>
      ${failed.length ? `<div class="notice notice-error a-finding" role="status">${I('alert')}<span class="notice-body"><b>${esc(cat(failed[0].id).name)}: Sunday’s restore test failed.</b>${esc(failed[0].restoreFail.why)} The backup itself is fine; it is the restored copy that didn’t come up.</span><span class="notice-actions"><button class="btn btn-sm" data-act="retest" data-id="${failed[0].id}" ${S.jobs[failed[0].id] ? 'disabled' : ''}>${S.jobs[failed[0].id] ? '<span class="spinner"></span>Testing…' : 'Run the test again'}</button></span></div>` : ''}
      <div class="widgets a-widgets">
        <div class="widget"><span class="widget-label">Last backup</span><span class="widget-value">${sinceTop()}</span><span class="widget-caption">Every hour, to 3 places; the next at 15:00</span></div>
        <div class="widget"><span class="widget-label">Kept</span><span class="widget-value">${gb(D.storedGB)}</span><span class="widget-caption">For ${gb(D.dataGB)} of data, deduplicated and encrypted</span></div>
        <div class="widget ${failed.length ? 'is-bad' : ''}"><span class="widget-label">Restore tests</span><span class="widget-value">${t.length - failed.length}<small>of ${t.length} passed</small></span><span class="widget-caption">${failed.length ? `${esc(and(failed.map((a) => cat(a.id).name)))} failed on Sunday` : 'All passed on Sunday'}</span></div>
        <div class="widget"><span class="widget-label">Weekly archive</span><span class="widget-value">${D.archive.sizeMB} MB</span><span class="widget-caption">Sunday 8 Nov, 03:34 · to B2 and Proton</span></div>
      </div>
      <section class="a-block" aria-labelledby="h-rt"><div class="a-block-head"><div><h2 id="h-rt">Restore tests</h2><p>Every Sunday at 04:00 each app’s latest backup is restored beside it, started, health-checked and signed in to as a test person, then thrown away.</p></div></div>
        <div class="group"><table class="rtable has-lead" style="--lead:32px"><thead><tr><th>App</th><th>Last 12 Sundays</th><th class="n">Sunday 8 Nov</th><th class="n">Takes</th><th><span class="sr-only">Action</span></th></tr></thead><tbody>
          ${apps.map((a) => { const d = cat(a.id); return row(appIcon(d, 'app-icon-md'), d.name, `${d.purpose} · ${node(a.node).name}`, a.restore, a.restoreMin, `#/apps/${a.id}`, a.restore[11] === 'fail' ? `<a class="btn btn-sm" href="#/apps/${a.id}">See why</a>` : ''); }).join('')}
          ${D.platform.map((p) => row(p.id === 'signin' ? `<img class="app-icon app-icon-md" src="icons/apps/authentik.svg" alt="">` : `<span class="app-icon app-icon-md is-mark" role="img" aria-label="HomersLab">${MARK_16}</span>`, p.name, p.detail, p.restore, p.restoreMin, null)).join('')}
        </tbody></table></div>
        <div class="group-foot cluster" style="gap:16px">${weeksLegend(['pass', 'fail'])}<span>A failure is mailed and pushed to Inês within a minute.</span></div></section>
      <section class="a-block" aria-labelledby="h-tg"><div class="a-block-head"><div><h2 id="h-tg">Where the backups go</h2><p>Deduplicated and encrypted with restic: ${gb(D.storedGB)} kept for ${gb(D.dataGB)} of data. Every hour, at the top of the hour.</p></div></div>
        <div class="target-cards">${D.targets.map((x) => `<section class="card" aria-labelledby="tg-${x.id}"><div class="card-head"><h3 class="card-title" id="tg-${x.id}">${esc(x.name)}</h3><span class="card-meta">Last ${esc(x.last)}</span></div>
          <dl class="kv"><dt>How</dt><dd>${esc(x.kind)}</dd><dt>Where</dt><dd>${esc(x.where)}</dd><dt>Kept</dt><dd class="num">${gb(x.storedGB)}</dd><dt>Room</dt><dd class="num">${x.freeTB ? `${x.freeTB.toFixed(2)} TB free` : 'Pay as it grows'}</dd>${x.immutable ? `<dt>Locked</dt><dd>${esc(x.immutable)}</dd>` : ''}</dl>
          <p class="subhead">${esc(x.note)}</p></section>`).join('')}</div></section>
      <section class="a-block" aria-labelledby="h-ar"><div class="a-block-head"><div><h2 id="h-ar">The weekly archive</h2><p>The small, sensitive things, once a week, encrypted to a key that isn’t on any machine. Someone who took over a node could read the hourly backups, but never this.</p></div></div>
        <div class="archive-cards"><div class="card stack" style="gap:12px"><h3 class="card-title">What it holds</h3><ul class="lines">${D.archive.holds.map((h) => `<li>${I('lock')}<span>${esc(h[0].toUpperCase() + h.slice(1))}</span></li>`).join('')}</ul></div>
          <div class="card stack" style="gap:12px"><h3 class="card-title">When and where</h3><dl class="kv"><dt>When</dt><dd>${esc(D.archive.day)} at ${esc(D.archive.at)}</dd><dt>Last</dt><dd>${esc(D.archive.last)}, <span class="num">${D.archive.sizeMB} MB</span></dd><dt>To</dt><dd>${esc(and(D.archive.to))}</dd><dt>Its key</dt><dd><span class="mono">${esc(D.archive.key)}</span><span class="subhead" style="display:block">The public half. The private half is only in your recovery kit.</span></dd></dl>
            <div class="btn-row"><button class="btn" data-act="by-hand">${I('terminal')}Restore by hand</button></div></div></div></section>`;
  });
  act('backup-now', () => { log('backup now'); toast('Backing up every app now; it takes a few minutes. You get a mail only if something fails.', 'backups'); });
  act('by-hand', () => dialog({ title: 'Restore by hand, without HomersLab', sub: 'The recovery kit has the one-page how-to. Plain restic and age are all it takes.', wide: true,
    body: `<p class="group-head">List an app’s backups on Attic NAS</p><div class="cmd"><code>restic -r sftp:attic-nas:/srv/backups snapshots --tag paperless</code></div>
      <p class="group-head">Restore the latest into a folder</p><div class="cmd"><code>restic -r sftp:attic-nas:/srv/backups restore latest --tag paperless --target ./paperless</code></div>
      <p class="group-head">Open the archive with the key from the kit</p><div class="cmd"><code>age -d -i recovery-key.txt archive-2026-11-08.tar.age | tar x</code></div>
      <p class="subhead">The restic password is in the recovery kit too. Each restored app comes with its plain compose file.</p>`, actions: [{ label: 'Close', value: 'ok', kind: 'primary' }] }));

  // ---------- restore one app to an earlier point ----------
  const R = { app: null, point: 0, older: false, job: null };
  const sameDay = (a, b) => a.toDateString() === b.toDateString();
  const pointLabel = (p) => { const d = p.d, n = D.NOW, y = new Date(n); y.setDate(n.getDate() - 1); return `${sameDay(d, n) ? 'Today' : sameDay(d, y) ? 'Yesterday' : UI.weekdayDate(d)}, ${hhmm(d)}`; };
  const groupOf = (p) => (p.kind === 'hourly' ? (sameDay(p.d, D.NOW) ? 'Today, every hour' : 'The day before, every hour') : p.kind === 'daily' ? 'Every day, for a month' : 'Sundays, for 3 months');
  route(/^#\/backups\/restore$/, (m, q) => {
    if (q.get('app') && R.app !== q.get('app')) { R.app = q.get('app'); R.point = 0; R.job = null; }
    if (!R.app || !inst(R.app)) R.app = S.installed[0].id;
    const a = inst(R.app), d = cat(a.id), n = node(a.node), pts = D.points, p = pts[R.point];
    if (R.job) return restoreProgress(a, d, n, p);
    const shown = R.older ? pts : pts.slice(0, 6);
    const groups = []; shown.forEach((x, i) => { const g = groupOf(x); if (!groups.length || groups[groups.length - 1].g !== g) groups.push({ g, items: [] }); groups[groups.length - 1].items.push([x, i]); });
    return `
      <div class="page-head"><div><h1>Restore an app</h1><p class="page-sub">Take one app back to an earlier backup. The others carry on as they are.</p></div></div>
      <div class="split"><div class="stack" style="gap:24px">
        <section aria-labelledby="h-ra"><div class="group-head"><h2 id="h-ra">Which app</h2></div><div class="group"><div class="audience-box"><label class="sr-only" for="ra-app">App</label>
          <select id="ra-app" class="select" data-change="ra-app">${S.installed.map((x) => `<option value="${x.id}" ${x.id === a.id ? 'selected' : ''}>${esc(cat(x.id).name)} (${esc(cat(x.id).purpose)})</option>`).join('')}</select></div></div></section>
        <section aria-labelledby="h-rp"><div class="group-head"><h2 id="h-rp">Which point</h2><span>${esc(D.RETENTION)}</span></div>
          ${groups.map((g) => `<div class="point-group"><p class="group-head" style="font-weight:400">${esc(g.g)}</p><div class="group" role="radiogroup" aria-label="${esc(g.g)}">${g.items.map(([x, i]) => `<label class="row row-choice"><input type="radio" name="ra-point" value="${i}" data-change="ra-point" ${i === R.point ? 'checked' : ''}><span class="row-main"><span class="row-label num">${esc(pointLabel(x))}</span>${i === 0 ? '<span class="row-sub">The latest backup, this hour</span>' : ''}</span><span class="row-trail">${I('check2', 'icon checkmark')}</span></label>`).join('')}</div></div>`).join('')}
          ${R.older ? '' : `<button class="btn btn-plain" style="margin-top:12px" data-act="ra-older">Show older backups (${pts.length - 6} more)</button>`}</section>
      </div>
      <aside class="rail" aria-label="What happens"><div class="card stack" style="gap:14px"><h2 class="card-title">What happens</h2>
        <ul class="lines">
          <li>${I('copy')}<span>${esc(UI.poss(d.name))} data as it is now is backed up first<small>So this restore can be undone</small></span></li>
          <li>${I('pause')}<span>${esc(d.name)} stops, for about ${a.restoreMin} minutes</span></li>
          <li>${I('rollback')}<span>It goes back to <b class="num">${esc(pointLabel(p))}</b></span></li>
          <li>${I('alert')}<span>Changes since then are gone from ${esc(d.name)}<small>They stay in the backup taken first</small></span></li>
          <li>${I('check')}<span>Health check, then it is back on Home</span></li></ul>
        <button class="btn btn-primary btn-lg btn-block" data-act="ra-go">${I('key')}Restore with your passkey</button>
        <p class="stepup-bound">${I('lock')}<span>Restoring replaces an app’s data, a risky change: your passkey approves it.</span></p></div></aside></div>`;
  }, { back: { href: '#/backups', label: 'Backups' } });
  act('ra-app', (t) => { R.app = t.value; R.point = 0; history.replaceState(null, '', location.pathname + location.search + '#/backups/restore?app=' + t.value); render(); });
  act('ra-point', (t) => { R.point = +t.value; A.soon(); });
  act('ra-older', () => { R.older = true; render(); });
  act('ra-go', async () => {
    const a = inst(R.app), d = cat(a.id), p = D.points[R.point];
    const plan = { id: UI.planId({ k: 'restore', a: a.id, p: R.point }), summary: `restore ${d.name} to ${pointLabel(p)}` };
    const ok = await stepUp({ plan, title: `Restore ${esc(d.name)}`, action: 'Restore it', sub: 'Restoring replaces an app’s data, so HomersLab asks for your passkey.', what: [{ icon: 'rollback', text: `${esc(d.name)} goes back to ${esc(pointLabel(p))}` }, { icon: 'copy', text: 'Its data as it is now is backed up first' }] });
    if (!ok) return; S.approvals.push(ok);
    R.job = { step: 0, planId: ok.planId, at: ok.at, steps: [`Backed up ${d.name} as it is now`, `Stopped ${d.name}`, `Restored its data and database from ${pointLabel(p)}`, `Started ${d.name}`, 'Health check passed'] };
    log(`restore ${a.id} to point ${R.point} (${ok.planId})`); render();
    const tick = () => { if (!R.job) return; R.job.step++; if (location.hash.startsWith('#/backups/restore')) render(); if (R.job.step < R.job.steps.length) setTimeout(tick, 800); else toast(`${d.name} is back to ${pointLabel(p)}.`, 'rollback'); };
    setTimeout(tick, 700);
  });
  function restoreProgress(a, d, n, p) {
    const j = R.job, done = j.step >= j.steps.length;
    return `
      <div class="page-head"><div class="a-ident">${appIcon(d, 'app-icon-lg')}<div><h1>${done ? `${esc(d.name)} is restored` : `Restoring ${esc(d.name)}`}</h1><p class="page-sub">To ${esc(pointLabel(p))} · plan <span class="plan-id">${esc(j.planId)}</span>, approved by Inês at ${esc(j.at)}</p></div></div>
        <div class="page-actions">${done ? `<a class="btn btn-primary" href="#/apps/${a.id}">Go to ${esc(d.name)}${I('arrowRight')}</a><button class="btn" data-act="ra-again">Restore another</button>` : ''}</div></div>
      <section class="card stack" aria-label="Progress" style="max-width:760px;gap:12px"><div class="progress-row"><div class="progress-head"><b>${done ? 'Done' : 'Restoring'}</b><span>${Math.min(j.step, j.steps.length)} of ${j.steps.length}</span></div><div class="progress" role="progressbar" aria-label="Restore progress" aria-valuemin="0" aria-valuemax="${j.steps.length}" aria-valuenow="${j.step}"><span style="--value:${(j.step / j.steps.length) * 100}%"></span></div></div>
        <ol class="steps" aria-live="polite">${j.steps.map((s, i) => { const st = i < j.step ? 'done' : i === j.step ? 'now' : 'todo'; return `<li class="step is-${st}"><span class="step-mark">${st === 'done' ? I('check2') : st === 'now' ? '<span class="spinner"></span>' : ''}</span><span class="step-text"><span class="step-title">${esc(s)}</span><span class="sr-only">${st === 'done' ? 'Done' : st === 'now' ? 'In progress' : 'To do'}</span></span><span class="step-time"></span></li>`; }).join('')}</ol>
        ${done ? `<div class="notice">${I('info')}<span class="notice-body">The copy from before the restore is kept as a backup from today, ${esc(hhmm(now()))}. Restore it the same way to undo this.</span></div>` : ''}</section>`;
  }
  act('ra-again', () => { R.job = null; render(); });
})();
