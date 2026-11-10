// HomersLab prototypes: the household's operations data, beside data.js. PROTOTYPE, throwaway, invented.
// Incidents and uptime (ADR 0018), alerts, the maintenance window, domains and their DNS records (ADR 0016), and what
// the disk analyzer finds on the disks (ADR 0017). Uptime is computed from the incidents, so the two always agree.
(() => {
  const D = window.HL_DATA;
  const NOW = D.NOW; // Monday 9 Nov 2026, 14:06

  // ---------- incidents: one stretch of something not working, from the first failed check to "resolved" ----------
  // `hits` names what was down: node ids, app ids, 'public' (the public path) or 'all'
  const incidents = [
    { id: 'in-power', title: 'A power cut in the house', level: 'critical', start: '2026-10-31T19:42', end: '2026-10-31T20:58', hits: ['all'],
      what: 'Every machine lost power at once. The outside check stopped hearing the heartbeat, and healthchecks.io mailed Inês at 19:52.',
      fix: 'Power came back at 20:52. All three machines started by themselves (their BIOS restores power), and every app answered its health check by 20:58. Nothing was lost: the last backup was 19:00, and every app’s data checked clean.',
      alerts: ['19:52 The heartbeat stopped (healthchecks.io, by mail)', '20:58 Resolved: every app is working again'] },
    { id: 'in-public', title: 'The public path failed from outside', level: 'warning', start: '2026-11-03T11:05', end: '2026-11-03T11:31', hits: ['public'],
      what: 'Home and Paperless didn’t answer from the internet; on the mesh everything worked.',
      fix: 'NetBird’s proxy cluster eu1 had trouble on its side and recovered by itself. Nothing to do here.', alerts: ['11:10 Home doesn’t answer from outside', '11:31 Resolved'] },
    { id: 'in-pi', title: 'Kitchen Pi offline', level: 'warning', start: '2026-10-22T07:10', end: '2026-10-22T07:24', hits: ['kitchen-pi', 'donetick', 'actual', 'karakeep'],
      what: 'Kitchen Pi stopped answering over the mesh; Attic NAS noticed after 5 minutes.', fix: 'It had been unplugged while cleaning. Back when plugged in.', alerts: ['07:15 Kitchen Pi is offline', '07:24 Resolved'] },
    { id: 'in-jf', title: 'Jellyfin’s update rolled back', level: 'warning', start: '2026-10-14T03:02', end: '2026-10-14T03:06', hits: ['jellyfin'], planned: true,
      what: 'The update to 10.11.1 didn’t answer its health check within 4 minutes, in the maintenance window.', fix: 'HomersLab returned Jellyfin to its snapshot from 03:00. The next tested version, a week later, went through.', alerts: ['03:06 Jellyfin’s update was rolled back (in the morning digest)'] },
    { id: 'in-cert', title: 'A certificate renewal failed once', level: 'warning', start: '2026-11-01T04:00', end: '2026-11-01T10:02', hits: [], noDowntime: true,
      what: 'Infomaniak’s API timed out when HomersLab asked for the challenge record. The certificate still had 30 days.', fix: 'The retry six hours later worked. No alert: it was renewed long before the 14-day warning.', alerts: [] },
    { id: 'in-restore', title: 'Paperless-ngx’s restore test failed', level: 'warning', start: '2026-11-08T04:12', end: null, hits: [], noDowntime: true,
      what: 'The restored copy did not pass its health check within 10 minutes: Paperless was still moving its database to a new layout. The app itself kept working.', fix: 'Open: run the test again from its page.', alerts: ['04:13 Paperless-ngx’s restore test failed (mail and ntfy)'] },
  ];
  const t = (s) => new Date(s);
  const minutes = (i) => ((i.end ? t(i.end) : NOW) - t(i.start)) / 60000;
  const affects = (i, id, nodeId) => !i.noDowntime && (i.hits.includes('all') || i.hits.includes(id) || (nodeId && i.hits.includes(nodeId)));
  const DAY = 86400000;
  function uptime(id, nodeId, since) {
    const from = (days) => Math.max(NOW - days * DAY, since ? +t(since) : 0);
    const pct = (days) => { const span = (NOW - from(days)) / 60000; const down = incidents.filter((i) => affects(i, id, nodeId) && +t(i.start) >= from(days)).reduce((s, i) => s + minutes(i), 0); return Math.max(0, 100 - (down / span) * 100); };
    // ninety days, oldest first: ok, partial (a short incident), down (an hour or more), or none (not installed yet)
    const days = Array.from({ length: 90 }, (_, k) => {
      const d0 = new Date(NOW); d0.setHours(0, 0, 0, 0); d0.setDate(d0.getDate() - (89 - k));
      if (since && d0 < new Date(new Date(since).setHours(0, 0, 0, 0))) return 'none';
      const hit = incidents.filter((i) => affects(i, id, nodeId) && new Date(t(i.start)).setHours(0, 0, 0, 0) === +d0);
      return !hit.length ? 'ok' : hit.some((i) => minutes(i) >= 60) ? 'down' : 'partial';
    });
    return { d1: pct(1), d30: pct(30), y: pct(365), days };
  }
  // response times: 24 hourly medians, deterministic
  const spark = (base, seed) => Array.from({ length: 24 }, (_, i) => { const x = Math.sin(seed * 9.1 + i * 1.7) * 0.22 + Math.sin(i / 3.2 + seed) * 0.12; return Math.round(base * (1 + x) + (i === 3 ? base * 0.6 : 0)); });
  const SINCE = { paperless: '2026-03-14', jellyfin: '2026-04-02', plex: '2026-06-18', navidrome: '2026-04-02', kavita: '2026-04-09', donetick: '2026-04-12', actual: '2026-05-03', memos: '2026-10-27', karakeep: '2026-10-20' };
  const up = {
    apps: D.installed.map((a, i) => ({ id: a.id, node: a.node, ms: a.health.ms, spark: spark(a.health.ms, i + 1), ...uptime(a.id, a.node, SINCE[a.id] + 'T12:00') })),
    nodes: D.nodes.map((n, i) => ({ id: n.id, ms: [4, 9, 12][i], spark: spark([4, 9, 12][i], i + 20), ...uptime(n.id, n.id) })),
    public: { id: 'public', ms: 96, spark: spark(96, 40), ...uptime('public', null) },
  };

  // ---------- the outside heartbeat ----------
  const heartbeat = { service: 'healthchecks.io', check: 'Ferreira primary', every: '5 minutes', grace: '5 minutes', last: '14:05', state: 'up', to: 'Inês, by mail and ntfy',
    note: 'The check only learns that pings arrive: nothing about the household’s machines is published.', watchers: 'Attic NAS and Kitchen Pi also watch Study PC over the mesh' };

  // ---------- alerts: per admin, per level, and the maintenance window ----------
  const alerts = {
    channels: [
      { id: 'mail', name: 'Mail', detail: 'ines@ferreira.family', icon: 'mail' },
      { id: 'ntfy', name: 'ntfy', detail: 'ntfy.ferreira.family, topic “homerslab-ines”', icon: 'bell' },
      { id: 'desktop', name: 'The desktop app', detail: 'On Inês’s laptop', icon: 'desktop' },
      { id: 'webhook', name: 'A webhook', detail: 'Not set up: Matrix, a Signal bot or Home Assistant', icon: 'link', off: true },
    ],
    levels: [
      { id: 'critical', name: 'Critical', words: 'The primary down, a disk failing, security events. Pushes even in quiet hours.', on: { mail: true, ntfy: true, desktop: true, webhook: false }, locked: 'mail' },
      { id: 'warning', name: 'Warning', words: 'A node or the public path down, low space, expiries, a restore test failing, a request.', on: { mail: true, ntfy: true, desktop: false, webhook: false } },
      { id: 'digest', name: 'Morning digest', words: 'Everything from the night, and what waited through quiet hours, at 07:00.', on: { mail: true, ntfy: false, desktop: false, webhook: false } },
    ],
    quiet: { from: '22:00', to: '07:00' },
    window: { from: '02:00', to: '05:00', tonight: ['Jellyfin 10.11.3 (automatic updates are off: it waits for you)', 'The weekly scrub of Attic NAS’s app data', 'Restore tests on Sunday'] },
    examples: [
      { id: 'primary', level: 'critical', icon: 'alert', title: 'Study PC isn’t answering', body: 'The heartbeat stopped at 19:42. Attic NAS can’t reach it over the mesh either. Apps on Study PC are down; Kitchen Pi and Attic NAS are fine.', via: 'healthchecks.io, mail · ntfy', when: '19:52' },
      { id: 'cert', level: 'warning', icon: 'lock', title: 'A certificate expires in 14 days', body: '*.ferreira.pt expires on 23 Nov and its renewal failed twice: Cloudflare refused the token. Replace it in Settings › Domains.', via: 'mail · ntfy', when: '09:00' },
      { id: 'device', level: 'critical', icon: 'laptop', title: 'A new device joined the mesh', body: 'Lúcia’s new phone (iPhone 16) joined NetBird with her account at 13:12. If that wasn’t her, revoke it.', via: 'mail · ntfy · desktop', when: '13:12' },
      { id: 'resolved', level: 'resolved', icon: 'check', title: 'Resolved: the public path works again', body: 'Home and Paperless answer from the internet again, after 26 minutes. NetBird’s proxy recovered by itself.', via: 'mail · ntfy', when: '11:31' },
    ],
    digest: { at: '07:00', items: ['Jellyfin updated to 10.11.3 at 02:14; healthy', 'The scrub of Attic NAS’s app data repaired 2 blocks from the other disk', 'Bay 1’s disk reports 24 reallocated sectors, up from 0 a month ago', 'Media pool is 91% full: about 63 days left at this rate', 'Nobody signed in from a new place'] },
  };

  // ---------- domains: only HomersLab's own records, every domain equal, one sign-in domain ----------
  const OWN = (id) => `homerslab owner=ferreira id=${id}`;
  const domains = [
    { name: 'ferreira.family', main: true, dns: 'Infomaniak', token: 'Held on Study PC since 2 Mar', cert: { name: '*.ferreira.family', issuer: 'Let’s Encrypt', until: '7 Jan 2027', renewed: '8 Nov' }, added: '2 Mar',
      records: [
        { name: '_acme-challenge', type: 'TXT', value: 'Written for each renewal, removed after', owner: 'homerslab', marker: OWN('c1') },
        { name: '*', type: 'CNAME', value: 'eu1.proxy.netbird.io', owner: 'homerslab', marker: OWN('p1'), why: 'Paperless-ngx is public through NetBird’s proxy' },
        { name: '_netbird-validate', type: 'TXT', value: 'nb-verify=7f3a…', owner: 'homerslab', marker: OWN('p2'), why: 'Proves the domain to NetBird’s proxy' },
        { name: '@', type: 'MX', value: 'mx1.mail.ovh.net (10)', owner: 'other' },
        { name: '@', type: 'TXT', value: 'v=spf1 include:mx.ovh.com ~all', owner: 'other' },
        { name: 'ovh._domainkey', type: 'TXT', value: 'v=DKIM1; k=rsa; p=MIIB…', owner: 'other' },
        { name: '_dmarc', type: 'TXT', value: 'v=DMARC1; p=quarantine', owner: 'other' },
        { name: '@', type: 'A', value: '185.31.40.12 (the family’s web page)', owner: 'other' },
      ] },
    { name: 'ferreira.pt', main: false, dns: 'Cloudflare', token: 'Held on Study PC since 4 Nov', cert: { name: '*.ferreira.pt', issuer: 'Let’s Encrypt', until: '23 Nov 2026', renewed: '24 Aug', failing: 'Cloudflare refused the token twice: it may have been revoked' }, added: '4 Nov',
      records: [
        { name: '_acme-challenge', type: 'TXT', value: 'Written for each renewal, removed after', owner: 'homerslab', marker: OWN('c2') },
        { name: '*', type: 'CNAME', value: 'eu1.proxy.netbird.io', owner: 'homerslab', marker: OWN('p3'), why: 'Paperless-ngx is public through NetBird’s proxy' },
        { name: 'documents', type: 'A', value: '93.184.216.34', owner: 'other', blocks: 'documents.ferreira.pt', refused: 'This name already points somewhere else, in a record HomersLab didn’t create, so Paperless-ngx can’t answer at documents.ferreira.pt. Remove the record at Cloudflare if it’s no longer used; HomersLab will never change it.' },
        { name: '@', type: 'MX', value: 'route1.mx.cloudflare.net (12)', owner: 'other' },
        { name: '@', type: 'TXT', value: 'v=spf1 include:_spf.mx.cloudflare.net ~all', owner: 'other' },
      ] },
  ];

  // ---------- what the analyzer finds: a tree per disk. owner: an app id, a storage location ('st:<id>'), 'leftover' or 'system' ----------
  const F = (name, gb, o = {}) => ({ name, gb, ...o });
  const analyzer = {
    'an-media': F('Media pool', 19840, { kids: [
      F('Films', 8400, { owner: 'st:media', type: 'Video', age: '2 days', kids: [F('The Grand Budapest Hotel (2014).mkv', 14.2, { type: 'Video', age: '3 years', dupOf: 'dl-gbh' }), F('Spirited Away (2001).mkv', 11.8, { type: 'Video', age: '4 years' }), F('Interstellar (2014) 4K.mkv', 62.4, { type: 'Video', age: '2 years' }), F('Toy Story (1995).mkv', 9.1, { type: 'Video', age: '5 years' }), F('Other films (612)', 8302.5, { type: 'Video', age: 'Various', group: true })] }),
      F('Series', 7920, { owner: 'st:media', type: 'Video', age: '1 day', kids: [F('Bluey (3 seasons)', 182, { type: 'Video', age: '1 year' }), F('Planet Earth II', 241, { type: 'Video', age: '6 years', old: true }), F('Other series (48)', 7497, { type: 'Video', age: 'Various', group: true })] }),
      F('Home videos', 1460, { owner: 'st:media', type: 'Video', age: '3 weeks', kids: [F('2008–2012, from the camcorder', 640, { type: 'Video', age: '14 years', old: true }), F('Wedding 2009.mov', 38.6, { type: 'Video', age: '17 years', old: true, dupOf: 'hv-wed' }), F('Wedding 2009 (copy).mov', 38.6, { type: 'Video', age: '17 years', old: true, dup: 'hv-wed' }), F('Phones, 2013–2025', 742.8, { type: 'Video', age: '3 weeks' })] }),
      F('Music', 1120, { owner: 'st:media', type: 'Audio', age: '4 days', kids: [F('Lossless (1,840 albums)', 1040, { type: 'Audio', age: 'Various' }), F('Old MP3 copies of the same albums', 80, { type: 'Audio', age: '9 years', old: true })] }),
      F('Books', 210, { owner: 'st:media', type: 'Books', age: '2 weeks' }),
      F('Downloads', 412, { owner: 'st:media', type: 'Mixed', age: '5 months', kids: [F('The.Grand.Budapest.Hotel.2014.1080p.mkv', 14.2, { type: 'Video', age: '3 years', dup: 'dl-gbh' }), F('ubuntu-22.04-desktop-amd64.iso', 4.7, { type: 'Disk image', age: '3 years', old: true }), F('Old torrents (unfinished)', 393.1, { type: 'Mixed', age: '2 years', old: true })] }),
      F('Scanner inbox', 6.4, { owner: 'st:scans', type: 'Documents', age: 'Today' }),
      F('Jellyfin’s transcode cache', 182, { owner: 'jellyfin', type: 'Cache', age: 'Today', inApp: true, cleanup: 'Clear Jellyfin’s transcode cache' }),
      F('Old move copy: Navidrome (kept until 12 Nov)', 0.6, { owner: 'leftover', type: 'Move copy', age: '5 days', leftover: 'The copy kept for 7 days after Navidrome moved to App data' }),
      F('Btrfs snapshots before updates', 129, { owner: 'leftover', type: 'Snapshots', age: 'Up to 30 days', leftover: 'Restore points HomersLab took before updates', kids: [F('Older than 14 days (6)', 92, { type: 'Snapshots', age: '15–30 days', old: true }), F('Last 14 days (4)', 37, { type: 'Snapshots', age: 'Under 14 days' })] }),
    ] }),
    'an-apps': F('App data', 46, { kids: [
      F('Jellyfin', 2.1, { owner: 'jellyfin', type: 'App data', inApp: true, cleanup: 'Jellyfin’s own cleanup: old images and the metadata cache' }),
      F('Navidrome', 0.6, { owner: 'navidrome', type: 'App data', inApp: true, cleanup: 'Navidrome’s own cleanup: the artwork cache' }),
      F('Kavita', 0.9, { owner: 'kavita', type: 'App data', inApp: true, cleanup: 'Kavita’s own cleanup: covers and the bookmark cache' }),
      F('Logs', 1.4, { owner: 'leftover', type: 'Logs', age: '60 days', leftover: 'Logs older than 14 days', old: true }),
      F('Snapshots', 41, { owner: 'system', type: 'Snapshots', age: 'Hourly', note: 'The hourly snapshots backups are made from; HomersLab ages them out itself' }),
    ] }),
    'sp-data': F('Data disk', 31, { kids: [
      F('Paperless-ngx', 18.4, { owner: 'paperless', type: 'App data', inApp: true, cleanup: 'Paperless’s own cleanup: empty the trash (34 documents, 210 MB)', perPerson: { ines: 6.2, tomas: 5.9, lucia: 4.1, family: 2.2 } }),
      F('Plex', 1.2, { owner: 'plex', type: 'App data', inApp: true, cleanup: 'Plex’s own cleanup: “Optimize database” and old bundles' }),
      F('The household’s sign-in', 0.4, { owner: 'system', type: 'App data' }),
      F('Snapshots', 11, { owner: 'system', type: 'Snapshots', age: 'Hourly', note: 'The hourly snapshots backups are made from' }),
    ] }),
    'kp-nvme': F('NVMe disk', 61, { kids: [
      F('System', 9.8, { owner: 'system', type: 'System' }),
      F('Mealie, removed 2 Oct (data kept)', 3.4, { owner: 'leftover', type: 'Removed app', age: '5 weeks', leftover: 'Mealie’s data, kept when it was removed', removedApp: 'mealie' }),
      F('Karakeep', 3.8, { owner: 'karakeep', type: 'App data', inApp: true, cleanup: 'Karakeep’s own cleanup: archived page snapshots' }),
      F('Actual Budget', 0.2, { owner: 'actual', type: 'App data', inApp: true }),
      F('Donetick', 0.1, { owner: 'donetick', type: 'App data', inApp: true }),
      F('Memos', 0.3, { owner: 'memos', type: 'App data', inApp: true }),
      F('Container images not used by any app', 2.6, { owner: 'leftover', type: 'Images', age: '2 months', leftover: 'Images left from older versions' }),
      F('Snapshots', 40.8, { owner: 'system', type: 'Snapshots', age: 'Hourly' }),
    ] }),
  };
  // space per person, as far as apps report it (GB)
  const perPerson = [{ id: 'ines', gb: 6.2, from: 'Paperless 6.2 GB' }, { id: 'tomas', gb: 5.9, from: 'Paperless 5.9 GB' }, { id: 'lucia', gb: 4.1, from: 'Paperless 4.1 GB' }, { id: 'rita', gb: 0, from: 'Nothing reported' }, { id: 'duarte', gb: 0, from: 'Nothing reported' }];

  Object.assign(D, { incidents, uptime: up, heartbeat, alerts, domains, analyzer, perPerson, minutes });

  // keep the figures honest
  const sum = (n) => (n.kids ? n.kids.reduce((s, k) => s + sum(k), 0) : n.gb);
  for (const [id, root] of Object.entries(analyzer)) {
    const disk = D.nodes.flatMap((n) => n.disks).find((d) => d.id === id);
    console.assert(disk && Math.abs(sum(root) - disk.usedGB) < 0.6, 'the analyzer’s tree adds up to the disk’s use', id, sum(root), disk && disk.usedGB);
    for (const k of root.kids) if (k.kids) console.assert(Math.abs(sum(k) - k.gb) < 0.6, 'a folder is the sum of what is in it', k.name, sum(k), k.gb);
  }
  console.assert(up.apps.every((a) => a.d30 > 99 && a.d1 === 100), 'every app was up today and above 99% for 30 days');
  console.assert(Math.round(minutes(incidents[0])) === 76, 'the power cut lasted 76 minutes');
})();
