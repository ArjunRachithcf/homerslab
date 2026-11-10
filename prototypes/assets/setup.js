// HomersLab first-time setup prototype, as a setup assistant (round 2). PROTOTYPE, throwaway.
// One question per screen, in plain words, with a sensible default already chosen: the sign-in suggested from the
// machine's memory, NetBird, the domain from your mail address and its DNS provider from the domain's name servers, the
// mail server from the domain. Everything else waits behind "More options"; each token has "Where do I find this?".
// Two halves (decision of round 1): on the home network, from the one-time link, up to "mesh connected, certificate
// issued"; then this device joins the mesh, the passkey is made at auth.<domain>, and the home-network listener
// closes at the first sign-in over the mesh. Secrets travel only inward: none is ever shown back.
// Hash routes per step; ?resume=yes|end starts as if interrupted; ?browser=primary shows the recovery kit's refusal.
(() => {
  const D = HL_DATA, { esc, toast, osPrompt, now, hhmm } = UI;
  const STEPS = ['welcome', 'you', 'household', 'signin', 'network', 'domain', 'mail', 'kit', 'join', 'passkey', 'done']
    .map((id, i) => ({ id, half: i < 8 ? 'lan' : 'mesh' }));
  const LAN = STEPS.filter((s) => s.half === 'lan').map((s) => s.id);
  const idx = (id) => STEPS.findIndex((s) => s.id === id);

  Proto.option({ key: 'resume', label: 'Arriving', def: 'no', values: [{ v: 'no', name: 'For the first time' }, { v: 'yes', name: 'After an interruption' }, { v: 'end', name: 'With the first half done' }], note: 'Reload after switching.' });
  Proto.option({ key: 'browser', label: 'This browser runs on', def: 'laptop', values: [{ v: 'laptop', name: 'Inês’s laptop' }, { v: 'primary', name: 'Study PC itself' }] });
  Proto.option({ key: 'onmesh', label: 'Is this laptop on the mesh yet?', def: 'no', values: [{ v: 'no', name: 'Not yet' }, { v: 'yes', name: 'Yes, already' }] });
  const arrive = Proto.get('resume', 'no'), resume = arrive === 'yes', firstHalf = arrive === 'end';

  const S = {
    done: new Set(firstHalf ? LAN : resume ? ['you', 'household', 'signin'] : []),
    name: 'Inês Ferreira', mail: 'ines@ferreira.family', household: 'The Ferreira household', touched: {},
    idp: 'authentik', mesh: 'netbird', second: 'none', planeUrl: '', hsUrl: '',
    domain: 'ferreira.family', dns: null,
    smtp: { from: 'homerslab@ferreira.family', host: 'mail.infomaniak.com', port: '587', sec: 'starttls', user: 'homerslab@ferreira.family' },
    saved: firstHalf ? { mesh: true, dns: true, smtp: true } : {}, checked: firstHalf ? { mesh: '13:51', dns: '13:55' } : {}, mailSent: firstHalf ? '13:57' : null,
    kitKept: { print: firstHalf, save: false }, kitConfirm: firstHalf ? '7HQD' : '', joined: null, passkey: null, closed: null,
    open: {}, err: null, busy: null,
  };
  if (firstHalf || resume) S.done.add('welcome');
  const KEY = 'AGE-SECRET-KEY-1QF7Z4VM8K3P5R6T9W0X2YDA6C8E0G2J4L6N8Q0S2U4W7HQD';
  const RESTIC = 'maple-harbour-copper-north-ember-quarry';
  const LINK_UNTIL = new Date(D.NOW.getTime() + 30 * 60000);
  const AUTH = () => `auth.${S.domain || 'ferreira.family'}`;

  // ---------- the choices, each in plain words, and where its token is found ----------
  const IDP = {
    authentik: { name: 'Authentik', sub: 'Suggested for study-pc: it has 32 GB of memory', more: 'The fuller one: about 2 GB of memory, and a web console for emergencies.' },
    kanidm: { name: 'Kanidm', sub: 'The lighter one: about 200 MB of memory', more: 'The lighter one: about 200 MB, for a Raspberry Pi or a small PC; a command line for emergencies.' },
  };
  const MESH = {
    netbird: { name: 'NetBird', sub: 'NetBird’s cloud · free for a household · public apps on your own domain', token: 'NetBird access token', app: 'NetBird', from: 'netbird.io', checked: 'NetBird account “Ferreira”, no machines yet' },
    tailscale: { name: 'Tailscale', sub: 'Tailscale’s cloud · public apps through Funnel; a domain is optional', token: 'Tailscale API access token', app: 'Tailscale', from: 'tailscale.com', checked: 'tailnet tail8f3c.ts.net' },
    headscale: { name: 'Headscale', sub: 'Your own server, off-site · no public apps', token: 'Headscale API key', app: 'Tailscale', from: 'tailscale.com', checked: 'Headscale 0.29.7, no machines yet' },
  };
  const DNS = {
    infomaniak: { name: 'Infomaniak', smtp: 'mail.infomaniak.com' }, cloudflare: { name: 'Cloudflare', smtp: null }, gandi: { name: 'Gandi', smtp: 'mail.gandi.net' },
    ovh: { name: 'OVHcloud', smtp: 'ssl0.ovh.net' }, hetzner: { name: 'Hetzner', smtp: 'mail.your-server.de' }, desec: { name: 'deSEC', smtp: null }, porkbun: { name: 'Porkbun', smtp: null },
  };
  // the DNS provider, guessed from the domain's name servers (simulated: the Ferreiras' domain is at Infomaniak)
  const guessDns = (d) => { if (!d || d === 'ferreira.family') return 'infomaniak'; let h = 0; for (const c of d) h = (h * 31 + c.charCodeAt(0)) >>> 0; return Object.keys(DNS)[1 + (h % 6)]; };
  const dns = () => S.dns || guessDns(S.domain);
  const HELP = {
    netbird: { title: 'Where to find a NetBird token', sub: 'In NetBird’s dashboard; it takes about a minute.', steps: ['Open <b>app.netbird.io</b> and sign in.', 'Go to <span class="path"><b>Team</b>›<b>Service users</b></span> and choose <b>Create service user</b>, as an admin.', 'Open it, choose <b>Create token</b>, and copy the token.'] },
    tailscale: { title: 'Where to find a Tailscale token', sub: 'In Tailscale’s admin console. HomersLab uses it once to make its own OAuth client, then forgets it.', steps: ['Open <b>login.tailscale.com/admin</b>.', 'Go to <span class="path"><b>Settings</b>›<b>Keys</b></span>.', 'Choose <b>Generate access token</b> and copy it.'] },
    headscale: { title: 'Where to find a Headscale key', sub: 'On the server that runs Headscale.', steps: ['Open a terminal on that server.', 'Run <b>headscale apikeys create</b>.', 'Copy the key it prints.'] },
    infomaniak: { title: 'Where to find an Infomaniak token', sub: 'HomersLab only writes _acme-challenge records, for one wildcard certificate. Your mail records stay as they are.', steps: ['Open <b>manager.infomaniak.com</b> and sign in.', 'Go to <span class="path"><b>Your profile</b>›<b>Developer</b>›<b>API tokens</b></span>.', 'Create a token with the <b>domain</b> scope, and copy it.'] },
    cloudflare: { title: 'Where to find a Cloudflare token', sub: 'HomersLab only writes _acme-challenge records.', steps: ['Open <b>dash.cloudflare.com</b>.', 'Go to <span class="path"><b>My profile</b>›<b>API tokens</b></span>.', 'Use the <b>Edit zone DNS</b> template for this zone, and copy the token.'] },
    dns: { title: 'Where to find a DNS token', sub: 'HomersLab only writes _acme-challenge records.', steps: ['Open your DNS provider’s dashboard.', 'Find its API tokens or keys.', 'Make one that may edit this domain’s DNS records, and copy it.'] },
    smtp: { title: 'Where to find the mail password', sub: 'The password of the mailbox HomersLab sends from. An app password is better, where your provider has them.', steps: ['Open your mail provider’s settings for that address.', 'Make an app password if it offers one; otherwise use the mailbox’s password.', 'Copy it and paste it here.'] },
  };
  const meshes = () => [S.mesh, ...(S.mesh === 'netbird' && S.second !== 'none' ? [S.second] : [])];
  const meshNames = () => UI.and(meshes().map((m) => MESH[m].name));

  // ---------- small pieces ----------
  const kv = (k, v) => `<div class="row"><span class="row-main"><span class="row-label">${k}</span></span><span class="row-value">${v}</span></div>`;
  const field = (key, label, value, { type = 'text', hint = '', auto = 'off', mono = false } = {}) => `<div class="field"><label class="field-label" for="f-${key}">${label}</label><input id="f-${key}" class="input${mono ? ' input-secret' : ''}" type="${type}" value="${esc(value)}" data-f="${key}" autocomplete="${auto}" spellcheck="false"${hint ? ` aria-describedby="h-${key}"` : ''}${S.err === key ? ' aria-invalid="true"' : ''}>${hint ? `<span class="hint" id="h-${key}">${hint}</span>` : ''}</div>`;
  const pick = (title, sub) => `<div class="pick"><span class="pick-text"><span class="pick-title">${title}</span><span class="pick-sub">${sub}</span></span><span class="choice-mark">${I('check2')}</span></div>`;
  const check = (html) => `<div class="as-check">${I('check')}<span>${html}</span></div>`;
  const secret = (key, label, help) => S.saved[key]
    ? `<div class="field"><span class="field-label">${label}</span><div class="secret-saved"><span class="subhead">${I('lock', 'icon icon-sm')}Saved on study-pc. It won’t be shown again.</span><button type="button" class="btn btn-sm btn-plain" data-s="replace" data-k="${key}">Replace</button></div></div>`
    : `<div class="field"><div class="field-head"><label class="field-label" for="sec-${key}">${label}</label>${help ? `<button type="button" class="help-btn" data-s="help" data-k="${help}">Where do I find this?</button>` : ''}</div><input id="sec-${key}" class="input input-secret" type="password" autocomplete="off" spellcheck="false" placeholder="Paste it here" data-secret="${key}"${S.err === key ? ` aria-invalid="true" aria-describedby="err-${key}"` : ''}>${S.err === key ? `<span class="error-text" id="err-${key}">Paste it to continue. Any text works in this prototype.</span>` : ''}</div>`;
  const radios = (name, value, items) => `<div class="group" role="radiogroup" aria-label="${esc(name)}">${items.map(([v, label, sub]) => `<label class="row row-choice"><input type="radio" name="${esc(name)}" value="${v}" data-r="${esc(name)}" ${value === v ? 'checked' : ''}><span class="row-main"><span class="row-label">${label}</span>${sub ? `<span class="row-sub wrap">${sub}</span>` : ''}</span><span class="row-trail">${I('check2', 'icon checkmark')}</span></label>`).join('')}</div>`;
  const surname = () => (S.name.trim().split(/\s+/).pop() || '');
  const mailDomain = () => (S.mail.split('@')[1] || '').trim();
  const validMail = (m) => /^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(m);

  // ---------- the screens: a title, one sentence, the question, More options, one primary ----------
  const V = {};
  V.welcome = () => ({
    illo: 'welcome', title: resume ? 'Welcome back' : 'Let’s set up HomersLab',
    lead: resume ? 'You stopped at your private network yesterday at 21:40. What you chose then is kept.' : 'This machine, <b>study-pc</b>, becomes your household’s home server. It takes about 20 minutes, and you can stop at any time.',
    moreLabel: 'About this machine',
    more: `<div class="group">${kv('Machine', 'study-pc · Fedora 43 · x86_64')}${kv('Processor', 'Ryzen 7 5700X, 8 cores')}${kv('Memory', '32 GB')}${kv('GPU', 'NVIDIA RTX 3060, 12 GB')}${kv('Secrets', 'Sealed to its TPM 2.0')}${kv('Containers', 'Docker 29.1')}</div>
      <p class="group-head">You’ll need</p><ul class="group lead-sm">${[['mesh', 'Your mesh’s token: NetBird, Tailscale or Headscale'], ['globe', 'Your domain and its DNS provider’s token'], ['mail', 'The password of a mailbox HomersLab can send from'], ['printer', 'A printer or a password manager, for the recovery kit'], ['laptop', 'This laptop or your phone, to put on the mesh']].map(([i, t]) => `<li class="row"><span class="row-lead">${I(i, 'icon icon-md')}</span><span class="row-main"><span class="row-label wrap">${t}</span></span></li>`).join('')}</ul>`,
    primary: { label: resume ? 'Continue where I stopped' : 'Get started' },
  });
  V.you = () => ({
    illo: 'you', title: 'What’s your name?', lead: 'You’ll be the household’s admin: you install apps and invite people.',
    body: field('name', 'Your name', S.name, { auto: 'name' }) + field('mail', 'Your mail address', S.mail, { type: 'email', auto: 'email', hint: 'For alerts, and to reset your passkey.' }),
    primary: { label: 'Continue', ok: !!S.name.trim() && validMail(S.mail), why: 'Your name and a mail address, to continue.' },
  });
  V.household = () => ({
    illo: 'household', title: 'Name your household', lead: 'Everyone you invite joins it. You can change its name later.',
    body: field('household', 'The household’s name', S.household),
    primary: { label: 'Continue', ok: !!S.household.trim(), why: 'Give the household a name to continue.' },
  });
  V.signin = () => ({
    illo: 'signin', title: 'How everyone signs in', lead: 'One household account and a passkey for every app, on the TV too.',
    body: pick(IDP[S.idp].name, IDP[S.idp].sub),
    more: `${radios('Sign-in', S.idp, [['authentik', 'Authentik', IDP.authentik.more], ['kanidm', 'Kanidm', IDP.kanidm.more]])}<p class="subhead">Either way, everyone gets passkeys and one account for every app, and you manage people in Admin, never in its own console.</p>`,
    primary: { label: 'Continue' },
  });
  V.network = () => {
    const m = MESH[S.mesh], two = meshes().length > 1;
    return {
      illo: 'network', title: 'Your private network', lead: `It joins your machines and everyone’s devices, at home or away.${S.mesh === 'netbird' && !two ? ' NetBird is a good start.' : ''}`,
      body: pick(two ? `${m.name} and ${MESH[S.second].name}` : m.name, two ? 'People can use the app they already have; every node joins both' : m.sub)
        + (S.mesh === 'headscale' ? field('hsUrl', 'Its address', S.hsUrl || 'https://headscale.ferreira.family') : '')
        + meshes().map((k) => secret(k === S.mesh ? 'mesh' : 'second', MESH[k].token, k)).join('')
        + (S.checked.mesh ? check(`Checked at ${esc(S.checked.mesh)}: ${esc(m.checked)}.`) : ''),
      more: radios('Mesh', S.mesh, Object.entries(MESH).map(([k, x]) => [k, x.name, x.sub]))
        + (S.mesh === 'netbird' ? `<label class="as-row"><span class="row-main"><span class="row-label">Tailscale as well</span><span class="row-sub wrap">For people who already use the Tailscale app; every node joins both meshes.</span></span><input type="checkbox" class="switch" data-s="second" ${S.second === 'tailscale' ? 'checked' : ''} aria-label="Tailscale as well"></label>`
          + field('planeUrl', 'Your own NetBird server', S.planeUrl, { hint: 'Leave it empty for NetBird’s cloud. On a server outside your home: HomersLab never opens a port on your router.' }) : ''),
      primary: { label: meshes().every((k) => S.saved[k === S.mesh ? 'mesh' : 'second']) && S.checked.mesh ? 'Continue' : 'Check and continue' },
    };
  };
  V.domain = () => {
    const p = DNS[dns()];
    return {
      illo: 'domain', title: 'Your domain', lead: `Every app gets a name under it, like <span class="host">photos.${esc(S.domain || 'your.domain')}</span>, with its own certificate. The names answer only on the mesh.`,
      body: field('domain', 'Domain', S.domain, { hint: S.touched.domain ? '' : 'From your mail address.' })
        + `<div class="as-row">${I('globe', 'icon icon-md')}<span class="row-main"><span class="row-title">Its DNS is at ${esc(p.name)}</span><span class="row-sub wrap">${S.dns ? 'As you chose' : `Found from ${esc(S.domain || 'the domain')}’s name servers`}</span></span></div>`
        + secret('dns', `${p.name} API token`, HELP[dns()] ? dns() : 'dns')
        + (S.checked.dns ? check(`Checked at ${esc(S.checked.dns)}: the certificate for <b>*.${esc(S.domain)}</b> is issued.`) : ''),
      more: `<div class="field"><label class="field-label" for="f-dns">Another DNS provider</label><select id="f-dns" class="select" data-r="dns">${Object.entries(DNS).map(([k, x]) => `<option value="${k}" ${dns() === k ? 'selected' : ''}>${x.name}</option>`).join('')}</select></div><p class="subhead">HomersLab only writes _acme-challenge records, for one wildcard certificate, so the apps’ names never appear in public certificate logs. Nothing else in your zone changes, mail records included.</p>`,
      primary: { label: S.saved.dns && S.checked.dns ? 'Continue' : 'Check and continue' },
    };
  };
  V.mail = () => ({
    illo: 'mail', title: 'Sending mail', lead: `HomersLab writes invites, passkey resets and alerts from <b>${esc(S.smtp.from)}</b>, through ${esc(DNS[dns()].name)}’s mail server.`,
    body: secret('smtp', `The password of ${esc(S.smtp.from)}`, 'smtp') + (S.mailSent ? check(`A test mail went to ${esc(S.mail)} at ${esc(S.mailSent)}.`) : ''),
    secondary: S.saved.smtp && !S.mailSent ? `<button type="button" class="btn btn-plain" data-s="testmail">${I('mail')}Send a test mail</button>` : '',
    more: `<div class="fields">${[['from', 'Send from'], ['host', 'Mail server'], ['port', 'Port'], ['user', 'Username']].map(([k, l]) => `<div class="field"><label class="field-label" for="m-${k}">${l}</label><input id="m-${k}" class="input" value="${esc(S.smtp[k])}" data-m="${k}" autocomplete="off" spellcheck="false"></div>`).join('')}
      <div class="field"><label class="field-label" for="m-sec">Security</label><select id="m-sec" class="select" data-m="sec"><option value="starttls" ${S.smtp.sec === 'starttls' ? 'selected' : ''}>STARTTLS</option><option value="tls" ${S.smtp.sec === 'tls' ? 'selected' : ''}>TLS</option></select></div></div>`,
    primary: { label: 'Continue' },
  });
  V.kit = () => {
    const onPrimary = Proto.get('browser', 'laptop') === 'primary', kept = S.kitKept.print || S.kitKept.save, confirmed = S.kitConfirm.trim().toUpperCase() === KEY.slice(-4);
    if (onPrimary) return {
      illo: 'kit', title: 'Your recovery kit', lead: 'Enough to restore everything on new hardware, even without HomersLab.',
      body: `<div class="notice notice-warn" role="alert">${I('alert')}<span class="notice-body"><b>This browser is on study-pc itself.</b>The kit’s key would be made on the machine it protects against, so HomersLab won’t make it here. Open this link on another device on your home network, such as your laptop.</span></div>`,
      primary: { label: 'Continue', ok: false, why: 'Make the kit on another device.' },
    };
    return {
      illo: 'kit', title: 'Your recovery kit', lead: 'Enough to restore everything on new hardware, even without HomersLab. Print it, or save it in your password manager, away from your machines.',
      body: `<div class="as-two"><button type="button" class="btn" data-s="print" aria-pressed="${S.kitKept.print}">${I(S.kitKept.print ? 'check' : 'printer')}${S.kitKept.print ? 'Printed' : 'Print it'}</button><button type="button" class="btn" data-s="save" aria-pressed="${S.kitKept.save}">${I(S.kitKept.save ? 'check' : 'download')}${S.kitKept.save ? 'Saved' : 'Save it'}</button></div>`
        + field('kitConfirm', 'Type the last four characters of its key', S.kitConfirm, { mono: true, hint: confirmed ? 'That matches.' : 'From the kit, so we know you have it somewhere safe.' }),
      moreLabel: 'What’s in the kit',
      more: `<div class="as-kit"><div><span>Archive key (age)</span><span><span class="kit-key">${KEY}</span></span></div><div><span>Backup password</span><span><span class="kit-key">${RESTIC}</span></span></div>
        <div><span>The primary</span><span>study-pc · Fedora 43 · at home on 192.168.1.20</span></div><div><span>Sign-in</span><span>${IDP[S.idp].name} · admin ${esc(S.name)}</span></div><div><span>Meshes</span><span>${esc(meshNames())}</span></div><div><span>Domain</span><span>${esc(S.domain)}</span></div><div><span>By hand</span><span>A one-page how-to with plain restic and age</span></div></div>
        <p class="subhead">Made here, in this browser on your laptop: the archive’s private key never reaches your machines; only its public half goes to study-pc.</p>`,
      primary: { label: 'Continue', ok: kept && confirmed, why: !kept ? 'Print the kit or save it first.' : 'Type the last four characters to continue.' },
    };
  };
  V.join = () => {
    const onMesh = S.joined || Proto.get('onmesh', 'no') === 'yes';
    if (onMesh && !S.joined) S.joined = hhmm(now());
    const m = MESH[S.mesh];
    return {
      illo: 'join', title: S.joined ? 'This laptop is on the mesh' : 'Put this laptop on the mesh', lead: `Admin and your passkey live at names that answer only on the mesh. Then you continue at <span class="host">${esc(AUTH())}</span>.`,
      body: check(`<b>Mesh connected, certificate issued.</b> study-pc is on ${esc(meshNames())}, and *.${esc(S.domain)} has its certificate.`)
        + (S.joined ? check(`This laptop joined ${esc(m.name)} at ${esc(S.joined)}, as Inês’s laptop.`)
          : `<ol class="group as-steps"><li><span class="n">1</span><span>Install the ${esc(m.app)} app<small>From ${esc(m.from)}, for macOS, Windows or Linux</small></span></li><li><span class="n">2</span><span>Give it this one-time key<small>It works once, for 15 minutes</small><span class="cmd" style="margin-top:8px"><code>NB-7F3K-QX2M-9WTD</code><button type="button" class="btn btn-icon btn-sm" data-s="copy" aria-label="Copy the key">${I('copy')}</button></span></span></li></ol>
            <div class="as-wait" aria-live="polite"><span><span class="spinner"></span>Waiting for this laptop…</span><button type="button" class="btn btn-sm" data-s="join">Simulate: it joined</button></div>`),
      moreLabel: 'Use your phone instead',
      more: `<div class="as-qr">${qr()}<p class="subhead">Scan it with the phone’s camera. It opens the ${esc(m.app)} app with a one-time key for the phone, which works for 15 minutes; then continue on the phone.</p></div>`,
      primary: { label: `Continue at ${AUTH()}`, ok: !!S.joined, why: 'Put this laptop or your phone on the mesh first.' },
    };
  };
  V.passkey = () => ({
    illo: 'passkey', title: S.passkey ? 'Your passkey is saved' : 'Create your passkey',
    lead: S.passkey ? 'Add a second one later, such as a security key, so losing one device doesn’t lock you out.' : `It takes the place of a password: Touch ID, Windows Hello or a security key. It belongs to <span class="host">${esc(AUTH())}</span>, the name you’ll use every day.`,
    body: `<div class="as-account"><span class="avatar avatar-lg" style="--h:195" aria-hidden="true">${esc((S.name.trim()[0] || 'I').toUpperCase())}</span><span class="row-main"><span class="row-title">${esc(S.name)}</span><span class="row-sub">${esc(S.mail)} · admin of ${esc(S.household.replace(/^The /, 'the '))}</span></span></div>`,
    primary: S.passkey ? { label: 'Sign in to Admin' } : { label: 'Create passkey', act: 'passkey', icon: 'key' },
    back: !S.passkey,
  });
  V.done = () => ({
    illo: 'done', title: 'You’re all set', lead: `${esc(S.household)} answers over the mesh from now on.`,
    body: `<div class="as-closed"><span class="s-x">${I('lock')}</span><span><b>The home-network listener is closed, for good.</b><p>Closed at ${esc(S.closed)}, at your first sign-in over the mesh: <span class="host">192.168.1.20:7020</span> no longer answers.</p></span></div>`,
    moreLabel: 'What you set up',
    more: `<div class="group">${kv('Admin', `<span class="host">admin.${esc(S.domain)}</span>`)}${kv('Sign-in', `${IDP[S.idp].name}, with your passkey`)}${kv('Meshes', esc(meshNames()))}${kv('Domain', `${esc(S.domain)}, certificate issued`)}${kv('Mail', esc(S.smtp.from))}${kv('Recovery kit', S.kitKept.print && S.kitKept.save ? 'Printed and saved' : S.kitKept.print ? 'Printed' : 'Saved')}</div>
      <p class="group-head">Next, in Admin</p><div class="group lead-sm">${[['backups', 'Choose where backups go', 'admin.html#/backups'], ['store', 'Get your first app from the Store', 'admin.html#/store'], ['people', 'Invite your household', 'admin.html#/people/invite'], ['nodes', 'Add another machine as a node', 'admin.html#/nodes/add']].map(([i, t, h]) => `<a class="row row-link" href="${h}"><span class="row-lead">${I(i, 'icon icon-md')}</span><span class="row-main"><span class="row-label">${t}</span></span>${I('chevronRight', 'icon row-chev')}</a>`).join('')}</div>`,
    primary: { label: 'Open Admin', href: 'admin.html#/store' }, back: false,
  });

  function qr(seed = 11) {
    let cells = '', s = seed;
    const finder = (x, y) => { for (const [ox, oy] of [[0, 0], [18, 0], [0, 18]]) { const dx = x - ox, dy = y - oy; if (dx >= 0 && dx < 7 && dy >= 0 && dy < 7) return dx === 0 || dx === 6 || dy === 0 || dy === 6 || (dx >= 2 && dx <= 4 && dy >= 2 && dy <= 4) ? 1 : 0; if (dx >= -1 && dx <= 7 && dy >= -1 && dy <= 7) return 0; } return null; };
    for (let y = 0; y < 25; y++) for (let x = 0; x < 25; x++) { s = (s * 1103515245 + 12345) & 0x7fffffff; const f = finder(x, y); if (f === null ? (s >> 9) % 2 === 0 : f === 1) cells += `<rect x="${x}" y="${y}" width="1.02" height="1.02"/>`; }
    return `<svg class="qr" viewBox="0 0 25 25" role="img" aria-label="QR code to put a phone on the mesh (simulated)">${cells}</svg>`;
  }

  // ---------- where we are ----------
  const firstOpen = () => STEPS.find((s) => !S.done.has(s.id)) || STEPS[STEPS.length - 1];
  const reachable = (id) => STEPS.slice(0, idx(id)).every((x) => S.done.has(x.id));
  function current() {
    const id = (location.hash || '').replace(/^#\//, '');
    const s = STEPS.find((x) => x.id === id);
    if (!s) return resume ? STEPS[0] : firstOpen();
    return s.id === 'welcome' || reachable(s.id) ? s : firstOpen();
  }
  const illo = (st, animate) => {
    const v = V[st.id]();
    return ILLO[v.illo]({ name: S.name, mail: S.mail, domain: S.domain, made: !!S.passkey }).replace('class="illo ', `class="illo ${animate ? 'is-in ' : ''}`);
  };
  const dots = (st) => `<div class="dots" aria-hidden="true">${['lan', 'mesh'].map((h) => `<span class="dots-half">${STEPS.filter((s) => s.half === h).map((s) => `<i class="dot-step${s.id === st.id ? ' is-now' : S.done.has(s.id) ? ' is-done' : ''}"></i>`).join('')}</span>`).join('')}</div>`;
  const actionsHtml = (v) => {
    const p = v.primary, off = p.ok === false, busy = S.busy;
    const btn = p.href ? `<a class="btn btn-primary" href="${p.href}">${p.label}${I('arrowRight')}</a>`
      : `<button type="button" class="btn btn-primary" data-s="${p.act || 'next'}" ${off ? 'disabled aria-describedby="as-why"' : ''} ${busy ? 'aria-busy="true"' : ''}>${busy ? `<span class="spinner" aria-hidden="true"></span>${busy}` : `${p.icon ? I(p.icon) : ''}${p.label}`}</button>`;
    return `${btn}${off && p.why ? `<p class="as-why" id="as-why">${p.why}</p>` : ''}${v.secondary || ''}`;
  };

  function render() {
    const st = current();
    if (location.hash !== '#/' + st.id) history.replaceState(null, '', location.pathname + location.search + '#/' + st.id);
    const v = V[st.id](), i = idx(st.id);
    const atAuth = st.half === 'mesh' && st.id !== 'join';
    document.querySelector('#s-top').innerHTML = `<span class="brand"><span class="brand-l">${LOCKUP}</span><span class="brand-m" role="img" aria-label="HomersLab">${MARK}</span></span>
      <span class="as-where">${S.closed ? `${I('lock')}<span class="host">${esc(AUTH())}</span><span class="sep long">· the home network is closed</span>`
        : atAuth ? `<span class="dot dot-green" aria-hidden="true"></span><span class="host">${esc(AUTH())}</span><span class="sep long">· over ${esc(MESH[S.mesh].name)}</span>`
        : `${I('home')}<span class="host">192.168.1.20:7020</span><span class="sep">· link until ${hhmm(LINK_UNTIL)}</span>`}</span>`;
    document.querySelector('#s-main').innerHTML = `
      <section class="assistant glass-thick" aria-labelledby="as-title">
        ${i > 0 && v.back !== false ? `<button type="button" class="topbar-back as-back" data-s="back">${I('chevronBack')}Back</button>` : ''}
        ${dots(st)}
        <div data-illo-slot>${illo(st, !UI.still())}</div>
        <div><h1 class="as-title" id="as-title" tabindex="-1"><span class="sr-only">Step ${i + 1} of ${STEPS.length}: </span>${v.title}</h1>${v.lead ? `<p class="as-lead">${v.lead}</p>` : ''}</div>
        ${v.body ? `<div class="as-body">${v.body}</div>` : ''}
        ${v.more ? `<details class="more" data-more="${st.id}" ${S.open[st.id] ? 'open' : ''}><summary>${v.moreLabel || 'More options'}${I('chevronDown')}</summary><div class="more-body">${v.more}</div></details>` : ''}
        <div class="as-actions">${actionsHtml(v)}</div>
      </section>`;
    document.querySelector('#s-foot').textContent = S.closed ? 'Setup is done. From now on HomersLab answers only on the mesh.'
      : st.half === 'lan' ? `You can stop at any time: what you’ve chosen is kept, and you continue where you stopped. This link works once, from this machine or your home network, until ${hhmm(LINK_UNTIL)}.`
      : 'You can stop at any time: what you’ve chosen is kept, and you continue where you stopped.';
    document.title = `${v.title.replace(/<[^>]+>/g, '')} · HomersLab setup`;
    Proto.refresh();
  }
  // a re-render that keeps the person's place: the focused field and its caret, the open More options
  function rerender() {
    const a = document.activeElement, id = a && a.id, pos = a && typeof a.selectionStart === 'number' ? a.selectionStart : null;
    render();
    const n = id && document.getElementById(id);
    if (n) { n.focus({ preventScroll: true }); if (pos !== null) try { n.setSelectionRange(pos, pos); } catch {} }
  }
  // only what follows the typing: the illustration, the sentence and the primary button
  function refreshLive() {
    const st = current(), v = V[st.id]();
    const slot = document.querySelector('[data-illo-slot]'); if (slot) slot.innerHTML = illo(st, false);
    const acts = document.querySelector('.as-actions'); if (acts) acts.innerHTML = actionsHtml(v);
    const lead = document.querySelector('.as-lead'); if (lead && v.lead) lead.innerHTML = v.lead;
  }

  // ---------- moving between screens: forward pushes, back pops ----------
  let lastIdx = null;
  function navigate() {
    const st = current(), i = idx(st.id);
    const type = lastIdx === null || lastIdx === i ? 'fade' : i > lastIdx ? 'push' : 'pop';
    lastIdx = i;
    HL.run(render, { type }).then(() => { document.querySelector('#as-title')?.focus({ preventScroll: true }); window.scrollTo(0, 0); });
  }
  const go = (id) => { location.hash = '#/' + id; };
  window.addEventListener('hashchange', navigate);

  // ---------- typing ----------
  document.addEventListener('input', (e) => {
    const t = e.target;
    if (t.dataset.f) {
      const k = t.dataset.f; S[k] = t.value; S.touched[k] = true; if (S.err === k) S.err = null;
      if (k === 'name' && !S.touched.household && surname()) S.household = `The ${surname()} household`;
      if (k === 'mail' && !S.touched.domain && mailDomain()) { S.domain = mailDomain(); if (!S.touched.from) S.smtp.from = S.smtp.user = `homerslab@${S.domain}`; }
      if (k === 'domain') { S.dns = null; delete S.saved.dns; delete S.checked.dns; if (!S.touched.from) S.smtp.from = S.smtp.user = `homerslab@${S.domain}`; }
      refreshLive();
    }
    if (t.dataset.m) { S.smtp[t.dataset.m] = t.value; if (t.dataset.m === 'from') S.touched.from = true; if (t.dataset.m === 'host') S.touched.host = true; }
    if (t.dataset.secret && S.err === t.dataset.secret) { S.err = null; t.removeAttribute('aria-invalid'); t.removeAttribute('aria-describedby'); document.getElementById('err-' + t.dataset.secret)?.remove(); }
  });
  document.addEventListener('change', (e) => {
    const t = e.target;
    if (t.dataset.r === 'Sign-in') { S.idp = t.value; rerender(); }
    if (t.dataset.r === 'Mesh') { S.mesh = t.value; if (t.value !== 'netbird') S.second = 'none'; delete S.saved.mesh; delete S.checked.mesh; delete S.saved.second; rerender(); }
    if (t.dataset.r === 'dns') { S.dns = t.value; delete S.saved.dns; delete S.checked.dns; const p = DNS[S.dns]; if (p.smtp && !S.touched.host) S.smtp.host = p.smtp; rerender(); }
    if (t.dataset.s === 'second') { S.second = t.checked ? 'tailscale' : 'none'; delete S.saved.second; delete S.checked.mesh; setTimeout(rerender, UI.still() ? 0 : 240); }
  });
  document.addEventListener('toggle', (e) => { const d = e.target.closest && e.target.closest('details[data-more]'); if (d) S.open[d.dataset.more] = d.open; }, true);
  document.addEventListener('keydown', (e) => { if (e.key === 'Enter' && e.target.matches && e.target.matches('.assistant input:not([type="checkbox"]):not([type="radio"])')) { e.preventDefault(); document.querySelector('.as-actions .btn-primary:not([disabled])')?.click(); } });

  // ---------- pressing ----------
  // a check that takes a moment: the primary says what it is doing, then the screen moves on
  function busyThen(words, ms, fn) { S.busy = words; refreshLive(); setTimeout(() => { S.busy = null; fn(); }, UI.still() ? 150 : ms); }
  function needSecrets(keys) {
    for (const k of keys) {
      if (S.saved[k]) continue;
      const inp = document.querySelector(`[data-secret="${k}"]`);
      if (!inp || !inp.value.trim()) { S.err = k; rerender(); document.querySelector(`[data-secret="${k}"]`)?.focus(); return false; }
    }
    for (const k of keys) { const inp = document.querySelector(`[data-secret="${k}"]`); if (inp) inp.value = ''; S.saved[k] = true; } // inward only: nothing echoes it back
    return true;
  }
  const next = (st) => { S.done.add(st.id); go(STEPS[idx(st.id) + 1].id); };

  document.addEventListener('click', async (e) => {
    const b = e.target.closest('[data-s]'); if (!b || b.disabled || b.getAttribute('aria-busy') === 'true') return;
    if (b.dataset.s === 'second') return; // the switch: its change event does the work
    const st = current(), a = b.dataset.s;
    if (a === 'back') { const prev = STEPS[idx(st.id) - 1]; if (prev) go(prev.id); return; }
    if (a === 'next') {
      if (st.id === 'welcome') { S.done.add('welcome'); go(firstOpen().id); return; }
      if (st.id === 'network') {
        const keys = meshes().map((k) => (k === S.mesh ? 'mesh' : 'second'));
        if (keys.every((k) => S.saved[k]) && S.checked.mesh) return next(st);
        if (!needSecrets(keys)) return;
        return busyThen('Checking the token…', 900, () => { S.checked.mesh = hhmm(now()); toast(`Checked: ${MESH[S.mesh].checked}. HomersLab sets up its rules and names.`, 'mesh'); next(st); });
      }
      if (st.id === 'domain') {
        if (!S.domain.trim()) { S.err = 'domain'; rerender(); return; }
        if (S.saved.dns && S.checked.dns) return next(st);
        if (!needSecrets(['dns'])) return;
        return busyThen('Issuing the certificate…', 1200, () => { S.checked.dns = hhmm(now()); toast(`The certificate for *.${S.domain} is issued.`, 'lock'); next(st); });
      }
      if (st.id === 'mail') { if (!needSecrets(['smtp'])) return; return next(st); }
      if (st.id === 'passkey') { S.closed = hhmm(now()); S.done.add('passkey'); toast('Signed in over the mesh. The home-network listener is closed, for good.', 'lock'); go('done'); return; }
      return next(st);
    }
    if (a === 'help') { const h = HELP[b.dataset.k] || HELP.dns; await UI.dialog({ title: esc(h.title), sub: esc(h.sub), body: `<ol class="group help-steps">${h.steps.map((x) => `<li><span>${x}</span></li>`).join('')}</ol><p class="subhead">HomersLab seals it on study-pc and never shows it again. Replacing it later is a risky change, so it asks for your passkey.</p>`, actions: [{ label: 'Done', value: 'ok', kind: 'primary' }] }); return; }
    if (a === 'replace') { delete S.saved[b.dataset.k]; if (b.dataset.k === 'mesh') delete S.checked.mesh; if (b.dataset.k === 'dns') delete S.checked.dns; if (b.dataset.k === 'smtp') S.mailSent = null; rerender(); document.querySelector(`[data-secret="${b.dataset.k}"]`)?.focus(); return; }
    if (a === 'testmail') { S.mailSent = hhmm(now()); rerender(); toast(`A test mail went to ${S.mail}.`, 'mail'); return; }
    if (a === 'print') {
      S.kitKept.print = true;
      document.querySelector('#kit-print').innerHTML = `<h1>HomersLab recovery kit · ${esc(S.household)} (PROTOTYPE, fake values)</h1><dl><dt>Archive key (age)</dt><dd>${KEY}</dd><dt>Backup password (restic)</dt><dd>${RESTIC}</dd><dt>The primary</dt><dd>study-pc, Fedora 43, 192.168.1.20</dd><dt>Domain</dt><dd>${esc(S.domain)}</dd></dl>`;
      rerender(); try { window.print(); } catch {} return;
    }
    if (a === 'save') {
      S.kitKept.save = true;
      try { const txt = `HomersLab recovery kit (PROTOTYPE, fake values)\n${S.household}\n\nArchive key (age): ${KEY}\nBackup password (restic): ${RESTIC}\n`; const url = URL.createObjectURL(new Blob([txt], { type: 'text/plain' })); const l = document.createElement('a'); l.href = url; l.download = 'homerslab-recovery-kit.txt'; l.click(); setTimeout(() => URL.revokeObjectURL(url), 1000); } catch {}
      rerender(); return;
    }
    if (a === 'join') { S.joined = hhmm(now()); toast(`This laptop joined ${MESH[S.mesh].name} as Inês’s laptop.`, 'mesh'); HL.run(rerender, { type: 'fade' }); return; }
    if (a === 'copy') { toast('Copied.'); return; }
    if (a === 'passkey') {
      const ok = await osPrompt({ create: true, site: `${AUTH()} (the household’s sign-in)`, account: S.mail });
      if (ok) { S.passkey = `today at ${hhmm(now())}`; toast('Passkey saved.', 'key'); HL.run(rerender, { type: 'fade' }); }
    }
  });

  Proto.onChange((k) => { if (k !== 'resume') render(); });
  Proto.state(() => ({ step: current().id, half: current().half === 'lan' ? 'on the home network (the one-time link)' : 'on the mesh', done: [...S.done].join(', ') || '—', 'sign-in': S.idp, meshes: meshes().join(' + '), 'DNS provider': `${DNS[dns()].name}${S.dns ? '' : ' (found from the domain)'}`, 'secrets saved (never shown back)': Object.keys(S.saved).join(', ') || '—', 'recovery kit': `${S.kitKept.print ? 'printed ' : ''}${S.kitKept.save ? 'saved' : ''}` || 'not yet', 'this device': S.joined ? `on the mesh since ${S.joined}` : 'not on the mesh', passkey: S.passkey || 'not yet', 'home-network listener': S.closed ? `closed at ${S.closed}` : 'open (setup only)' }));
  window.addEventListener('DOMContentLoaded', () => { Proto.start(); lastIdx = idx(current().id); render(); });
})();
