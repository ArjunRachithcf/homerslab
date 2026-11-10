// HomersLab Admin prototype: nodes, storage locations, and adding a node with a join code. PROTOTYPE, throwaway.
// Risky (the passkey): making a join code, removing a node.
(() => {
  const { D, S, cat, inst, node, host, and, appIcon, chev, meter, memUsed, memFree, appsDisk, fit, suggest, route, render, go, act, log } = A;
  const { esc, toast, dialog, stepUp, hhmm, now, gb } = UI;
  const appsOn = (n) => S.installed.filter((a) => a.node === n.id);
  const gpuWords = (n) => (n.gpu ? n.gpu.name : 'No GPU');
  const sec = (id, title, words, body) => `<section class="sec" aria-labelledby="h-${id}"><div class="sec-head"><h2 id="h-${id}">${title}</h2><p>${words}</p></div><div class="sec-body">${body}</div></section>`;
  const meshName = (k) => (k === 'netbird' ? 'NetBird' : 'Tailscale');

  // ---------- the nodes ----------
  route(/^#\/nodes$/, () => {
    const N = S.nodes, online = N.filter((n) => n.state === 'online').length;
    return `
      <div class="page-head"><div><h1>Nodes</h1><p class="page-sub">${N.length} nodes, ${online === N.length ? 'all online' : `${online} online`}. ${esc(N.find((n) => n.primary).name)} is the primary: it runs HomersLab’s manager and the household’s sign-in.</p></div>
        <div class="page-actions"><a class="btn btn-primary" href="#/nodes/add">${I('plus')}Add a node</a></div></div>
      ${A.nodesSeg('machines')}${A.diskAlerts()}
      <div class="node-cards">${N.map((n) => { const apps = appsOn(n), d = appsDisk(n); return `
        <section class="card node-card" aria-labelledby="nc-${n.id}">
          <div class="nc-head"><div><h2 id="nc-${n.id}"><a href="#/nodes/${n.id}">${esc(n.name)}</a></h2><p class="nc-meta">${esc(n.os)} · ${esc(n.arch)} · ${esc(n.cpu)}</p></div>${n.primary ? '<span class="pill">Primary</span>' : ''}</div>
          <div class="stack" style="gap:12px">${meter({ label: 'Memory', used: +memUsed(n).toFixed(1), total: n.memGB, fmt: (v) => v.toFixed(1), unit: ' GB' })}${meter({ label: d.name + ' (apps)', used: d.usedGB, total: d.sizeGB })}</div>
          <p class="nc-gpu">${I('gpu')}<span>${esc(gpuWords(n))}</span></p>
          <div><p class="subhead">${apps.length} ${apps.length === 1 ? 'app' : 'apps'}${n.primary ? ', plus the household’s sign-in' : ''}</p><div class="nc-apps" style="margin-top:8px">${apps.map((a) => `<span>${appIcon(cat(a.id))}${esc(cat(a.id).name)}</span>`).join('') || '<span class="muted">None yet</span>'}</div></div>
          <div class="nc-foot"><span class="cluster" style="gap:6px"><span class="dot dot-green" aria-hidden="true"></span>Online · load ${n.load}% · ${esc(n.engine)}</span><span class="num">${Object.entries(n.addrs).map(([k, v]) => `${meshName(k)} ${v}`).join(' · ')}</span></div>
        </section>`; }).join('')}</div>
      <section class="a-block" style="margin-top:48px" aria-labelledby="h-st">
        <div class="a-block-head"><div><h2 id="h-st">Storage locations</h2><p>Shared folders apps may use, on any node. Read-only for media apps unless you say otherwise.</p></div><button class="btn" data-act="add-storage">${I('plus')}Add a storage location</button></div>
        <div class="group"><table class="rtable has-lead" style="--lead:22px"><thead><tr><th>Name</th><th>Where</th><th class="n">Used</th><th>Access</th><th>Used by</th></tr></thead><tbody>
          ${S.storage.map((s) => `<tr><td><div class="ident">${I('folder', 'icon icon-lg')}<div class="names"><b>${esc(s.name)}</b><span>${esc(s.kind)}</span></div></div></td>
            <td class="hide-sm">${esc(node(s.node).name)}, <span class="host">${esc(s.path)}</span></td>
            <td class="end n">${s.sharedPoolWith ? `<span class="muted-cell hide-sm">Shares the ${esc(S.storage.find((x) => x.id === s.sharedPoolWith).name)}’s disk</span><span class="muted-cell sm-inline">Shared disk</span>` : `${gb(s.usedGB)} of ${gb(s.sizeGB)}`}</td>
            <td class="hide-sm">${esc(s.access)}</td>
            <td class="line2" style="display:table-cell">${esc(and(s.usedBy.map((u) => cat(u).name)))}</td></tr>`).join('')}
        </tbody></table></div></section>`;
  });
  act('add-storage', async () => {
    const v = await dialog({ title: 'Add a storage location', sub: 'A shared folder that apps on any node may use: the music, the films, a scanner’s inbox.',
      body: `<div class="field"><label class="field-label" for="st-name">Name</label><input id="st-name" class="input" placeholder="For example, Old home videos" autocomplete="off"></div>
        <div><p class="group-head">Where it is</p><div class="group" role="radiogroup" aria-label="Where it is"><label class="row row-choice"><input type="radio" name="st-kind" checked><span class="row-main"><span class="row-label">A folder on a node</span><span class="row-sub wrap">Shared with apps on other nodes over the mesh</span></span><span class="row-trail">${I('check2', 'icon checkmark')}</span></label><label class="row row-choice"><input type="radio" name="st-kind"><span class="row-main"><span class="row-label">A network share on the mesh</span><span class="row-sub wrap">SMB or NFS, on a NAS that isn’t a node</span></span><span class="row-trail">${I('check2', 'icon checkmark')}</span></label></div></div>
        <div class="group"><label class="row"><span class="row-main"><span class="row-label">Read-only for apps</span><span class="row-sub wrap">Media apps read; they never write</span></span><input type="checkbox" class="switch" checked aria-label="Read-only for apps"></label></div>`,
      actions: [{ label: 'Cancel', value: 'no', kind: 'ghost' }, { label: 'Add it', value: 'yes', kind: 'primary' }] });
    if (v === 'yes') toast('Prototype: the storage location would be checked and added here.', 'info');
  });

  // ---------- one node ----------
  route(/^#\/nodes\/(?!add$|disks$|uptime$)([\w-]+)$/, (m) => {
    const n = node(m[1]); if (!n) return `<div class="page-head"><div><h1>No such node</h1></div></div>`;
    const apps = appsOn(n), used = memUsed(n);
    return `
      <div class="page-head"><div><div class="a-h1-line"><h1>${esc(n.name)}</h1>${n.primary ? '<span class="pill">Primary</span>' : ''}<span class="pill pill-green"><span class="dot dot-green dot-sm"></span>Online</span></div>
        <p class="page-sub">${esc(n.os)} · ${esc(n.arch)} · ${esc(n.cpu)} · joined ${esc(n.joined)}</p></div></div>
      <div class="a-facts">
        <div class="widget"><span class="widget-label">Memory</span><span class="widget-value num">${used.toFixed(1)} of ${n.memGB} GB</span><span class="widget-caption">${memFree(n)} GB free</span></div>
        <div class="widget"><span class="widget-label">Apps</span><span class="widget-value num">${apps.length}</span><span class="widget-caption">${n.primary ? 'Plus the sign-in and the manager' : 'Each with its own data here'}</span></div>
        <div class="widget"><span class="widget-label">GPU</span><span class="widget-value">${n.gpu ? (n.gpu.kind === 'nvidia' ? 'NVIDIA' : 'Intel') : '—'}</span><span class="widget-caption">${esc(n.gpu ? n.gpu.name : 'None on this node')}</span></div>
        <div class="widget"><span class="widget-label">Secrets</span><span class="widget-value">${n.tpm ? 'TPM' : 'Root key'}</span><span class="widget-caption">${n.tpm ? 'Sealed to its TPM 2.0' : 'No TPM: a key only root reads'}</span></div>
        <div class="widget"><span class="widget-label">Engine</span><span class="widget-value">${esc(n.engine.split(' ')[0])}</span><span class="widget-caption">${esc(n.engine)}</span></div>
      </div>
      ${sec('napps', `Apps on ${esc(n.name)}`, `Each app runs on one node and keeps its own data there. Its base use, ${gb(n.baseMemGB)}: ${esc(n.baseNote.toLowerCase())}.`,
        apps.length ? `<div class="group"><table class="rtable has-lead" style="--lead:32px"><thead><tr><th>App</th><th class="n">Memory now</th><th class="n">Data</th><th class="chev-cell"><span class="sr-only">Open</span></th></tr></thead><tbody>${apps.map((a) => `<tr class="link-row" data-act="row" data-href="#/apps/${a.id}"><td><div class="ident">${appIcon(cat(a.id), 'app-icon-md')}<div class="names"><a href="#/apps/${a.id}"><b>${esc(cat(a.id).name)}</b></a><span>${esc(cat(a.id).purpose)}</span></div></div></td><td class="end n">${gb(a.memNowGB)}</td><td class="n hide-sm">${gb(a.dataGB)}</td><td class="line2">${gb(a.dataGB)} of data, kept on ${esc(n.name)}</td><td class="chev-cell">${chev()}</td></tr>`).join('')}</tbody></table></div>`
          : `<div class="group"><div class="empty">${I('apps')}<p class="empty-title">No apps yet</p><p class="empty-text">When you install an app, the catalog suggests a node from what it needs.</p></div></div>`)}
      ${sec('disks', 'Disks', 'Where its apps’ data and any storage locations live. Encrypted, btrfs, mirrored where there are two disks for app data.', `<div class="group lead-md">${n.disks.map((d) => `<a class="row row-link" href="#/disks/${d.id}"><span class="row-lead">${I('disk', 'icon icon-md')}</span><span class="row-main"><span class="row-title">${esc(d.name)}${d.layout === 'mirror' ? ', mirrored' : ''}</span><span class="row-sub">${d.state === 'empty' ? 'Empty: ready to prepare' : d.state === 'foreign' ? `${esc(d.fs)}, not HomersLab’s` : `${HL.size(d.id === 'an-media' ? Math.round(d.sizeGB * (A.mediaPaused() ? 0.96 : 0.91)) : d.usedGB)} of ${HL.size(d.sizeGB)}`} · ${esc(d.members.map((x) => x.model).join(' + '))}</span></span>${I('chevronRight', 'icon row-chev')}</a>`).join('')}</div>${n.disks.some((d) => d.role === 'system' && !d.enc) && n.tpm ? `<div class="group"><a class="row row-link" href="#/nodes/${n.id}/encrypt"><span class="row-main"><span class="row-title">Encrypt the system disk</span><span class="row-sub wrap">A guided migration in place, after a verified full copy and a rehearsal in a VM</span></span>${I('chevronRight', 'icon row-chev')}</a></div>` : ''}`)}
      ${sec('nmesh', 'On the mesh', 'Every node joins every mesh the household uses. Its edge serves its apps’ names; the home network sees the machine, never its apps.',
        `<div class="group">${Object.entries(n.addrs).map(([k, v]) => `<div class="row"><span class="row-main"><span class="row-label">${meshName(k)}</span></span><span class="row-value num">${esc(v)}</span></div>`).join('')}<div class="row"><span class="row-main"><span class="row-label">Its edge</span></span><span class="row-value">Up, serving ${apps.length} ${apps.length === 1 ? 'name' : 'names'}</span></div><div class="row"><span class="row-main"><span class="row-label">Firewall</span></span><span class="row-value">No published ports · checked 14:05</span></div></div>`)}
      ${sec('nrm', 'Remove this node', 'Its apps move or are removed first. Moving an app is a backup, then a restore on another node. Removing a node asks for your passkey.',
        `<div class="group"><div class="row is-top row-wrap"><span class="row-main"><span class="row-title">Remove ${esc(n.name)}</span><span class="row-sub wrap">${n.primary ? 'The primary runs HomersLab and the household’s sign-in, so it can’t be removed. To move to new hardware, restore from the recovery kit.' : `${apps.length} ${apps.length === 1 ? 'app' : 'apps'} to move or remove first.`}</span></span>${n.primary ? '' : `<button class="btn btn-sm btn-destructive" data-act="remove-node" data-id="${n.id}">Remove…</button>`}</div></div>`)}`;
  }, { back: { href: '#/nodes', label: 'Nodes' } });
  act('remove-node', async (t) => {
    const n = node(t.dataset.id), apps = appsOn(n);
    const moves = apps.map((a) => { const d = cat(a.id); const to = S.nodes.filter((x) => x.id !== n.id).map((x) => ({ x, f: fit(d, x) })).filter((y) => y.f.ok).sort((p, q) => q.f.score - p.f.score)[0]; return { a, d, to: to?.x }; });
    const v = await dialog({ title: `Remove ${esc(n.name)}`, sub: 'First its apps move, one at a time: a backup here, then a restore there.', wide: true,
      body: A.list([...moves.map((x) => [x.to ? 'move' : 'remove', `${esc(x.d.name)}: ${x.to ? `moves to ${esc(x.to.name)}` : 'no other node fits; it is removed, its data kept'}`]), ['x', `${esc(n.name)} leaves the mesh, and its agent stops`]]),
      actions: [{ label: 'Cancel', value: 'no', kind: 'ghost' }, { label: 'Continue', value: 'yes', kind: 'primary' }] });
    if (v !== 'yes') return;
    const ok = await stepUp({ plan: { id: UI.planId({ k: 'rmnode', n: n.id }), summary: `remove ${n.name}` }, title: `Remove ${esc(n.name)}`, action: 'Remove it', sub: 'Removing a node is a risky change, so HomersLab asks for your passkey.', what: [{ icon: 'nodes', text: `${esc(n.name)} stops being a node, after its ${apps.length} ${apps.length === 1 ? 'app moves' : 'apps move'}` }] });
    if (!ok) return; S.approvals.push(ok); log(`node ${n.id} removal approved (${ok.planId})`); toast('Prototype: the moves would run now, each with its own progress.', 'info');
  });

  // ---------- add a node: the passkey makes a join code, the machine runs one command, it appears ----------
  const add = { step: 'start', distro: 'fedora', code: null, until: null };
  const CMDS = {
    fedora: ['sudo dnf install ./homerslab-1.0.0-1.x86_64.rpm', 'Fedora: dnf checks the package’s signature'],
    debian: ['sudo apt install ./homerslab_1.0.0_amd64.deb', 'Debian and Ubuntu; for arm64, the _arm64.deb'],
    arch: ['sudo pacman -U homerslab-1.0.0-1-x86_64.pkg.tar.zst', 'Arch; for arm64, the aarch64 package'],
  };
  let timer = null;
  const mmss = (left) => `${String(Math.floor(left / 60000)).padStart(2, '0')}:${String(Math.floor((left % 60000) / 1000)).padStart(2, '0')}`;
  route(/^#\/nodes\/add$/, () => {
    clearInterval(timer);
    const left = add.until ? Math.max(0, add.until - Date.now()) : 0;
    const head = `<div class="page-head"><div><h1>Add a node</h1><p class="page-sub">Any Linux machine of yours, x86_64 or arm64 (a Raspberry Pi 5 too), on Fedora, Debian, Ubuntu or Arch. It joins the household with a one-time code.</p></div></div>`;
    if (add.step === 'start') return head + `<div class="split"><section class="card stack" aria-label="Before you start" style="gap:20px">
        <ul class="lines"><li>${I('key')}<span>Your passkey approves a join code<small>Making a join code is a risky change: the machine will run apps and hold their data</small></span></li><li>${I('terminal')}<span>On the machine, you install HomersLab’s signed package and run one command</span></li><li>${I('mesh')}<span>It joins ${esc(A.meshWords())} by itself, and its edge and firewall come up</span></li></ul>
        <div class="btn-row"><button class="btn btn-primary btn-lg" data-act="join-code">${I('key')}Make a join code with your passkey</button></div></section>
        <aside class="rail"><div class="card stack" style="gap:12px"><h2 class="card-title">What a node needs</h2><ul class="lines"><li>${I('check')}<span>Linux on x86_64 or arm64</span></li><li>${I('check')}<span>2 GB of memory or more for its own use</span></li><li>${I('check')}<span>Internet access, to reach the mesh</span></li><li>${I('check')}<span>No open ports on your router, ever</span></li></ul></div></aside></div>
      <section class="a-block" style="margin-top:40px" aria-labelledby="h-now"><div class="a-block-head"><div><h2 id="h-now">Your nodes now</h2><p>What the household has today, so you can see what a new machine would add.</p></div></div>
        <div class="group"><table class="rtable has-lead" style="--lead:22px"><thead><tr><th>Node</th><th class="n">Memory free</th><th>GPU</th><th class="n">Apps</th><th class="chev-cell"><span class="sr-only">Open</span></th></tr></thead><tbody>
        ${S.nodes.map((n) => `<tr class="link-row" data-act="row" data-href="#/nodes/${n.id}"><td><div class="ident">${I('nodes', 'icon icon-lg')}<div class="names"><a href="#/nodes/${n.id}"><b>${esc(n.name)}</b></a><span>${esc(n.os)} · ${esc(n.arch)}${n.primary ? ' · primary' : ''}</span></div></div></td>
          <td class="end n">${memFree(n).toFixed(1)} of ${n.memGB} GB</td><td class="hide-sm">${esc(gpuWords(n))}</td><td class="n hide-sm">${appsOn(n).length}</td>
          <td class="line2">${esc(gpuWords(n))} · ${appsOn(n).length} ${appsOn(n).length === 1 ? 'app' : 'apps'}</td><td class="chev-cell">${chev()}</td></tr>`).join('')}</tbody></table></div></section>`;
    if (add.step === 'code') return head + `<div class="split"><section class="card stack" aria-label="The join code" style="gap:18px">
        <div><p class="subhead">The join code</p><p class="join-code" aria-label="Join code ${add.code.split('').join(' ')}">${esc(add.code)}</p><p class="subhead">Works once, for the next <span class="num" id="join-left">${mmss(left)}</span>. Approved with your passkey at ${esc(add.at)}.</p></div>
        <div class="segmented" style="--n:3" role="radiogroup" aria-label="The machine’s distribution" data-seg="distro"><span class="segmented-thumb" aria-hidden="true"></span>${[['fedora', 'Fedora'], ['debian', 'Debian or Ubuntu'], ['arch', 'Arch']].map(([k, l]) => `<button type="button" role="radio" data-act="join-distro" data-d="${k}" aria-checked="${add.distro === k}">${l}</button>`).join('')}</div>
        <ul class="lines"><li>${I('download')}<span>Download HomersLab’s package from the releases page, then install it<small>${esc(CMDS[add.distro][1])}</small></span></li></ul>
        <div class="cmd"><code>${esc(CMDS[add.distro][0])}</code><button class="btn btn-icon btn-sm" data-act="copy" aria-label="Copy the command">${I('copy')}</button></div>
        <ul class="lines"><li>${I('terminal')}<span>Then join the household</span></li></ul>
        <div class="cmd"><code>sudo homerslab join ${esc(add.code)}</code><button class="btn btn-icon btn-sm" data-act="copy" aria-label="Copy the command">${I('copy')}</button></div>
        <p class="cluster" aria-live="polite"><span class="spinner"></span><span class="muted">Waiting for the machine to join…</span></p>
        <div class="btn-row"><button class="btn" data-act="join-sim">Simulate: the machine runs the command</button><button class="btn btn-plain" data-act="join-cancel">Cancel the code</button></div></section>
        <aside class="rail"><div class="card stack" style="gap:10px"><h2 class="card-title">Why a code, not a password</h2><p class="muted">The code works once and for 30 minutes, and HomersLab knows who made it. The machine never learns a secret it could reuse: it gets its own certificate when it joins.</p></div></aside></div>`;
    if (add.step === 'joined') return head + `<div class="split"><section class="card stack" aria-label="The machine that joined" style="gap:18px">
        <div class="notice notice-ok">${I('check')}<span class="notice-body">A machine joined with the code at ${esc(add.joinedAt)}. The code no longer works.</span></div>
        <div class="group" style="box-shadow:none;background:var(--fill)">
          <div class="row"><span class="row-main"><span class="row-label">Machine</span></span><span class="row-value">garage-mini · Ubuntu 26.04 LTS · x86_64</span></div>
          <div class="row"><span class="row-main"><span class="row-label">Processor and memory</span></span><span class="row-value">Intel N150, 4 cores · 16 GB</span></div>
          <div class="row"><span class="row-main"><span class="row-label">GPU</span></span><span class="row-value">Intel UHD (N150)</span></div>
          <div class="row"><span class="row-main"><span class="row-label">Secrets</span></span><span class="row-value">TPM 2.0: sealed to it</span></div>
          <div class="row"><span class="row-main"><span class="row-label">Mesh</span></span><span class="row-value num">NetBird 100.88.12.7 · Tailscale 100.101.7.25</span></div>
          <div class="row"><span class="row-main"><span class="row-label">Its edge</span></span><span class="row-value">Up; the home network kept out</span></div></div>
        <div class="field"><label class="field-label" for="node-name">Its name in the household</label><input id="node-name" class="input" value="Garage mini PC" autocomplete="off"></div>
        <div class="btn-row"><button class="btn btn-primary btn-lg" data-act="join-done">Add it to the household</button></div></section>
        <aside class="rail"><div class="card stack" style="gap:10px"><h2 class="card-title">Next</h2><p class="muted">When you install an app, HomersLab suggests this node when it fits best. Apps already installed stay where they are.</p></div></aside></div>`;
    return head;
  }, { back: { href: '#/nodes', label: 'Nodes' }, after: () => { if (add.step === 'code') timer = setInterval(() => { const el = document.querySelector('#join-left'); if (!el) { clearInterval(timer); return; } el.textContent = mmss(Math.max(0, add.until - Date.now())); }, 1000); } });
  act('join-code', async () => {
    const plan = { id: UI.planId({ k: 'join', t: S.nodes.length }), summary: 'make a one-time join code' };
    const ok = await stepUp({ plan, title: 'Make a join code', action: 'Make it', sub: 'A join code lets a machine into the household, so HomersLab asks for your passkey.', what: [{ icon: 'nodes', text: 'One machine may join the household as a node, once, in the next 30 minutes' }] });
    if (!ok) return; S.approvals.push(ok);
    Object.assign(add, { step: 'code', code: '7Q4M-2KXP-9RTD', until: Date.now() + 30 * 60000, at: ok.at }); log(`join code made (${ok.planId})`); render();
  });
  act('join-distro', (t) => { add.distro = t.dataset.d; render(); });
  act('join-cancel', () => { Object.assign(add, { step: 'start', code: null, until: null }); log('join code cancelled'); toast('The join code is cancelled.'); render(); });
  act('join-sim', () => { add.step = 'joined'; add.joinedAt = hhmm(now()); log('a machine joined'); render(); });
  act('join-done', () => {
    const name = document.querySelector('#node-name')?.value.trim() || 'Garage mini PC';
    S.nodes.push({ id: 'garage-mini', name, os: 'Ubuntu 26.04 LTS', arch: 'x86_64', cpu: 'Intel N150, 4 cores', memGB: 16, baseMemGB: 1.3, baseNote: 'Its own system, HomersLab’s agent and edge', gpu: { kind: 'intel', name: 'Intel UHD (N150), shared memory' }, tpm: true, engine: 'Docker 29.1', disks: [{ name: 'NVMe disk', sizeGB: 512, usedGB: 22, apps: true }], addrs: { netbird: '100.88.12.7', tailscale: '100.101.7.25' }, joined: UI.dayMonth(now()), state: 'online', load: 2 });
    Object.assign(add, { step: 'start', code: null, until: null }); log(`node ${name} added`); toast(`${name} is a node now.`); go('#/nodes/garage-mini');
  });
})();
