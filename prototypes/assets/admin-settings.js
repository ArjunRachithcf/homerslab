// HomersLab Admin prototype: the household's settings, as first-time setup left them. PROTOTYPE, throwaway.
// Sign-in settings are risky (the passkey). The prototype also asks for it to replace a mesh token and to make a new
// recovery kit: an open question for the maintainer (both can hand the household to someone else).
(() => {
  const { D, S, mesh, meshWords, route, render, act, log } = A;
  const { esc, toast, stepUp } = UI;
  const assistants = [{ id: 'mcp-1', name: 'Claude, in Inês’s terminal', scope: 'Plan installs, explain plans, check health, read logs', used: 'Today 13:20', made: '20 Oct' }];
  const sec = (id, title, words, body) => `<section class="sec" aria-labelledby="h-${id}"><div class="sec-head"><h2 id="h-${id}">${title}</h2><p>${words}</p></div><div class="sec-body">${body}</div></section>`;
  const kv = (k, v) => `<div class="row is-top row-wrap"><span class="row-main"><span class="row-label">${k}</span><span class="row-sub wrap">${v}</span></span></div>`;

  const appearance = () => { try { return localStorage.getItem('hl-admin-theme') || 'dark'; } catch { return 'dark'; } };
  // the glass level: live as it changes, kept in this browser
  document.addEventListener('hl:glass', (e) => { try { localStorage.setItem('hl-admin-glass', String(e.detail)); } catch {} if (Proto.get('glass', '')) Proto.set('glass', null, ''); });
  act('ap-theme', (t) => {
    try { if (t.dataset.v === 'dark') localStorage.removeItem('hl-admin-theme'); else localStorage.setItem('hl-admin-theme', t.dataset.v); } catch {}
    log(`appearance: ${t.dataset.v}`);
    const q = new URLSearchParams(location.search).get('theme');
    HL.run(() => { if (q !== 'light' && q !== 'dark') { const d = window.HL_THEME_DEFAULT(); if (d) document.documentElement.dataset.theme = d; else delete document.documentElement.dataset.theme; } render(); }, { type: 'fade' });
  });
  route(/^#\/settings$/, () => {
    const m = mesh();
    return `
      <div class="page-head"><div><h1>Settings</h1><p class="page-sub">What first-time setup chose on 2 March. Anything here that touches sign-in, the meshes or recovery asks for your passkey.</p></div></div>
      ${sec('ap', 'Appearance', 'Admin opens dark on every device, in Home’s dark look. Your choice here is kept in this browser only.',
        `<div class="group"><div class="row row-wrap"><span class="row-main"><span class="row-label" id="ap-l">Appearance</span><span class="row-sub wrap">${appearance() === 'system' ? 'Follows this device’s setting' : appearance() === 'light' ? 'Light, whatever this device says' : 'Dark, whatever this device says'}</span></span>
          <div class="segmented" style="--n:3" role="radiogroup" aria-labelledby="ap-l" data-seg="ap"><span class="segmented-thumb" aria-hidden="true"></span>${[['dark', 'Dark'], ['light', 'Light'], ['system', 'System']].map(([v, l]) => `<button type="button" role="radio" aria-checked="${appearance() === v}" data-act="ap-theme" data-v="${v}">${l}</button>`).join('')}</div></div>
          <div class="row is-top a-glass-row"><span class="row-main"><span class="row-label">Glass</span><span class="row-sub wrap">How see-through the bars, the sidebar, panels and sheets are. Words stay legible at every level; Solid is the same as your device’s reduced transparency.</span></span><div class="a-glass-ctl">${HL.glassControl('ad-glass')}</div></div></div>`)}
      ${sec('hh', 'The household', 'Every app is named under its domain, on the mesh.',
        `<div class="group">${kv('Name', esc(D.household.name))}<a class="row row-link is-top" href="#/settings/domains"><span class="row-main"><span class="row-label">Domains</span><span class="row-sub wrap"><span class="host">${esc(D.household.domain)}</span>, the main domain, and <span class="host">ferreira.pt</span>: every app answers on both; sign-in on auth.${esc(D.household.domain)}</span></span>${I('chevronRight', 'icon row-chev')}</a>${kv('Mail', `${esc(D.household.mailFrom)}, for invites, passkey resets and alerts`)}</div>`)}
      ${sec('alerts', 'Alerts and notifications', 'Yours: which channels each level reaches, quiet hours, the morning digest, and the household’s maintenance window.',
        `<div class="group"><a class="row row-link is-top" href="#/settings/alerts"><span class="row-main"><span class="row-label">Alerts and notifications</span><span class="row-sub wrap">Critical by mail, ntfy and the desktop app · quiet 22:00–07:00 · digest at 07:00 · window 02:00–05:00</span></span>${I('chevronRight', 'icon row-chev')}</a></div>`)}
      ${sec('si', 'Sign-in', 'The household account comes from one identity provider. People are managed here, never in its own console.',
        `<div class="group lead-md"><div class="row is-top row-wrap"><span class="row-lead"><img class="app-icon app-icon-md" src="icons/apps/authentik.svg" alt=""></span><span class="row-main"><span class="row-title">${esc(D.household.idp.name)} ${esc(D.household.idp.version)}</span><span class="row-sub wrap">On Study PC · passkeys for everyone · OIDC, LDAP for TV apps, and guarded apps at the edge</span></span><span class="row-value">Chosen at setup</span></div></div>`)}
      ${sec('me', 'Meshes', 'Every node joins every mesh in use; people’s devices join either. Installing an app never needs a visit to a mesh’s own site.',
        `<div class="group">${m.names.map((nm) => `<div class="row is-top row-wrap"><span class="row-main"><span class="row-title">${esc(nm)}</span><span class="row-sub wrap">${nm === 'NetBird' ? 'NetBird’s cloud · its token is replaced by itself before it lapses, next on 2 Mar 2027 · public apps through its public path' : nm === 'Tailscale' ? 'Tailscale’s cloud · tailnet tail8f3c.ts.net · an OAuth client HomersLab made from a one-time token' : 'Your own Headscale, off-site · no public path'}</span></span><button class="btn btn-sm" data-act="mesh-token" data-m="${esc(nm)}">${I('key')}Replace its token</button></div>`).join('')}</div>`)}
      ${sec('ad', 'Admin', 'Reachable over the mesh only, never from the internet or the home network.',
        `<div class="group">${kv('Address', `<span class="host">https://${esc(D.household.adminUrl)}</span>, on ${esc(meshWords())}`)}${kv('Home network', 'Its setup listener closed for good at your first sign-in over the mesh, 2 Mar')}${kv('Home', `<span class="host">https://${esc(D.household.homeUrl)}</span>, public, behind the household account; it can’t reach anything in Admin`)}</div>`)}
      ${sec('rk', 'Recovery kit', 'Enough to restore everything on new hardware, even without HomersLab. You keep it away from the machines.',
        `<div class="group"><div class="row is-top row-wrap"><span class="row-main"><span class="row-title">Made 2 March: printed, and in Inês’s password manager</span><span class="row-sub wrap">Archive key <span class="mono">${esc(D.household.recoveryKey)}</span> (the public half) · backup password · targets · the one-page how-to</span></span><button class="btn btn-sm" data-act="new-kit">${I('key')}Make a new kit</button></div></div>
          <p class="group-foot">A kit is made in your browser, never on the primary: HomersLab refuses when the browser runs on Study PC itself.</p>`)}
      ${sec('as', 'Assistants', 'An assistant uses the same API through a scoped token. It can plan and read, never make a risky change, and is never shown a secret.',
        `<div class="group">${assistants.map((x) => `<div class="row is-top row-wrap" data-row="mcp-${x.id}"><span class="row-main"><span class="row-title">${esc(x.name)}</span><span class="row-sub wrap">${esc(x.scope)} · made ${esc(x.made)} · last used ${esc(x.used.toLowerCase())}</span></span><button class="btn btn-sm" data-act="mcp-revoke" data-id="${x.id}">Revoke</button></div>`).join('') || `<div class="row"><span class="row-main"><span class="row-label">No assistants</span></span></div>`}
          <button type="button" class="row row-link row-action" data-act="mcp-add"><span class="row-main"><span class="row-label">Add an assistant</span></span></button></div>`)}
      ${sec('hl', 'HomersLab', 'Updates itself the way the apps do: signed releases, a snapshot first, a rollback if unhealthy.',
        `<div class="group"><div class="row"><span class="row-main"><span class="row-title">HomersLab ${esc(D.household.version)}</span><span class="row-sub">Up to date · checked 13:40 · no telemetry, ever</span></span></div></div>`)}`;
  });
  act('mesh-token', async (t) => {
    const ok = await stepUp({ plan: { id: UI.planId({ k: 'token', m: t.dataset.m }), summary: `replace ${UI.poss(t.dataset.m)} token` }, title: `Replace ${esc(UI.poss(t.dataset.m))} token`, action: 'Continue', sub: 'A mesh token decides which network the household’s machines join, so the prototype asks for your passkey.', what: [{ icon: 'mesh', text: `You paste a new token for ${esc(t.dataset.m)}; the old one stops being used` }] });
    if (ok) { S.approvals.push(ok); toast('Prototype: a field for the new token would open here. Tokens only travel inward; none is ever shown back.', 'info'); }
  });
  act('new-kit', async () => {
    const ok = await stepUp({ plan: { id: UI.planId({ k: 'kit' }), summary: 'make a new recovery kit' }, title: 'Make a new recovery kit', action: 'Make it', sub: 'A new kit changes who can read the archive, so the prototype asks for your passkey.', what: [{ icon: 'key', text: 'A new archive key, made in this browser; next Sunday’s archive uses it' }, { icon: 'info', text: 'Older archives still need the old kit: keep it until they age out' }] });
    if (ok) { S.approvals.push(ok); toast('Prototype: the kit would be made in this browser, as in first-time setup.', 'info'); }
  });
  act('mcp-revoke', (t) => { const i = assistants.findIndex((x) => x.id === t.dataset.id); if (i >= 0) { const [x] = assistants.splice(i, 1); log(`assistant ${x.id} revoked`); toast(`${x.name}: its token no longer works.`); A.renderList(); } });
  act('mcp-add', () => toast('Prototype: the scoped token would be made in this browser; HomersLab keeps only its hash, so it is never sent back.', 'info'));
})();
