// HomersLab Admin prototype: Settings › Alerts and notifications (ADR 0018) and Settings › Domains (ADR 0016).
// PROTOTYPE, throwaway. Alerts are per admin and per level; mail is always on for critical. Domains: HomersLab writes
// only its own DNS records, marked as its own; every domain serves every app; sign-in stays on the main domain.
(() => {
  const { D, S, cat, and, route, render, go, act, log } = A;
  const { esc, toast, dialog, stepUp, hhmm, now } = UI;
  const AL = (S.alerts = S.alerts || JSON.parse(JSON.stringify(D.alerts)));
  const DOM = (S.domains = S.domains || JSON.parse(JSON.stringify(D.domains)));
  const main = () => DOM.find((d) => d.main);

  // ---------- the maintenance window, as Admin and Home show it (a prototype state) ----------
  Proto.option({ key: 'window', label: 'Maintenance window', def: 'no', values: [{ v: 'no', name: 'Not now (14:06)' }, { v: 'yes', name: 'Running (02:40)' }], note: 'In the window, planned work runs and shows “updating”, on Home too.' });
  Proto.onChange((k) => { if (k === 'window') render(); });
  A.inWindow = () => Proto.get('window', 'no') === 'yes';
  A.windowNotice = () => (A.inWindow() ? `<div class="notice notice-info a-finding" role="status">${I('clock')}<span class="notice-body"><b>The maintenance window is running, until ${esc(AL.window.to)}.</b>Jellyfin is updating to 10.11.3 (Home says “Updating until 03:00”), then the weekly scrub of Attic NAS’s app data. Their own alerts wait for the morning digest.</span><span class="notice-actions"><a class="btn btn-sm" href="#/settings/alerts">The window</a></span></div>` : '');

  const LEVEL = { critical: 'Critical', warning: 'Warning', resolved: 'Resolved' };
  const notif = (x) => `<article class="notif is-${x.level}" aria-label="${esc(LEVEL[x.level])} alert: ${esc(x.title)}"><span class="notif-mark" aria-hidden="true"></span><div class="notif-top"><span><span class="notif-level">${LEVEL[x.level]}</span> · HomersLab · ${esc(x.via)}</span><span>${esc(x.when)}</span></div><div class="notif-title">${esc(x.title)}</div><div class="notif-body">${esc(x.body)}</div></article>`;
  A.notif = notif;
  const sec = (id, title, words, body) => `<section class="sec" aria-labelledby="h-${id}"><div class="sec-head"><h2 id="h-${id}">${title}</h2><p>${words}</p></div><div class="sec-body">${body}</div></section>`;
  const HOURS = Array.from({ length: 24 }, (_, h) => `${String(h).padStart(2, '0')}:00`);
  const timeSel = (id, v, label) => `<label class="sr-only" for="${id}">${label}</label><select id="${id}" class="input al-time" data-change="al-time" data-k="${id}">${HOURS.map((h) => `<option ${h === v ? 'selected' : ''}>${h}</option>`).join('')}</select>`;

  // ---------- alerts and notifications, for Inês ----------
  route(/^#\/settings\/alerts$/, () => `
    <div class="page-head"><div><h1>Alerts and notifications</h1><p class="page-sub">Yours, Inês: each admin chooses their own. One alert per incident, then “resolved”; a reminder every 4 hours while it lasts. The family hears on Home only.</p></div></div>
    ${A.windowNotice()}
    ${sec('al-ch', 'Where each level reaches you', 'Mail is always on for critical alerts, so nothing depends on one channel.',
      `<div class="group al-wide"><table class="rtable al-table"><thead><tr><th>Level</th>${AL.channels.map((c) => `<th class="c">${esc(c.name)}</th>`).join('')}</tr></thead><tbody>
        ${AL.levels.map((l) => `<tr><td><div class="names al-level"><b>${esc(l.name)}</b><span>${esc(l.words)}</span></div></td>${AL.channels.map((c) => `<td class="c"><input type="checkbox" class="switch" data-change="al-ch" data-l="${l.id}" data-c="${c.id}" ${l.on[c.id] ? 'checked' : ''} ${l.locked === c.id || c.off ? 'disabled' : ''} aria-label="${esc(l.name)} by ${esc(c.name)}${l.locked === c.id ? ': always on' : c.off ? ': not set up' : ''}"></td>`).join('')}</tr>`).join('')}
      </tbody></table></div>
      ${AL.levels.map((l) => `<div class="al-phone"><p class="group-head">${esc(l.name)}</p><div class="group">${AL.channels.map((c) => `<label class="row"><span class="row-main"><span class="row-label">${esc(c.name)}</span>${l.locked === c.id ? '<span class="row-sub">Always on for critical</span>' : c.off ? '<span class="row-sub">Not set up</span>' : ''}</span><input type="checkbox" class="switch" data-change="al-ch" data-l="${l.id}" data-c="${c.id}" ${l.on[c.id] ? 'checked' : ''} ${l.locked === c.id || c.off ? 'disabled' : ''} aria-label="${esc(l.name)} by ${esc(c.name)}"></label>`).join('')}</div></div>`).join('')}
      <div class="group lead-sm">${AL.channels.map((c) => `<div class="row"><span class="row-lead">${I(c.icon, 'icon icon-md')}</span><span class="row-main"><span class="row-title">${esc(c.name)}</span><span class="row-sub wrap">${esc(c.detail)}</span></span>${c.off ? '<button class="btn btn-sm" data-act="al-webhook">Set it up</button>' : '<button class="btn btn-sm" data-act="al-test" data-c="' + c.id + '">Send a test</button>'}</div>`).join('')}</div>`)}
    ${sec('al-q', 'Quiet hours', 'In these hours only critical alerts push: the primary down, a disk failing, security events. The rest waits for the morning digest.',
      `<div class="group"><div class="row row-wrap"><span class="row-main"><span class="row-label">Quiet from</span></span>${timeSel('quiet-from', AL.quiet.from, 'Quiet from')}<span class="al-to">to</span>${timeSel('quiet-to', AL.quiet.to, 'Quiet until')}</div></div>`)}
    ${sec('al-d', 'The morning digest', `At ${esc(AL.quiet.to)}, by ${esc(AL.channels.filter((c) => AL.levels[2].on[c.id]).map((c) => c.name.toLowerCase()).join(' and '))}: the night’s planned work, and what waited through quiet hours.`,
      `<article class="card al-digest" aria-label="The morning digest, as it arrives"><p class="subhead">HomersLab · Tuesday 10 Nov, ${esc(AL.digest.at)}</p><h3>Good morning, Inês. All is well; two things to look at.</h3>
        <ul class="lines">${AL.digest.items.map((x, i) => `<li>${I(i < 2 ? 'check' : 'alert')}<span>${esc(x)}</span></li>`).join('')}</ul><p class="subhead">Open Admin over the mesh for details. Reply STOP to change what you get.</p></article>`)}
    ${sec('al-w', 'The maintenance window', 'Updates, scrubs, restore tests and moves run in it, every night. Planned work silences its own alerts and shows “updating” on Home.',
      `<div class="group"><div class="row row-wrap"><span class="row-main"><span class="row-label">Every night from</span></span>${timeSel('win-from', AL.window.from, 'Window from')}<span class="al-to">to</span>${timeSel('win-to', AL.window.to, 'Window until')}</div>
        <div class="row is-top row-wrap"><span class="row-main"><span class="row-label">Tonight</span><span class="row-sub wrap">${esc(AL.window.tonight.join(' · '))}</span></span></div></div>
        <p class="group-foot">You can still run something now from its page; its plan says who it affects. The household chooses the window, for every admin.</p>`)}
    ${sec('al-ex', 'What alerts look like', 'Four examples, as they would reach you.',
      `<div class="al-notifs">${AL.examples.map(notif).join('')}</div><div class="btn-row"><button class="btn" data-act="al-show">${I('bell')}Show them as alerts here</button></div>`)}`,
    { back: { href: '#/settings', label: 'Settings' } });
  act('al-ch', (t) => { AL.levels.find((l) => l.id === t.dataset.l).on[t.dataset.c] = t.checked; log(`alerts: ${t.dataset.l} by ${t.dataset.c} ${t.checked ? 'on' : 'off'}`); });
  act('al-time', (t) => { const k = t.dataset.k; if (k === 'quiet-from') AL.quiet.from = t.value; if (k === 'quiet-to') AL.quiet.to = t.value; if (k === 'win-from') AL.window.from = t.value; if (k === 'win-to') AL.window.to = t.value; log(`${k} ${t.value}`); A.soon(); });
  act('al-test', (t) => toast(`A test went out by ${AL.channels.find((c) => c.id === t.dataset.c).name.toLowerCase()}.`, 'bell'));
  act('al-webhook', () => toast('Prototype: a webhook address (Matrix, a Signal bot, Home Assistant) would be typed here; it only travels inward.', 'info'));
  act('al-show', () => AL.examples.forEach((x, i) => setTimeout(() => toast(`${x.title}. ${x.body.split('.')[0]}.`, x.icon), i * (UI.still() ? 50 : 1400))));

  // ---------- domains ----------
  const recRow = (d, r) => `<tr><td><span class="host">${esc(r.name === '@' ? d.name : `${r.name}.${d.name}`)}</span></td><td>${esc(r.type)}</td><td class="hide-sm dns-val">${esc(r.value)}</td>
    <td>${r.refused ? `<span class="owner-mark is-refused">${I('x')}Someone else’s: ${esc(r.blocks)} refused</span>` : r.owner === 'homerslab' ? `<span class="owner-mark is-ours">${I('check2')}HomersLab’s</span>` : `<span class="owner-mark is-theirs">${I('lock')}Someone else’s: untouched</span>`}</td></tr>`;
  route(/^#\/settings\/domains$/, () => {
    const m = main();
    return `<div class="page-head"><div><h1>Domains</h1><p class="page-sub">Every app answers on every domain, with no redirect. Sign-in stays on <span class="host">auth.${esc(m.name)}</span>, so one passkey works everywhere.</p></div>
        <div class="page-actions"><a class="btn" href="#/settings/domains/rename">Rename the main domain</a><a class="btn btn-primary" href="#/settings/domains/add">${I('plus')}Add a domain</a></div></div>
      ${DOM.map((d) => `<section class="a-block" aria-labelledby="dm-${esc(d.name.replace(/\W/g, ''))}" style="margin-bottom:36px">
        <div class="a-block-head"><div><h2 id="dm-${esc(d.name.replace(/\W/g, ''))}"><span class="host">${esc(d.name)}</span>${d.main ? ' <span class="pill">Main domain</span>' : ''}</h2><p>DNS at ${esc(d.dns)} · token: ${esc(d.token)} · added ${esc(d.added)}</p></div></div>
        ${d.cert.failing && !d.fixed ? `<div class="notice notice-warn a-finding" role="status">${I('lock')}<span class="notice-body"><b>${esc(d.cert.name)} expires on ${esc(d.cert.until)}, and its renewal is failing.</b>${esc(d.cert.failing)}. HomersLab alerts 14 and 3 days before.</span><span class="notice-actions"><button class="btn btn-sm" data-act="dom-token" data-d="${esc(d.name)}">Replace the token</button></span></div>` : ''}
        <div class="card"><dl class="disk-facts"><div><dt>Certificate</dt><dd>${esc(d.cert.name)}, ${esc(d.cert.issuer)}, until ${esc(d.fixed ? '7 Feb 2027' : d.cert.until)}</dd></div><div><dt>Apps here</dt><dd>${S.installed.length} of ${S.installed.length}, e.g. <span class="host">documents.${esc(d.name)}</span></dd></div><div><dt>Sign-in</dt><dd>${d.main ? `Here: <span class="host">auth.${esc(d.name)}</span>` : `On <span class="host">auth.${esc(main().name)}</span>; a guard cookie per domain`}</dd></div></dl></div>
        <div class="group" style="margin-top:var(--gap)"><table class="rtable dns-table"><thead><tr><th>Name</th><th>Type</th><th class="hide-sm">Value</th><th>Owner</th></tr></thead><tbody>${d.records.map((r) => recRow(d, r)).join('')}</tbody></table></div>
        ${d.records.filter((r) => r.refused).map((r) => `<p class="group-foot dns-refused">${I('x')}${esc(r.refused)}</p>`).join('')}
        <p class="group-foot">HomersLab marks each record it writes with an owner record beside it (<span class="mono">_homerslab-owner</span>) and never edits or deletes a record it didn’t write.</p></section>`).join('')}`;
  }, { back: { href: '#/settings', label: 'Settings' } });
  act('dom-token', async (t) => {
    const d = DOM.find((x) => x.name === t.dataset.d);
    const v = await dialog({ title: `Replace ${esc(d.name)}’s DNS token`, sub: `A new API token from ${esc(d.dns)}, with permission to edit this zone’s records. It travels only inward and is never shown again.`,
      body: `<div class="field"><label class="field-label" for="dt-token">The token</label><input id="dt-token" class="input input-secret" type="password" autocomplete="off" placeholder="Paste it here"><span class="hint">HomersLab still writes only its own records.</span></div>`,
      actions: [{ label: 'Cancel', value: 'no', kind: 'ghost' }, { label: 'Continue', value: 'yes', kind: 'primary' }] });
    if (v !== 'yes') return;
    const ok = await stepUp({ plan: { id: UI.planId({ k: 'dnstoken', d: d.name }), summary: `replace ${d.name}’s DNS token` }, title: `Replace ${esc(d.name)}’s DNS token`, action: 'Replace it', sub: 'A DNS token can change where the household’s names point, so HomersLab asks for your passkey.', what: [{ icon: 'key', text: `The new token for ${esc(d.name)} at ${esc(d.dns)}<small>Checked first; the certificate renews right after</small>` }] });
    if (!ok) return; S.approvals.push(ok); d.fixed = true; d.token = `Replaced today at ${ok.at}`; log(`dns token ${d.name} replaced`); toast(`The token works. ${d.cert.name} renewed until 7 Feb 2027.`, 'lock'); render();
  });

  // ---------- add a domain, step by step ----------
  const AD = (S.addDom = S.addDom || { step: 'name', name: '', dns: null });
  const GUESS = (n) => (/\.pt$/.test(n) ? 'Amen.pt' : /\.ch$/.test(n) ? 'Infomaniak' : /\.(de|eu)$/.test(n) ? 'Hetzner' : 'Cloudflare');
  route(/^#\/settings\/domains\/add$/, () => {
    const job = S.r4.jobs['dom-' + AD.name];
    if (AD.step === 'run' && job) return A.progressPage4(job, null, null, `<div class="card stack" style="gap:12px"><h2 class="card-title">Sign-in stays put</h2><p class="muted">People keep signing in on <span class="host">auth.${esc(main().name)}</span>; their passkeys don’t change.</p></div>`);
    const m = main(), apps = S.installed.slice(0, 4).map((a) => `${A.host(a.id).split('.')[0]}.${AD.name || 'example.org'}`);
    const steps = [['name', 'Its name'], ['token', 'Its DNS token'], ['plan', 'What happens']];
    const nav = `<ol class="lines dom-steps">${steps.map(([k, l], i) => `<li class="${AD.step === k ? 'is-now' : ''}">${I(steps.findIndex((s) => s[0] === AD.step) > i ? 'check2' : 'globe')}<span>${l}</span></li>`).join('')}</ol>`;
    let body;
    if (AD.step === 'name') body = `<section class="card stack" style="gap:16px"><h2 class="card-title">Which domain?</h2><div class="field"><label class="field-label" for="ad-dom">The domain</label><input id="ad-dom" class="input" placeholder="ferreira.casa" value="${esc(AD.name)}" autocomplete="off" spellcheck="false" aria-describedby="ad-dom-h"><span class="hint" id="ad-dom-h">One you own. Its mail and web records stay as they are.</span></div><div class="btn-row"><button class="btn btn-primary" data-act="ad-next" data-to="token">Continue</button></div></section>`;
    else if (AD.step === 'token') body = `<section class="card stack" style="gap:16px"><h2 class="card-title"><span class="host">${esc(AD.name)}</span>’s DNS token</h2><p>Its name servers are at <b>${esc(AD.dns)}</b>. HomersLab needs an API token there that may edit this zone’s records, to prove the domain for its certificate.</p>
      <div class="field"><label class="field-label" for="ad-token">The token</label><input id="ad-token" class="input input-secret" type="password" autocomplete="off" placeholder="Paste it here" aria-describedby="ad-token-h"><span class="hint" id="ad-token-h">It travels only inward, stays on Study PC, sealed to its TPM, and is never shown again.</span></div>
      <details class="more"><summary>How to make one at ${esc(AD.dns)}</summary><ol class="help-steps"><li>Sign in at ${esc(AD.dns)} and open the zone ${esc(AD.name)}</li><li>Create an API token with permission to edit DNS records for this zone only</li><li>Copy it, and paste it here</li></ol></details>
      <div class="btn-row"><button class="btn" data-act="ad-next" data-to="name">Back</button><button class="btn btn-primary" data-act="ad-next" data-to="plan">Check the token</button></div></section>`;
    else body = `<section class="card stack" style="gap:16px"><h2 class="card-title">Add <span class="host">${esc(AD.name)}</span></h2>
      <ol class="group stepup-list plan-steps">
        <li>${I('check')}<span>The token works, and may edit ${esc(AD.name)}’s records<small>Checked just now; no record was changed</small></span></li>
        <li>${I('lock')}<span>A wildcard certificate, <span class="host">*.${esc(AD.name)}</span><small>From Let’s Encrypt, proven with a challenge record that is removed right after</small></span></li>
        <li>${I('apps')}<span>Every app answers here too, with no redirect<small>${apps.map((x) => `<span class="host">${esc(x)}</span>`).join(', ')} and the rest</small></span></li>
        <li>${I('user')}<span>Sign-in stays on <span class="host">auth.${esc(m.name)}</span><small>${S.installed.length} sign-in clients get this domain’s addresses; guards set a cookie for it, one extra hop the first time</small></span></li>
        <li>${I('globe')}<span>No public record yet<small>The wildcard CNAME is written only when an app goes public here, and shown in that plan</small></span></li></ol>
      <div class="group"><div class="row is-top row-wrap"><span class="row-main"><span class="row-label">Records already in the zone</span><span class="row-sub wrap">MX, SPF and the apex: someone else’s, left untouched. No name HomersLab needs is taken.</span></span><span class="owner-mark is-theirs">${I('lock')}Untouched</span></div></div>
      <div class="btn-row"><button class="btn" data-act="ad-next" data-to="token">Back</button><button class="btn btn-primary" data-act="ad-go">${I('key')}Add it with your passkey</button></div></section>`;
    return `<div class="page-head"><div><h1>Add a domain</h1><p class="page-sub">Each domain has its own DNS token and wildcard certificate. HomersLab writes only its own records there, marked as its own.</p></div></div>
      <div class="split">${body}<aside class="rail"><div class="card stack" style="gap:12px"><h2 class="card-title">Steps</h2>${nav}</div></aside></div>`;
  }, { back: { href: '#/settings/domains', label: 'Domains' }, after: () => document.querySelector('#ad-dom')?.focus() });
  act('ad-next', (t) => {
    if (t.dataset.to === 'token') { const v = (document.querySelector('#ad-dom')?.value || AD.name).trim().toLowerCase(); if (!/^[a-z0-9-]+(\.[a-z0-9-]+)+$/.test(v)) return toast('Type a domain, such as ferreira.casa.', 'info'); if (DOM.some((d) => d.name === v)) return toast(`${v} is already one of the household’s domains.`, 'info'); AD.name = v; AD.dns = GUESS(v); }
    if (t.dataset.to === 'plan' && !(document.querySelector('#ad-token')?.value || '').trim()) return toast('Paste the token first.', 'info');
    AD.step = t.dataset.to; HL.run(render, { type: 'fade' });
  });
  act('ad-go', async () => {
    const ok = await stepUp({ plan: { id: UI.planId({ k: 'adddom', d: AD.name }), summary: `add ${AD.name}` }, title: `Add ${esc(AD.name)}`, action: 'Add it', sub: 'A domain changes where the household’s apps answer, so HomersLab asks for your passkey.', what: [{ icon: 'globe', text: `${esc(AD.name)}, with its token at ${esc(AD.dns)}<small>A wildcard certificate; every app answers here; sign-in stays on auth.${esc(main().name)}</small>` }] });
    if (!ok) return; S.approvals.push(ok);
    const name = AD.name;
    A.run4('dom-' + name, [{ t: 'Checked the token' }, { t: `Wrote _acme-challenge.${name}`, s: 'With its owner marker' }, { t: `Issued *.${name}`, s: 'Let’s Encrypt, until 7 Feb 2027' }, { t: 'Removed the challenge record' }, { t: 'Handed the certificate to every node’s edge' }, { t: `Updated ${S.installed.length} sign-in clients`, s: `Sign-in stays on auth.${main().name}` }, { t: `Every app answers on ${name}` }],
      { icon: 'globe', title: `Adding ${name}`, done: `${name} is added`, back: '#/settings/domains', backLabel: 'See the domains', plan: UI.planId({ k: 'adddom', d: name }), approved: ok, here: '#/settings/domains/add' }, () => {
        DOM.push({ name, main: false, dns: AD.dns, token: `Held on Study PC since today`, cert: { name: `*.${name}`, issuer: 'Let’s Encrypt', until: '7 Feb 2027' }, added: 'Today', records: [{ name: '_acme-challenge', type: 'TXT', value: 'Written for each renewal, removed after', owner: 'homerslab' }, { name: '@', type: 'MX', value: 'Someone else’s mail', owner: 'other' }] });
        toast(`${name} is added. Every app answers there now.`);
      });
    AD.step = 'run'; log(`add domain ${name}`); render();
  });
  // leaving the flow after it finished starts it fresh next time
  addEventListener('hashchange', () => { if (AD.step === 'run' && !location.hash.startsWith('#/settings/domains/add') && S.r4.jobs['dom-' + AD.name]?.done) { delete S.r4.jobs['dom-' + AD.name]; S.addDom = { step: 'name', name: '', dns: null }; Object.assign(AD, S.addDom); } });

  // ---------- rename the main domain: the migration's plan ----------
  route(/^#\/settings\/domains\/rename$/, () => {
    const m = main(), to = DOM.find((d) => !d.main), ready = !to.cert.failing || to.fixed;
    const people = S.people;
    return `<div class="page-head"><div><h1>Rename the main domain</h1><p class="page-sub">From <span class="host">${esc(m.name)}</span> to <span class="host">${esc(to.name)}</span>. A guided migration: rehearsed first, and it can be rolled back.</p></div></div>
      ${ready ? '' : `<div class="notice notice-warn a-finding" role="status">${I('lock')}<span class="notice-body"><b>Fix ${esc(to.name)}’s certificate first.</b>Its renewal is failing: ${esc(to.cert.failing)}.</span><span class="notice-actions"><button class="btn btn-sm" data-act="dom-token" data-d="${esc(to.name)}">Replace the token</button></span></div>`}
      <div class="split"><div class="plan-sections">
        <section><div class="plan-q"><h2>What happens</h2><span>About 20 minutes; nobody is signed out</span></div><ol class="group stepup-list plan-steps">
          ${[['lock', `The sign-in moves to auth.${to.name}`, 'With its certificate; the old address keeps working'], ['apps', `Every app’s sign-in client is updated`, `${S.installed.length} clients, and every guard`], ['globe', `${m.name} keeps answering for 90 days`, 'Until 7 Feb 2027, then it stops; links and bookmarks keep working until then'], ['user', 'Everyone adds a passkey on the new address', 'Passkeys are bound to the name; Home asks each person once, with one tap'], ['rollback', 'It can be rolled back for 90 days', 'Back to the old main domain, in one action']].map(([i, t, s]) => `<li>${I(i)}<span>${esc(t)}<small>${esc(s)}</small></span></li>`).join('')}</ol></section>
        <section><div class="plan-q"><h2>Passkeys on the new address</h2><span>0 of ${people.length} so far</span></div><div class="group lead-sm">${people.map((p) => `<div class="row"><span class="row-lead">${A.avatar(p, 'avatar-sm')}</span><span class="row-main"><span class="row-title">${esc(p.name)}</span><span class="row-sub">${p.role === 'kid' && !p.mail ? 'Added with an adult, on the living room’s key' : 'Asked on Home after the move'}</span></span><span class="pill">Not yet</span></div>`).join('')}</div></section></div>
        <aside class="rail"><div class="card stack" style="gap:12px"><h2 class="card-title">First, a rehearsal</h2><p class="muted">It runs the whole migration on copies of the sign-in and the apps’ settings, and reports what would change. Nothing is touched.</p>
          <button class="btn btn-block" data-act="dom-rehearse" ${ready ? '' : 'disabled'}>Rehearse it</button><button class="btn btn-primary btn-block" data-act="dom-rename" ${ready && S.rehearsed ? '' : 'disabled'}>${I('key')}Start with your passkey</button><p class="subhead">${S.rehearsed ? 'Rehearsed at ' + esc(S.rehearsed) + ': 41 of 41 checks passed.' : 'Start opens after a rehearsal passes.'}</p></div></aside></div>`;
  }, { back: { href: '#/settings/domains', label: 'Domains' } });
  act('dom-rehearse', () => { S.rehearsed = hhmm(now()); toast('Rehearsed: 41 of 41 checks passed. Nothing was changed.', 'check'); render(); });
  act('dom-rename', async () => { const to = DOM.find((d) => !d.main); const ok = await stepUp({ plan: { id: UI.planId({ k: 'rename', to: to.name }), summary: `make ${to.name} the main domain` }, title: `Make ${esc(to.name)} the main domain`, action: 'Start', sub: 'This moves the household’s sign-in, so HomersLab asks for your passkey.', what: [{ icon: 'globe', text: `Sign-in moves to auth.${esc(to.name)}<small>${esc(main().name)} answers for 90 days; everyone adds a passkey</small>` }] }); if (ok) { S.approvals.push(ok); toast('Prototype: the migration would run now, with its own progress.', 'info'); } });
})();
