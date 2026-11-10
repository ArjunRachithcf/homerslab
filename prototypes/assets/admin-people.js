// HomersLab Admin prototype: people, their devices, and invites. PROTOTYPE, throwaway.
// Risky (the passkey): roles, sign-in settings (sign-up, a passkey reset), removing a person, inviting an admin.
// Not risky: revoking a device, inviting an adult or a kid.
(() => {
  const { D, S, cat, inst, node, person, host, and, inAud, appIcon, avatar, ROLE, DEVICE_ICON, chev, route, render, go, soon, act, log } = A;
  const { esc, toast, dialog, stepUp, hhmm, now } = UI;
  const MESHNAME = { netbird: 'NetBird', tailscale: 'Tailscale' };
  const devicesOf = (pid) => S.devices.filter((d) => d.person === pid);
  const appsFor = (p) => S.installed.filter((a) => inAud(a.audience, p)).map((a) => cat(a.id));
  const ROLE_WORDS = {
    adult: 'Opens the apps for adults. Adds passkeys and devices of their own.',
    kid: 'Sees only apps for kids, in their kid views. Nothing to set up by hand.',
    admin: 'Everything an adult can, plus Admin: apps, nodes, people and backups.',
  };
  const sec = (id, title, words, body) => `<section class="sec" aria-labelledby="h-${id}"><div class="sec-head"><h2 id="h-${id}">${title}</h2><p>${words}</p></div><div class="sec-body">${body}</div></section>`;

  // ---------- the people ----------
  route(/^#\/people$/, () => {
    const P = S.people, devs = S.devices.length;
    return `
      <div class="page-head"><div><h1>People</h1><p class="page-sub">${P.length} people and ${devs} devices in the household. Sign-up is ${S.signup === 'invite' ? 'by invite only' : S.signup === 'domains' ? `open to addresses at ${esc(D.household.signupDomains.join(', '))}` : 'off'}.</p></div>
        <div class="page-actions"><a class="btn btn-primary" href="#/people/invite">${I('plus')}Invite someone</a></div></div>
      <div class="group"><table class="rtable has-lead" style="--lead:32px"><thead><tr><th>Person</th><th>Role</th><th class="n">Devices</th><th class="n">Passkeys</th><th>Last seen</th><th class="chev-cell"><span class="sr-only">Open</span></th></tr></thead><tbody>
      ${P.map((p) => { const ds = devicesOf(p.id); return `<tr class="link-row" data-act="row" data-href="#/people/${p.id}">
        <td><div class="ident">${avatar(p)}<div class="names"><a href="#/people/${p.id}"><b>${esc(p.name)}</b></a><span>${esc(p.mail || 'No mail address')}${p.age ? ` · ${p.age}` : ''}</span></div></div></td>
        <td class="end" style="text-align:left">${p.role === 'admin' ? '<span class="pill">Admin</span>' : `<span class="muted-cell">${ROLE[p.role]}</span>`}</td>
        <td class="hide-sm n">${ds.length ? `<span class="cell-state" style="justify-content:flex-end">${ds.map((d) => I(DEVICE_ICON[d.kind], 'icon icon-sm')).join('')}<span>${ds.length}</span></span>` : '<span class="muted-cell">None</span>'}</td>
        <td class="hide-sm n">${p.passkeys.length}</td>
        <td class="hide-sm">${esc(p.lastSeen)}</td>
        <td class="line2">${ds.length} ${ds.length === 1 ? 'device' : 'devices'} · ${p.passkeys.length} ${p.passkeys.length === 1 ? 'passkey' : 'passkeys'} · ${esc(p.lastSeen)}</td>
        <td class="chev-cell">${chev()}</td></tr>`; }).join('')}
      </tbody></table></div>
      <div class="grid a-block" style="margin-top:40px">
        <section class="span-6" aria-labelledby="h-inv"><div class="group-head"><h2 id="h-inv">Invites waiting</h2><span>${S.invites.length}</span></div>
          <div class="group">${S.invites.length ? S.invites.map((v) => `<div class="row is-top row-wrap" data-row="${v.id}"><span class="row-main"><span class="row-title">${esc(v.name)} · ${ROLE[v.role]}</span><span class="row-sub wrap">Sent ${esc(v.sent)} by mail to ${esc(v.mail)} · works until ${esc(v.expires)}</span></span><span class="btn-row"><button class="btn btn-sm" data-act="inv-resend" data-id="${v.id}">Send again</button><button class="btn btn-sm btn-plain" data-act="inv-cancel" data-id="${v.id}">Cancel</button></span></div>`).join('') : `<div class="empty">${I('mail')}<p class="empty-title">No invites waiting</p><p class="empty-text">An invite you send waits here until it is accepted.</p></div>`}
            <a class="row row-link row-action" href="#/people/invite"><span class="row-main"><span class="row-label">Invite someone</span></span>${chev()}</a></div>
          <p class="group-foot">An invite works once, for 7 days, and can only make the role it names.</p></section>
        <section class="span-6" aria-labelledby="h-signup"><div class="group-head"><h2 id="h-signup">Sign-up</h2><span class="needs-key">${I('key')}Your passkey</span></div>
          <div class="group" role="radiogroup" aria-labelledby="h-signup">${[['invite', 'By invite only', 'Nobody joins unless you invite them'], ['domains', `Anyone at ${D.household.signupDomains.join(', ')}`, 'They join as adults; you get a mail'], ['off', 'Off', 'Not even invites work until you turn it back on']].map(([v, l, h]) => `
            <label class="row row-choice"><input type="radio" name="signup" value="${v}" data-change="signup" ${S.signup === v ? 'checked' : ''}><span class="row-main"><span class="row-label">${esc(l)}</span><span class="row-sub wrap">${esc(h)}</span></span><span class="row-trail">${I('check2', 'icon checkmark')}</span></label>`).join('')}</div>
          <p class="group-foot">Who can make a household account without an invite. A sign-in setting, so a change asks for your passkey.</p></section>
      </div>`;
  });
  act('signup', async (t) => {
    const to = t.value, from = S.signup;
    const ok = await stepUp({ plan: { id: UI.planId({ k: 'signup', to }), summary: 'change who can sign up' }, title: 'Change sign-up', action: 'Change it', sub: 'Sign-up is a sign-in setting, so HomersLab asks for your passkey.', what: [{ icon: 'people', text: to === 'invite' ? 'Only invited people can join' : to === 'domains' ? `Anyone with an address at ${esc(D.household.signupDomains.join(', '))} can join, as an adult` : 'Nobody can join, invites included' }] });
    if (!ok) { S.signup = from; render(); return; }
    S.approvals.push(ok); S.signup = to; log(`sign-up: ${to}`); toast('Sign-up changed. Approved with your passkey.', 'key'); render();
  });
  act('inv-resend', (t) => { const v = S.invites.find((x) => x.id === t.dataset.id); v.sent = UI.dayMonth(now()); toast(`Sent again to ${v.mail}. The old link no longer works.`, 'mail'); render(); });
  act('inv-cancel', (t) => { const v = S.invites.find((x) => x.id === t.dataset.id); S.invites = S.invites.filter((x) => x !== v); log(`invite ${v.name} cancelled`); toast(`The invite for ${v.name} is cancelled; its link no longer works.`); A.renderList(); });

  // ---------- one person ----------
  route(/^#\/people\/(?!invite$)([\w-]+)$/, (m) => {
    const p = person(m[1]); if (!p) return `<div class="page-head"><div><h1>Not in the household</h1></div></div>`;
    const ds = devicesOf(p.id), apps = appsFor(p), me = p.id === S.people[0].id;
    return `
      <div class="page-head"><div class="a-ident">${avatar(p, 'avatar-xl')}<div><div class="a-h1-line"><h1>${esc(p.name)}</h1><span class="pill">${ROLE[p.role]}${p.age ? `, ${p.age}` : ''}</span></div>
        <p class="page-sub">${esc(p.mail || 'No mail address')} · joined ${esc(new Date(p.joined).getDate() + ' ' + UI.MONTHS[new Date(p.joined).getMonth()])} · last seen ${esc(p.lastSeen.toLowerCase())}</p></div></div>
        <div class="page-actions"><button class="btn" data-act="reset-passkey" data-id="${p.id}">${I('key')}Reset passkeys…</button></div></div>
      ${sec('dev', 'Devices', `Phones, tablets and computers on the mesh. Revoking one cuts it off at once and needs no passkey; ${esc(p.short)} can add it again with an invite to the mesh.`,
        `<div class="group lead-sm">${ds.length ? ds.map((d) => `<div class="row" data-row="dev-${d.id}"><span class="row-lead">${I(DEVICE_ICON[d.kind], 'icon icon-md')}</span><span class="row-main"><span class="row-title">${esc(d.name)}${d.current ? ' · this device' : ''}</span><span class="row-sub">${esc(d.model)} · ${MESHNAME[d.mesh]} · <span class="num">${esc(d.addr)}</span> · seen ${esc(d.seen.toLowerCase())}</span></span>${d.current ? '<span class="row-value">You are using it</span>' : `<button class="btn btn-sm" data-act="revoke" data-id="${d.id}">Revoke</button>`}</div>`).join('') : `<div class="row"><span class="row-main"><span class="row-label wrap">${esc(p.short)} has no devices of their own${p.role === 'kid' ? ': signs in on the living-room TV with a security key' : ''}.</span></span></div>`}
          <button type="button" class="row row-link row-action" data-act="device-invite" data-id="${p.id}"><span class="row-lead">${I('plus', 'icon icon-md')}</span><span class="row-main"><span class="row-label">Add a device for ${esc(p.short)}</span></span></button></div>`)}
      ${sec('pk', 'Passkeys', `${esc(p.short)} adds passkeys for new devices without you. If they lose all of them, reset them: they get a way to make a new one. A reset asks for your passkey.`,
        `<div class="group lead-sm">${p.passkeys.map((k) => `<div class="row"><span class="row-lead">${I('key', 'icon icon-md')}</span><span class="row-main"><span class="row-title">${esc(k.name)}</span><span class="row-sub">Added ${esc(k.added)}</span></span></div>`).join('')}</div>`)}
      ${sec('apps', `What ${esc(p.short)} sees on Home`, `From each app’s audience${p.role === 'kid' ? ', in their kid views' : ''}. Change an app’s audience on its page.`,
        `<div class="group"><div class="mini-tiles">${apps.map((d) => `<a class="mini-tile" href="#/apps/${d.id}">${appIcon(d)}<span>${esc(d.name)}${p.role === 'kid' && d.kidView ? '<small>kid view</small>' : ''}</span></a>`).join('') || '<p class="muted">Nothing yet.</p>'}</div></div>`)}
      ${sec('role', 'Role', 'A role decides what someone may do, so changing one asks for your passkey.',
        `<div class="group" role="radiogroup" aria-labelledby="h-role">${['adult', 'kid', 'admin'].map((r) => `<label class="row row-choice"><input type="radio" name="role-${p.id}" value="${r}" data-change="role" data-id="${p.id}" ${p.role === r ? 'checked' : ''} ${me ? 'disabled' : ''}><span class="row-main"><span class="row-title">${ROLE[r]}</span><span class="row-sub wrap">${esc(ROLE_WORDS[r])}</span></span><span class="row-trail">${I('check2', 'icon checkmark')}</span></label>`).join('')}</div>
          ${me ? '<p class="group-foot">You can’t change your own role; another admin can.</p>' : ''}`)}
      ${sec('rm', 'Remove from the household', `Revokes ${esc(UI.poss(p.short))} household account and every device together. Their files in apps stay with the apps.`,
        `<div class="group"><div class="row is-top row-wrap"><span class="row-main"><span class="row-title">Remove ${esc(p.name)}</span><span class="row-sub wrap">${ds.length} ${ds.length === 1 ? 'device' : 'devices'} and ${p.passkeys.length} ${p.passkeys.length === 1 ? 'passkey' : 'passkeys'} stop working at once. Your passkey approves it.</span></span>${me ? '<span class="row-value">Not yourself</span>' : `<button class="btn btn-sm btn-destructive" data-act="remove-person" data-id="${p.id}">Remove…</button>`}</div></div>`)}`;
  }, { back: { href: '#/people', label: 'People' } });
  act('revoke', async (t) => {
    const d = S.devices.find((x) => x.id === t.dataset.id), p = person(d.person);
    const v = await dialog({ title: `Revoke ${esc(d.name)}?`, sub: 'Cutting a device off is never risky, so it needs no passkey.', body: A.list([['x', `It leaves ${MESHNAME[d.mesh]} at once and can’t reach any app`], ['x', 'Its sessions end; a passkey stored on it no longer signs in'], ['info', `${esc(p.short)} keeps their household account and other devices`]]), actions: [{ label: 'Cancel', value: 'no', kind: 'ghost' }, { label: 'Revoke it', value: 'yes', kind: 'primary' }] });
    if (v !== 'yes') return;
    S.devices = S.devices.filter((x) => x !== d); log(`revoked ${d.id}`); toast(`${d.name} was cut off at ${hhmm(now())}.`); A.renderList();
  });
  act('device-invite', (t) => { const p = person(t.dataset.id); toast(`${p.short} gets a one-time way onto the mesh for the new device${p.mail ? ', by mail' : ''}.`, 'mail'); });
  act('reset-passkey', async (t) => {
    const p = person(t.dataset.id);
    const ok = await stepUp({ plan: { id: UI.planId({ k: 'reset', p: p.id }), summary: `reset ${UI.poss(p.short)} passkeys` }, title: `Reset ${esc(UI.poss(p.short))} passkeys`, action: 'Reset them', sub: 'A passkey reset is a sign-in setting, so HomersLab asks for your passkey.', what: [{ icon: 'key', text: `${esc(UI.poss(p.short))} ${p.passkeys.length} ${p.passkeys.length === 1 ? 'passkey stops' : 'passkeys stop'} working` }, { icon: 'mail', text: p.mail ? `A one-time link goes to ${esc(p.mail)} to make a new one` : 'You get a one-time code to hand over in person' }] });
    if (!ok) return; S.approvals.push(ok); log(`passkeys reset for ${p.id}`); toast(`${UI.poss(p.short)} passkeys are reset.`, 'key'); render();
  });
  act('role', async (t) => {
    const p = person(t.dataset.id), to = t.value, from = p.role;
    const what = [{ icon: 'people', text: `${esc(p.name)}: ${ROLE[from]} → <b>${ROLE[to]}</b>` }];
    if (to === 'admin') what.push({ icon: 'shield', text: 'They can open Admin, install apps, add nodes and people, and restore backups' });
    if (to === 'kid') what.push({ icon: 'eye', text: 'They see only apps for kids, in their kid views' });
    const ok = await stepUp({ plan: { id: UI.planId({ k: 'role', p: p.id, to }), summary: `change ${UI.poss(p.short)} role` }, title: 'Change a role', action: 'Change it', sub: 'A role is a risky change, so HomersLab asks for your passkey.', what });
    if (!ok) { render(); return; }
    S.approvals.push(ok); p.role = to; log(`${p.id} role ${to}`); toast(`${p.short} is now ${to === 'admin' ? 'an admin' : to === 'adult' ? 'an adult' : 'a kid'}.`, 'key'); render();
  });
  act('remove-person', async (t) => {
    const p = person(t.dataset.id), ds = devicesOf(p.id);
    const ok = await stepUp({ plan: { id: UI.planId({ k: 'rmperson', p: p.id }), summary: `remove ${p.short} from the household` }, title: `Remove ${esc(p.name)}`, action: 'Remove them', sub: 'Removing someone takes away their role and their sign-in, so HomersLab asks for your passkey.', what: [{ icon: 'x', text: 'Their household account is revoked' }, { icon: 'x', text: `${ds.length} ${ds.length === 1 ? 'device leaves' : 'devices leave'} the mesh` }] });
    if (!ok) return; S.approvals.push(ok); S.people = S.people.filter((x) => x !== p); S.devices = S.devices.filter((d) => d.person !== p.id); log(`removed person ${p.id}`); toast(`${p.name} is no longer in the household.`); go('#/people');
  });

  // ---------- invite someone: an adult or a kid needs no passkey; an admin is a role, so it does ----------
  const inv = { name: '', mail: '', role: 'adult', how: 'mail' };
  route(/^#\/people\/invite$/, () => {
    const role = inv.role, fake = { role, id: '_new' };
    const sees = S.installed.filter((a) => inAud(a.audience, fake)).map((a) => cat(a.id));
    const first = inv.name.trim().split(/\s+/)[0] || '';
    return `
      <div class="page-head"><div><h1>Invite someone</h1><p class="page-sub">They get a household account with a passkey, and step-by-step help getting their phone and laptop onto the mesh.</p></div></div>
      <div class="split"><form class="card stack" id="inv-form" novalidate aria-label="The invite" style="gap:22px">
        <div class="fields fields-2"><div class="field"><label class="field-label" for="inv-name">Name</label><input id="inv-name" class="input" value="${esc(inv.name)}" autocomplete="off" data-inv="name"></div>
          <div class="field"><label class="field-label" for="inv-mail">Mail address</label><input id="inv-mail" class="input" type="email" value="${esc(inv.mail)}" autocomplete="off" data-inv="mail" aria-describedby="inv-mail-hint"><span class="hint" id="inv-mail-hint">Kids without one get a link instead.</span></div></div>
        <fieldset class="field" style="border:0;padding:0;margin:0"><legend class="field-label" style="margin-bottom:8px">Role</legend><div class="choices" style="--n:3">${['adult', 'kid', 'admin'].map((r) => `<label class="choice"><input type="radio" name="inv-role" value="${r}" data-change="inv-role" ${role === r ? 'checked' : ''}><span class="choice-head"><span class="choice-title">${ROLE[r]}</span><span class="choice-mark">${I('check2')}</span></span><span class="subhead">${esc(ROLE_WORDS[r])}</span>${r === 'admin' ? `<span><span class="needs-key">${I('key')}Your passkey</span></span>` : ''}</label>`).join('')}</div></fieldset>
        <div class="field"><span class="field-label" id="inv-how">How it reaches them</span><div class="segmented" style="--n:2" role="radiogroup" aria-labelledby="inv-how" data-seg="inv-how"><span class="segmented-thumb" aria-hidden="true"></span><button type="button" role="radio" data-act="inv-how" data-how="mail" aria-checked="${inv.how === 'mail'}">${I('mail', 'icon icon-sm')}By mail</button><button type="button" role="radio" data-act="inv-how" data-how="link" aria-checked="${inv.how === 'link'}">${I('qr', 'icon icon-sm')}A link and a QR code</button></div>
          <span class="hint">The invite works once, for 7 days. It can only make ${role === 'kid' ? 'a kid’s' : role === 'admin' ? 'an admin’s' : 'an adult’s'} account.</span></div>
        <div class="btn-row"><button type="button" class="btn btn-primary btn-lg" data-act="inv-send">${role === 'admin' ? `${I('key')}Send with your passkey` : inv.how === 'mail' ? 'Send the invite' : 'Make the link'}</button><a class="btn btn-plain btn-lg" href="#/people">Cancel</a></div>
        ${role === 'admin' ? '' : `<p class="subhead">Inviting an adult or a kid needs no passkey.</p>`}
      </form>
      <aside class="rail" aria-label="What they get">
        <section class="card mail-preview" aria-label="The mail they get"><div class="mp-head"><span>From ${esc(D.household.mailFrom)}</span><span>To ${esc(inv.mail || 'their address')}</span><b>Join ${esc(D.household.name.replace(/^The /, 'the '))}</b></div>
          <div class="mp-body"><p>Hello${first ? ` ${esc(first)}` : ''},</p><p>Inês invited you to the household’s apps. Make your household account with a passkey; it takes a minute, and then your phone and laptop join the private network step by step.</p><span class="btn btn-primary btn-sm" aria-hidden="true">Accept the invite</span><p class="subhead">The link works once, until ${esc(UI.dayMonth(new Date(now().getTime() + 7 * 864e5)))}.</p></div></section>
        <section class="card" aria-labelledby="h-sees" style="padding-bottom:8px"><div class="card-head"><h2 class="card-title" id="h-sees">What ${esc(first || 'they')} will see on Home</h2></div>
          <div class="mini-tiles" style="padding:0 0 8px">${sees.map((d) => `<span class="mini-tile">${appIcon(d)}<span>${esc(d.name)}${role === 'kid' && d.kidView ? '<small>kid view</small>' : ''}</span></span>`).join('') || '<p class="muted">Nothing yet: no app has kids in its audience.</p>'}</div>
          <p class="subhead" style="padding-bottom:12px">From each app’s audience, as ${role === 'adult' ? 'an adult' : role === 'kid' ? 'a kid' : 'an admin'}.</p></section>
      </aside></div>`;
  }, { back: { href: '#/people', label: 'People' }, after: () => { document.querySelectorAll('[data-inv]').forEach((el) => el.addEventListener('input', () => { inv[el.dataset.inv] = el.value; const keep = el.id, pos = el.selectionStart; A.render(); const n = document.getElementById(keep); n?.focus(); try { n?.setSelectionRange(pos, pos); } catch {} })); } });
  act('inv-role', (t) => { inv.role = t.value; soon(); });
  act('inv-how', (t) => { inv.how = t.dataset.how; render(); });
  act('inv-send', async () => {
    if (!inv.name.trim()) { toast('Give them a name first.', 'info'); document.getElementById('inv-name')?.focus(); return; }
    if (inv.how === 'mail' && !/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(inv.mail)) { toast('That mail address doesn’t look right.', 'info'); document.getElementById('inv-mail')?.focus(); return; }
    if (inv.role === 'admin') {
      const ok = await stepUp({ plan: { id: UI.planId({ k: 'invite', ...inv }), summary: `invite ${inv.name} as an admin` }, title: 'Invite an admin', action: 'Send it', sub: 'The admin role is a risky change, so HomersLab asks for your passkey.', what: [{ icon: 'shield', text: `${esc(inv.name)} gets the admin role: Admin, apps, nodes, people and backups` }] });
      if (!ok) return; S.approvals.push(ok);
    }
    const v = { id: 'inv-' + Date.now(), name: inv.name.trim(), mail: inv.mail || '(by link)', role: inv.role, sent: UI.dayMonth(now()), expires: UI.dayMonth(new Date(now().getTime() + 7 * 864e5)), by: 'ines', how: inv.how };
    S.invites.unshift(v); log(`invited ${v.name} (${v.role})`);
    if (inv.how === 'link') {
      await dialog({ title: `The invite for ${esc(v.name)}`, sub: 'Show them the code, or send them the link yourself. It works once, for 7 days.', body: `<div class="cluster" style="gap:16px;align-items:flex-start">${qr()}<div class="stack" style="gap:8px;min-width:0;flex:1"><div class="cmd"><code>https://${esc(D.household.homeUrl)}/join#k=Q7XM-4KRT-9PWD</code><button class="btn btn-icon btn-sm" data-act="copy" aria-label="Copy the link">${I('copy')}</button></div><p class="subhead">A simulated QR code: it encodes nothing.</p></div></div>`, actions: [{ label: 'Done', value: 'ok', kind: 'primary' }] });
    } else toast(`Invite sent to ${v.mail}.`, 'mail');
    Object.assign(inv, { name: '', mail: '', role: 'adult', how: 'mail' }); go('#/people');
  });
  function qr(seed = 7) {
    let cells = '', s = seed;
    const finder = (x, y) => { for (const [ox, oy] of [[0, 0], [18, 0], [0, 18]]) { const dx = x - ox, dy = y - oy; if (dx >= 0 && dx < 7 && dy >= 0 && dy < 7) return dx === 0 || dx === 6 || dy === 0 || dy === 6 || (dx >= 2 && dx <= 4 && dy >= 2 && dy <= 4) ? 1 : 0; if (dx >= -1 && dx <= 7 && dy >= -1 && dy <= 7) return 0; } return null; };
    for (let y = 0; y < 25; y++) for (let x = 0; x < 25; x++) { s = (s * 1103515245 + 12345) & 0x7fffffff; const f = finder(x, y); if (f === null ? (s >> 9) % 2 === 0 : f === 1) cells += `<rect x="${x}" y="${y}" width="1.02" height="1.02"/>`; }
    return `<svg class="qr" viewBox="0 0 25 25" role="img" aria-label="QR code (simulated)">${cells}</svg>`;
  }
  A.qr = qr;
})();
