// HomersLab prototypes: one fake household, the official catalog and a third-party one. Backups run every hour.
// PROTOTYPE, throwaway: every name, address, key and figure here is invented. Totals are computed from the rows,
// and the facts that appear twice are one value. Read the console: the asserts at the bottom must stay silent.
(() => {
  const NOW = new Date('2026-11-09T14:06:00');
  // the last twelve Sundays, oldest first: restore tests run on Sundays at 04:00
  const SUNDAYS = Array.from({ length: 12 }, (_, i) => { const d = new Date('2026-11-08T04:00:00'); d.setDate(d.getDate() - (11 - i) * 7); return d; });

  const household = {
    name: 'The Ferreira household', short: 'Ferreira', domain: 'ferreira.family',
    idp: { id: 'authentik', name: 'Authentik', version: '2026.8.5' },
    meshes: [
      { id: 'netbird', name: 'NetBird', plane: 'cloud', note: "NetBird's cloud", since: '2026-03-02' },
      { id: 'tailscale', name: 'Tailscale', plane: 'cloud', note: "Tailscale's cloud", tailnet: 'tail8f3c.ts.net', since: '2026-03-04' },
    ],
    mailFrom: 'HomersLab <homerslab@ferreira.family>',
    signup: 'invite', // 'invite' | 'domains' | 'off'
    signupDomains: ['ferreira.family'],
    version: '1.0.0',
    adminUrl: 'admin.ferreira.family',
    homeUrl: 'home.ferreira.family',
    recoveryKey: 'age1q7m3…k2vz', // the public half; the private half is in the recovery kit only
  };

  // hues at least 60 degrees apart in list order; never 22-62 (cream) or a saturated violet
  const people = [
    { id: 'ines', name: 'Inês Ferreira', short: 'Inês', role: 'admin', mail: 'ines@ferreira.family', hue: 195, joined: '2026-03-02', lastSeen: 'Now, on this laptop',
      passkeys: [{ name: 'MacBook Air (iCloud Keychain)', added: '2 Mar' }, { name: 'YubiKey 5C', added: '2 Mar' }] },
    { id: 'tomas', name: 'Tomás Ferreira', short: 'Tomás', role: 'adult', mail: 'tomas@ferreira.family', ntfy: true, hue: 75, joined: '2026-03-03', lastSeen: 'Today 13:58',
      passkeys: [{ name: 'Pixel 9 (Google Password Manager)', added: '3 Mar' }, { name: 'Windows Hello on his desktop', added: '4 Mar' }] },
    { id: 'lucia', name: 'Lúcia Ferreira', short: 'Lúcia', role: 'adult', mail: 'lucia@ferreira.family', hue: 330, joined: '2026-03-09', lastSeen: 'Today 10:05',
      passkeys: [{ name: 'iPad (iCloud Keychain)', added: '9 Mar' }] },
    { id: 'rita', name: 'Rita Ferreira', short: 'Rita', role: 'kid', age: 12, mail: 'rita@ferreira.family', hue: 140, joined: '2026-03-10', lastSeen: 'Today 13:40',
      passkeys: [{ name: 'Galaxy Tab (Samsung Pass)', added: '10 Mar' }] },
    { id: 'duarte', name: 'Duarte Ferreira', short: 'Duarte', role: 'kid', age: 8, mail: null, hue: 230, joined: '2026-03-10', lastSeen: 'Yesterday 19:20',
      passkeys: [{ name: 'Security key (kept in the living room)', added: '10 Mar' }] },
  ];

  const devices = [
    { id: 'ines-laptop', person: 'ines', name: 'Inês’s laptop', model: 'MacBook Air', kind: 'laptop', mesh: 'netbird', addr: '100.88.12.31', seen: 'Now', current: true },
    { id: 'ines-phone', person: 'ines', name: 'Inês’s phone', model: 'iPhone 15', kind: 'phone', mesh: 'netbird', addr: '100.88.12.32', seen: '14:01' },
    { id: 'tomas-phone', person: 'tomas', name: 'Tomás’s phone', model: 'Pixel 9', kind: 'phone', mesh: 'tailscale', addr: '100.101.7.40', seen: '13:58' },
    { id: 'tomas-desktop', person: 'tomas', name: 'Tomás’s desktop', model: 'Windows 11 PC', kind: 'desktop', mesh: 'tailscale', addr: '100.101.7.41', seen: 'Yesterday 22:14' },
    { id: 'living-room-tv', person: 'tomas', name: 'Living-room TV', model: 'Android TV', kind: 'tv', mesh: 'netbird', addr: '100.88.12.50', seen: '12:30' },
    { id: 'lucia-tablet', person: 'lucia', name: 'Lúcia’s iPad', model: 'iPad (10th gen)', kind: 'tablet', mesh: 'netbird', addr: '100.88.12.60', seen: '10:05' },
    { id: 'rita-tablet', person: 'rita', name: 'Rita’s tablet', model: 'Galaxy Tab S9', kind: 'tablet', mesh: 'netbird', addr: '100.88.12.70', seen: '13:40' },
  ];

  const invites = [
    { id: 'inv-joana', name: 'Joana Ferreira', mail: 'joana.ferreira@posteo.pt', role: 'adult', sent: '6 Nov', expires: '13 Nov', by: 'ines', how: 'mail' },
  ];

  // memory and disk in GB; `base` is what the node uses before any app (its own system, HomersLab's agent and edge)
  const nodes = [
    { id: 'study-pc', name: 'Study PC', primary: true, os: 'Fedora 43', arch: 'x86_64', cpu: 'Ryzen 7 5700X, 8 cores', memGB: 32, baseMemGB: 11.2,
      baseNote: 'Its own desktop, HomersLab’s manager, the household’s sign-in (Authentik)',
      gpu: { kind: 'nvidia', name: 'NVIDIA RTX 3060, 12 GB' }, tpm: true, engine: 'Docker 29.1',
      disks: [
        { id: 'sp-sys', name: 'System disk', role: 'system', sizeGB: 1000, usedGB: 412, layout: 'single', fs: 'btrfs', enc: null, growth: 0.3, scrub: { last: 'Sun 8 Nov, 02:40', errors: 0 },
          members: [{ model: 'Samsung 980 PRO 1 TB', serial: 'S5GXNF0R412873K', slot: 'M.2 slot 1', kind: 'NVMe', smart: 'ok', tempC: 41, hours: 9120, wear: '4% worn' }] },
        { id: 'sp-data', name: 'Data disk', role: 'apps', apps: true, sizeGB: 2000, usedGB: 31, layout: 'single', fs: 'btrfs', enc: 'luks-tpm', growth: 0.4, scrub: { last: 'Sun 8 Nov, 02:55', errors: 0 },
          members: [{ model: 'Samsung 990 EVO 2 TB', serial: 'S7M3NS0X108442P', slot: 'M.2 slot 2', kind: 'NVMe', smart: 'ok', tempC: 38, hours: 5480, wear: '2% worn' }] },
        { id: 'sp-new', name: 'New disk', role: 'empty', sizeGB: 4000, usedGB: 0, state: 'empty',
          members: [{ model: 'WD Red Plus 4 TB', serial: 'WD-WX42D81KJ3P7', slot: 'SATA port 2', kind: 'Hard disk', smart: 'ok', tempC: 31, hours: 6 }] },
        { id: 'sp-usb', name: 'USB disk', role: 'foreign', sizeGB: 2000, usedGB: 412, state: 'foreign', fs: 'NTFS', label: 'Backup Tomás 2019',
          contents: [{ what: 'Fotos 2012–2019', size: '268 GB' }, { what: 'Documentos', size: '41 GB' }, { what: 'Old laptop image (Windows 7)', size: '103 GB' }],
          members: [{ model: 'Seagate Expansion 2 TB (USB)', serial: 'NA8F1KQ2', slot: 'USB, front left', kind: 'Hard disk', smart: 'ok', tempC: 34, hours: 3310 }] },
      ],
      addrs: { netbird: '100.88.12.4', tailscale: '100.101.7.22' }, joined: '2 Mar', state: 'online', load: 9 },
    { id: 'attic-nas', name: 'Attic NAS', os: 'Debian 13', arch: 'x86_64', cpu: 'Intel N100, 4 cores', memGB: 16, baseMemGB: 1.4,
      baseNote: 'Its own system, HomersLab’s agent and edge',
      gpu: { kind: 'intel', name: 'Intel UHD (N100), shared memory' }, tpm: true, engine: 'Podman 5.7, rootful',
      bays: 4,
      disks: [
        { id: 'an-sys', name: 'System disk', role: 'system', sizeGB: 240, usedGB: 18, layout: 'single', fs: 'ext4', enc: 'luks-tpm', growth: 0.05, scrub: null,
          members: [{ model: 'Kingston A400 240 GB', serial: '50026B7782A1C4F3', slot: 'Internal M.2', kind: 'SSD', smart: 'ok', tempC: 37, hours: 14200, wear: '11% worn' }] },
        { id: 'an-apps', name: 'App data', role: 'apps', apps: true, sizeGB: 1000, usedGB: 46, layout: 'mirror', fs: 'btrfs', enc: 'luks-tpm', growth: 0.2, scrub: { last: 'Sun 8 Nov, 03:10', errors: 0, repaired: 2 },
          members: [
            { model: 'Samsung 870 EVO 1 TB', serial: 'S6PTNM0T503118W', slot: 'Bay 1', bay: 1, kind: 'SSD', smart: 'warning', tempC: 36, hours: 21430, realloc: 24, pending: 8, trend: 'up from 0 on 9 Oct' },
            { model: 'Samsung 870 EVO 1 TB', serial: 'S6PTNM0T503291B', slot: 'Bay 2', bay: 2, kind: 'SSD', smart: 'ok', tempC: 35, hours: 21428 }] },
        { id: 'an-media', name: 'Media pool', role: 'media', sizeGB: 21800, usedGB: 19840, layout: 'single', fs: 'btrfs', enc: 'luks-tpm', growth: 31, scrub: { last: 'Sun 1 Nov, 02:00 (14 h)', errors: 0 },
          members: [{ model: 'WD Red Pro 22 TB', serial: '2TKX8V4N', slot: 'Bay 3', bay: 3, kind: 'Hard disk', smart: 'ok', tempC: 39, hours: 8810 }] },
        { id: 'an-bak', name: 'Backups disk', role: 'backups', sizeGB: 4000, usedGB: 26, layout: 'single', fs: 'btrfs', enc: 'luks-tpm', growth: 0.2, scrub: { last: 'Sun 8 Nov, 03:40', errors: 0 },
          members: [{ model: 'Seagate IronWolf 4 TB', serial: 'ZW60K1PQ', slot: 'Bay 4', bay: 4, kind: 'Hard disk', smart: 'ok', tempC: 34, hours: 12030 }] },
      ],
      addrs: { netbird: '100.88.12.5', tailscale: '100.101.7.23' }, joined: '5 Mar', state: 'online', load: 14 },
    { id: 'kitchen-pi', name: 'Kitchen Pi', os: 'Debian 13', arch: 'arm64', cpu: 'Raspberry Pi 5, 4 cores', memGB: 8, baseMemGB: 1.1,
      baseNote: 'Its own system, HomersLab’s agent and edge',
      gpu: null, tpm: false, engine: 'Podman 5.7, rootless', usb: 'Sonoff Zigbee 3.0 dongle at /dev/ttyUSB0',
      disks: [
        { id: 'kp-nvme', name: 'NVMe disk', role: 'apps', apps: true, sizeGB: 256, usedGB: 61, layout: 'single', fs: 'btrfs', enc: null, encWhy: 'No TPM: it would ask for a passphrase at every boot', growth: 0.1, scrub: { last: 'Sun 8 Nov, 02:20', errors: 0 },
          members: [{ model: 'Raspberry Pi NVMe 256 GB', serial: 'RPI2541A88310', slot: 'M.2 HAT', kind: 'NVMe', smart: 'ok', tempC: 52, hours: 5890, wear: '7% worn' }] },
      ],
      addrs: { netbird: '100.88.12.6', tailscale: '100.101.7.24' }, joined: '8 Mar', state: 'online', load: 21 },
  ];

  const storage = [
    { id: 'media', name: 'Media library', node: 'attic-nas', path: '/srv/media', disk: 'an-media', kind: 'Local folder, shared over the mesh', sizeGB: 21800, usedGB: 19840, access: 'Read-only for media apps', usedBy: ['jellyfin', 'plex', 'navidrome', 'kavita'] },
    { id: 'scans', name: 'Scanner inbox', node: 'attic-nas', path: '/srv/scans', kind: 'Network share (SMB) for the scanner', disk: 'an-media', sizeGB: 21800, usedGB: 19840, sharedPoolWith: 'media', access: 'Read and write for Documents', usedBy: ['paperless'] },
  ];

  const catalogs = [
    { id: 'official', name: 'Official catalog', publisher: 'HomersLab', key: 'ed25519 9F2C 4A71 0B6E D3A8', url: 'github.com/homerslab/catalog', trusted: 'Built in', synced: '13:40', apps: 0 },
    { id: 'ines', name: 'Inês’s catalog', publisher: 'Inês Ferreira', key: 'ed25519 3B8E D017 55C2 9A4F', url: 'codeberg.org/ines-ferreira/homerslab-catalog', trusted: '12 Mar, with Inês’s passkey', synced: '13:40', apps: 0 },
  ];

  // purposes: what an app is for. The catalog groups by them, and a household may install several apps for one
  // purpose side by side (two photo apps; Jellyfin and Plex), so each person can use the one they prefer (story 26a)
  const categories = [
    { id: 'films', name: 'Films and series' },
    { id: 'photos', name: 'Photos and videos' },
    { id: 'music', name: 'Music and audiobooks' },
    { id: 'books', name: 'Books and comics' },
    { id: 'documents', name: 'Documents and files' },
    { id: 'knowledge', name: 'Notes, links and knowledge' },
    { id: 'home', name: 'Home and chores' },
    { id: 'money', name: 'Money' },
    { id: 'passwords', name: 'Passwords' },
    { id: 'assistants', name: 'Assistants' },
  ];

  // the app definitions as a catalog holds them (abridged): what it is, what it needs, how it is wired
  const catalog = [
    { id: 'immich', icon: 'icons/apps/immich.svg', homeFor: 'Photos and videos from every phone', name: 'Immich', purpose: 'Photos', glyph: 'photos', category: 'photos', tier: 'verified', catalog: 'official', version: '2.3.1', tested: '2 Nov',
      blurb: 'Photos and videos from every phone, kept at home, with faces, places and shared albums.',
      about: 'Every phone in the household backs up its camera roll here. People find photos by face, place or what is in them, and share albums with each other.',
      signIn: { method: 'native', words: 'With their household account, straight into Immich. The phone app signs in the same way.' },
      kidView: null, publicAllowed: true, defaultAudience: { adults: true },
      resources: { memGB: 6, diskGB: 20, grows: 'the photo library', gpu: 'optional', gpuWhy: 'face and object recognition', arch: ['x86_64', 'arm64'] },
      data: [{ what: 'The photo library', detail: 'every original, as uploaded' }, { what: 'Immich’s database', detail: 'albums, faces, people; dumped first with pg_dump' }, { what: 'Its settings' }],
      secrets: ['the database password', 'the session secret', 'the sign-in client’s secret'],
      extras: [{ id: 'gpu', kind: 'device', needs: 'gpu', optional: true, words: 'Use the GPU on {node}', detail: '{gpu}, for face and object recognition', without: 'Without it Immich still works; recognising faces takes longer.' }],
      images: { count: 3, size: '1.9 GB' }, screenshots: ['The timeline', 'People and faces', 'A shared album'] },
    { id: 'photoprism', icon: 'icons/apps/photoprism.svg', homeFor: 'Photos, found by what is in them', name: 'PhotoPrism', purpose: 'Photos', glyph: 'photos', category: 'photos', tier: 'community', catalog: 'official', version: '251130', tested: '30 Oct',
      blurb: 'Photos found by what, where and who is in them, with places on a map and private albums.',
      about: 'Indexes the photos in a folder you choose and finds them by the things in them, the places and the people. People who prefer it to Immich can use it side by side, on the same photos.',
      signIn: { method: 'native', words: 'With their household account, straight into PhotoPrism.' },
      kidView: null, publicAllowed: true, defaultAudience: { adults: true },
      resources: { memGB: 4, diskGB: 10, grows: 'its index and thumbnails', gpu: 'optional', gpuWhy: 'recognising faces and things', arch: ['x86_64', 'arm64'] },
      data: [{ what: 'Its index, albums and labels' }, { what: 'Not the photos', detail: 'it reads the folder you choose' }, { what: 'Its database', detail: 'dumped first' }],
      secrets: ['the database password', 'the sign-in client’s secret'],
      extras: [{ id: 'gpu', kind: 'device', needs: 'gpu', optional: true, words: 'Use the GPU on {node}', detail: '{gpu}, for faces and things', without: 'Without it PhotoPrism still works; indexing takes longer.' }],
      images: { count: 2, size: '1.3 GB' }, screenshots: ['Search', 'Places', 'An album'] },
    { id: 'paperless', icon: 'icons/apps/paperless-ngx.svg', homeFor: 'Letters, bills and contracts', name: 'Paperless-ngx', purpose: 'Documents', glyph: 'documents', category: 'documents', tier: 'verified', catalog: 'official', version: '2.19.0', tested: '4 Nov',
      blurb: 'Every letter, bill and contract, scanned, read and findable in seconds.',
      about: 'Scans from the scanner inbox and mail attachments are read, titled and tagged. Everyone finds their documents by searching what is written in them.',
      signIn: { method: 'native', words: 'With their household account, straight into Paperless.' },
      kidView: null, publicAllowed: true, defaultAudience: { adults: true },
      resources: { memGB: 2, diskGB: 10, grows: 'the documents', gpu: 'none', arch: ['x86_64', 'arm64'] },
      data: [{ what: 'The documents', detail: 'originals and their archived copies' }, { what: 'Paperless’s database', detail: 'dumped first with pg_dump' }, { what: 'Its settings' }],
      secrets: ['the database password', 'the secret key', 'the sign-in client’s secret'], extras: [], images: { count: 4, size: '1.4 GB' }, screenshots: ['Search', 'A document', 'Mail rules'] },
    { id: 'opencloud', icon: 'icons/apps/opencloud.svg', homeFor: 'Folders on every computer', kidFor: 'Your own folder', name: 'OpenCloud', purpose: 'Files', glyph: 'files', category: 'documents', tier: 'verified', catalog: 'official', version: '4.0.2', tested: '30 Oct',
      blurb: 'Folders that sync between everyone’s computers and phones, with links to share.',
      about: 'A desktop app keeps folders in sync; phones see the same files. Links can be shared with people outside the household, with a password and an end date.',
      signIn: { method: 'native', words: 'With their household account, in the browser and in the desktop and phone apps.' },
      kidView: 'Only their own folder', publicAllowed: true, defaultAudience: { adults: true },
      resources: { memGB: 2, diskGB: 10, grows: 'the files', gpu: 'none', arch: ['x86_64', 'arm64'] },
      data: [{ what: 'Everyone’s files' }, { what: 'Its settings and share links' }], secrets: ['its internal keys', 'the sign-in client’s secret'], extras: [], images: { count: 1, size: '0.4 GB' }, screenshots: ['Files', 'Sharing a link'] },
    { id: 'bentopdf', icon: 'icons/apps/bentopdf.svg', homeFor: 'Merge, split and sign PDFs', name: 'BentoPDF', purpose: 'PDF tools', glyph: 'pdf', category: 'documents', tier: 'community', catalog: 'official', version: '1.9.0', tested: '28 Oct',
      blurb: 'Merge, split, sign and shrink PDFs, in the browser, without uploading them anywhere.',
      about: 'Everything happens in the person’s own browser. HomersLab only serves the page.',
      signIn: { method: 'guarded', words: 'The edge asks for their household account before the page opens. BentoPDF has no sign-in of its own.' },
      kidView: null, publicAllowed: false, defaultAudience: { adults: true },
      resources: { memGB: 0.2, diskGB: 1, gpu: 'none', arch: ['x86_64', 'arm64'] },
      data: [{ what: 'Nothing to keep', detail: 'it stores no files' }], secrets: [], extras: [], images: { count: 1, size: '0.1 GB' }, screenshots: ['Tools'] },
    { id: 'jellyfin', icon: 'icons/apps/jellyfin.svg', homeFor: 'Films and series', kidFor: 'Films and series for you', name: 'Jellyfin', purpose: 'Films & TV', glyph: 'films', category: 'films', tier: 'verified', catalog: 'official', version: '10.11.3', tested: '6 Nov',
      blurb: 'The household’s films and series, on the TV, phones and tablets.',
      about: 'Plays the media library on every screen in the house. Kids get the kids’ library on their own.',
      signIn: { method: 'native', words: 'With their household account, also in the TV and phone apps.' },
      kidView: 'Kids’ library, rated up to 12', publicAllowed: true, defaultAudience: { everyone: true },
      resources: { memGB: 2, diskGB: 8, gpu: 'optional', gpuWhy: 'transcoding films for phones', arch: ['x86_64', 'arm64'] },
      data: [{ what: 'Its settings, watch history and artwork' }, { what: 'Not the films', detail: 'they are in the Media library, read-only' }],
      secrets: ['the sign-in client’s secret'], extras: [{ id: 'gpu', kind: 'device', needs: 'gpu', optional: true, words: 'Use the GPU on {node}', detail: '{gpu}, for transcoding', without: 'Without it, phones far from home may stutter.' }],
      needsStorage: 'media', images: { count: 1, size: '0.9 GB' }, screenshots: ['Home', 'A film', 'The kids’ library'] },
    { id: 'plex', icon: 'icons/apps/plex.svg', iconFull: true, homeFor: 'Films and series, on any TV', kidFor: 'Films for you, on the TV', name: 'Plex', purpose: 'Films & TV', glyph: 'films', category: 'films', tier: 'community', catalog: 'official', version: '1.42.2', tested: '5 Nov',
      blurb: 'The household’s films and series in the Plex apps, on smart TVs, consoles and phones.',
      about: 'Plex Media Server from Plex’s own package, installed on the node itself and looked after like every other app: updates, backups, health and the edge in front. It plays the same Media library as Jellyfin, so each person uses the one they prefer.',
      vendor: 'plexmediaserver, from Plex’s own repository, signed by Plex',
      signIn: { method: 'native', words: 'With their Plex account, linked once to their household account; the TV apps sign in with a code.', second: true },
      kidView: 'Kids’ library, rated up to 12', publicAllowed: false, publicWhy: 'Its definition allows mesh only: Plex’s own remote access stays off.', defaultAudience: { adults: true },
      resources: { memGB: 2, diskGB: 10, gpu: 'optional', gpuWhy: 'transcoding films for phones and TVs', arch: ['x86_64', 'arm64'] },
      data: [{ what: 'Its library database, watch history and artwork' }, { what: 'Not the films', detail: 'they are in the Media library, read-only' }],
      secrets: ['its claim token'], extras: [{ id: 'gpu', kind: 'device', needs: 'gpu', optional: true, words: 'Use the GPU on {node}', detail: '{gpu}, for transcoding', without: 'Without it, a film for a phone far from home may stutter.' }],
      needsStorage: 'media', images: { count: 0, size: '0.3 GB' }, screenshots: ['Home', 'A film', 'On the TV'] },
    { id: 'navidrome', icon: 'icons/apps/navidrome.svg', homeFor: 'The music library', kidFor: 'All the music', name: 'Navidrome', purpose: 'Music', glyph: 'music', category: 'music', tier: 'community', catalog: 'official', version: '0.58.0', tested: '2 Nov',
      blurb: 'The music library, streamed to any phone or browser.',
      about: 'Plays the music in the Media library. Phone apps that speak Subsonic work too.',
      signIn: { method: 'guarded', words: 'The edge asks for their household account; Navidrome trusts the edge. Phone apps use a password HomersLab makes for each person.' },
      kidView: null, publicAllowed: true, defaultAudience: { adults: true },
      resources: { memGB: 0.5, diskGB: 2, gpu: 'none', arch: ['x86_64', 'arm64'] },
      data: [{ what: 'Playlists, favourites and play counts' }, { what: 'Not the music', detail: 'it is in the Media library, read-only' }], secrets: [], extras: [], needsStorage: 'media', images: { count: 1, size: '0.1 GB' }, screenshots: ['Albums', 'Now playing'] },
    { id: 'kavita', icon: 'icons/apps/kavita.svg', homeFor: 'Books and comics', kidFor: 'Books and comics for you', name: 'Kavita', purpose: 'Books', glyph: 'books', category: 'books', tier: 'community', catalog: 'official', version: '0.8.8', tested: '27 Oct',
      blurb: 'Books and comics to read in the browser, with the place kept on every device.',
      about: 'Reads the Books folder of the Media library. Kids see the kids’ shelf only.',
      signIn: { method: 'native', words: 'With their household account, straight into Kavita.' },
      kidView: 'Kids’ shelf only', publicAllowed: true, defaultAudience: { everyone: true },
      resources: { memGB: 0.6, diskGB: 2, gpu: 'none', arch: ['x86_64', 'arm64'] },
      data: [{ what: 'Reading progress and collections' }, { what: 'Not the books', detail: 'they are in the Media library, read-only' }], secrets: ['the sign-in client’s secret'], extras: [], needsStorage: 'media', images: { count: 1, size: '0.3 GB' }, screenshots: ['Library', 'Reader'] },
    { id: 'calibreweb', icon: 'icons/apps/calibre-web.svg', homeFor: 'The e-book library', kidFor: 'Books to read', name: 'Calibre-Web', purpose: 'E-books', glyph: 'books', category: 'books', tier: 'community', catalog: 'official', version: '0.6.25', tested: '28 Oct',
      blurb: 'The e-book library, read in the browser or sent to an e-reader.',
      about: 'Reads a Calibre library from the Media library and sends books to Kindles and Kobos. People who prefer it to Kavita can use it side by side, on the same books.',
      signIn: { method: 'guarded', words: 'The edge asks for their household account; Calibre-Web trusts the edge.' },
      kidView: 'Kids’ shelf only', publicAllowed: true, defaultAudience: { adults: true },
      resources: { memGB: 0.3, diskGB: 1, gpu: 'none', arch: ['x86_64', 'arm64'] },
      data: [{ what: 'Its settings and shelves' }, { what: 'Not the books', detail: 'they are in the Media library' }], secrets: [], extras: [], needsStorage: 'media', images: { count: 1, size: '0.2 GB' }, screenshots: ['Library', 'Reader'] },
    { id: 'audiobookshelf', icon: 'icons/apps/audiobookshelf.svg', homeFor: 'Audiobooks and podcasts', kidFor: 'Stories to listen to', name: 'Audiobookshelf', purpose: 'Audiobooks and podcasts', glyph: 'audiobooks', category: 'music', tier: 'verified', catalog: 'official', version: '2.30.0', tested: '5 Nov',
      blurb: 'Audiobooks and podcasts, with the place kept between phone and car.',
      about: 'Listens where you left off, on the phone app or in the browser.',
      signIn: { method: 'native', words: 'With their household account, in the browser and the phone app.' },
      kidView: 'Kids’ shelf only', publicAllowed: true, defaultAudience: { everyone: true },
      resources: { memGB: 0.5, diskGB: 4, gpu: 'none', arch: ['x86_64', 'arm64'] },
      data: [{ what: 'Progress, podcasts and their downloads' }, { what: 'Its settings' }], secrets: ['the sign-in client’s secret'], extras: [], needsStorage: 'media', images: { count: 1, size: '0.3 GB' }, screenshots: ['Library', 'Player'] },
    { id: 'homeassistant', icon: 'icons/apps/home-assistant.svg', homeFor: 'Lights, heating and sensors', name: 'Home Assistant', purpose: 'House', glyph: 'homeauto', category: 'home', tier: 'community', catalog: 'official', version: '2026.11.1', tested: '6 Nov',
      blurb: 'Lights, heating and sensors in one place, working even when the internet is down.',
      about: 'Finds devices on the home network and talks to Zigbee and Thread devices through a USB stick.',
      signIn: { method: 'guarded', words: 'The edge asks for their household account first. Home Assistant then asks for its own sign-in once on each device.', second: true },
      kidView: null, publicAllowed: false, publicWhy: 'Its definition allows mesh only: it can switch things in the house.', defaultAudience: { adults: true },
      resources: { memGB: 2, diskGB: 8, gpu: 'none', arch: ['x86_64', 'arm64'] },
      data: [{ what: 'Its configuration and history' }, { what: 'Its database', detail: 'stopped for a moment while it is copied' }], secrets: ['the owner’s first password'],
      extras: [
        { id: 'hostnet', kind: 'network', optional: false, words: 'Use the home network of {node} directly', detail: 'To find lights, speakers and TVs on the LAN. It then listens on {node}’s own addresses, not only behind the edge.', without: 'Home Assistant can’t find devices without it.' },
        { id: 'usb', kind: 'device', optional: true, words: 'Use the USB stick on {node}', detail: 'Sonoff Zigbee 3.0 dongle at /dev/ttyUSB0', without: 'Without it, no Zigbee devices.' },
      ],
      images: { count: 2, size: '1.7 GB' }, screenshots: ['Overview', 'Energy', 'A room'] },
    { id: 'donetick', icon: 'icons/apps/donetick.svg', homeFor: 'Chores, and whose turn it is', kidFor: 'Your jobs this week', name: 'Donetick', purpose: 'Chores', glyph: 'chores', category: 'home', tier: 'community', catalog: 'official', version: '0.1.52', tested: '1 Nov',
      blurb: 'Who does what this week, with reminders and a fair share for everyone.',
      about: 'Chores repeat on their own schedule. Kids see and tick off only their own.',
      signIn: { method: 'native', words: 'With their household account, in the browser and the phone app.' },
      kidView: 'Only their own chores', publicAllowed: true, defaultAudience: { everyone: true },
      resources: { memGB: 0.2, diskGB: 1, gpu: 'none', arch: ['x86_64', 'arm64'] },
      data: [{ what: 'Chores, history and points' }], secrets: ['the sign-in client’s secret'], extras: [], images: { count: 1, size: '0.1 GB' }, screenshots: ['This week'] },
    { id: 'mealie', kidOk: true, icon: 'icons/apps/mealie.svg', homeFor: 'Recipes and meal plans', name: 'Mealie', purpose: 'Recipes', glyph: 'recipes', category: 'home', tier: 'community', catalog: 'official', version: '3.4.0', tested: '29 Oct',
      blurb: 'The household’s recipes, meal plans and shopping lists.',
      about: 'Imports recipes from a link and plans the week’s meals.',
      signIn: { method: 'native', words: 'With their household account, straight into Mealie.' },
      kidView: null, publicAllowed: true, defaultAudience: { adults: true },
      resources: { memGB: 0.5, diskGB: 2, gpu: 'none', arch: ['x86_64', 'arm64'] },
      data: [{ what: 'Recipes, photos and meal plans' }, { what: 'Its database' }], secrets: ['the sign-in client’s secret'], extras: [], images: { count: 1, size: '0.4 GB' }, screenshots: ['Recipes', 'Meal plan'] },
    { id: 'grocy', icon: 'icons/apps/grocy.svg', homeFor: 'What is in the cupboard', name: 'Grocy', purpose: 'Groceries and stock', glyph: 'groceries', category: 'home', tier: 'community', catalog: 'official', version: '4.5.0', tested: '20 Oct',
      blurb: 'What is in the cupboard, what runs out, and the shopping list that follows.',
      about: 'Scan what you buy; it tells you what to buy next.',
      signIn: { method: 'guarded', words: 'The edge asks for their household account; Grocy trusts the edge.' },
      kidView: null, publicAllowed: true, defaultAudience: { adults: true },
      resources: { memGB: 0.2, diskGB: 1, gpu: 'none', arch: ['x86_64', 'arm64'] },
      data: [{ what: 'Stock, products and lists' }], secrets: [], extras: [], images: { count: 1, size: '0.1 GB' }, screenshots: ['Stock'] },
    { id: 'vaultwarden', icon: 'icons/apps/vaultwarden.svg', homeFor: 'Passwords for everyone', name: 'Vaultwarden', purpose: 'Passwords', glyph: 'passwords', category: 'passwords', tier: 'verified', catalog: 'official', version: '1.35.1', tested: '3 Nov',
      blurb: 'A password manager for the whole household, in every browser and phone.',
      about: 'Works with the Bitwarden apps. Each vault is encrypted with its person’s own master password.',
      signIn: { method: 'native', words: 'With their household account, then their own master password, which encrypts their vault. HomersLab never sees it.', second: true },
      kidView: null, publicAllowed: true, defaultAudience: { adults: true },
      resources: { memGB: 0.2, diskGB: 1, gpu: 'none', arch: ['x86_64', 'arm64'] },
      data: [{ what: 'The encrypted vaults', detail: 'unreadable without each master password' }, { what: 'Its settings' }], secrets: ['the admin key', 'the sign-in client’s secret'], extras: [], images: { count: 1, size: '0.1 GB' }, screenshots: ['Vault'] },
    { id: 'actual', icon: 'icons/apps/actual-budget.svg', homeFor: 'The household budget', name: 'Actual Budget', purpose: 'Budget', glyph: 'budget', category: 'money', tier: 'community', catalog: 'official', version: '25.11.0', tested: '3 Nov',
      blurb: 'A budget that lives at home: every euro given a job, every month.',
      about: 'Envelope budgeting, with bank files imported by hand.',
      signIn: { method: 'native', words: 'With their household account, straight into Actual.' },
      kidView: null, publicAllowed: true, defaultAudience: { adults: true },
      resources: { memGB: 0.3, diskGB: 1, gpu: 'none', arch: ['x86_64', 'arm64'] },
      data: [{ what: 'The budget files' }], secrets: ['the sign-in client’s secret'], extras: [], images: { count: 1, size: '0.2 GB' }, screenshots: ['Budget', 'Reports'] },
    { id: 'bills', icon: 'icons/apps/bills.svg', homeFor: 'Bills due this week', name: 'Bill tracker', purpose: 'Bills', glyph: 'bills', category: 'money', tier: 'third', catalog: 'ines', version: '0.4.0', tested: '—',
      blurb: 'Reads bills from the household’s mail and says what is due this week.',
      about: 'Inês’s own app, published in her own catalog.',
      signIn: { method: 'guarded', words: 'The edge asks for their household account; it has no sign-in of its own.' },
      kidView: null, publicAllowed: false, publicWhy: 'Its definition allows mesh only.', defaultAudience: { admins: true },
      resources: { memGB: 0.4, diskGB: 1, gpu: 'none', arch: ['x86_64', 'arm64'] },
      data: [{ what: 'Bills seen and their state' }], secrets: ['the mail password (given at install)'], extras: [], images: { count: 1, size: '0.2 GB' }, screenshots: ['This week'] },
    { id: 'karakeep', icon: 'icons/apps/karakeep.svg', homeFor: 'Saved links and pages', name: 'Karakeep', purpose: 'Bookmarks', glyph: 'bookmarks', category: 'knowledge', tier: 'community', catalog: 'official', version: '0.27.1', tested: '25 Oct',
      blurb: 'Save a link, a note or a picture; find it again by what it says.',
      about: 'Keeps a copy of every saved page, so links never die.',
      signIn: { method: 'native', words: 'With their household account, in the browser, the extension and the phone app.' },
      kidView: null, publicAllowed: true, defaultAudience: { adults: true },
      resources: { memGB: 2, diskGB: 4, grows: 'saved pages', gpu: 'none', arch: ['x86_64', 'arm64'] },
      data: [{ what: 'Bookmarks and saved copies of pages' }, { what: 'Its search index', detail: 'rebuilt if lost, not backed up' }], secrets: ['the search key', 'the session secret', 'the sign-in client’s secret'], extras: [], images: { count: 3, size: '1.1 GB' }, screenshots: ['Bookmarks'] },
    { id: 'memos', icon: 'icons/apps/memos.png', homeFor: 'Quick notes and lists', name: 'Memos', purpose: 'Notes', glyph: 'notes', category: 'knowledge', tier: 'community', catalog: 'official', version: '0.26.0', tested: '24 Oct',
      blurb: 'Quick notes and lists, shared with the household or kept to yourself.',
      about: 'A timeline of short notes, private or shared.',
      signIn: { method: 'native', words: 'With their household account, straight into Memos.' },
      kidView: null, publicAllowed: true, defaultAudience: { adults: true },
      resources: { memGB: 0.2, diskGB: 1, gpu: 'none', arch: ['x86_64', 'arm64'] },
      data: [{ what: 'Notes and attachments' }], secrets: ['the sign-in client’s secret'], extras: [], images: { count: 1, size: '0.1 GB' }, screenshots: ['Timeline'] },
    { id: 'outline', icon: 'icons/apps/outline.svg', homeFor: 'How things work at home', name: 'Outline', purpose: 'Household wiki', glyph: 'wiki', category: 'knowledge', tier: 'community', catalog: 'official', version: '0.88.1', tested: '22 Oct',
      blurb: 'How the boiler works, the Wi-Fi password, the dentist’s number: written down once.',
      about: 'Pages everyone can read and edit, searchable.',
      signIn: { method: 'native', words: 'With their household account, straight into Outline.' },
      kidView: null, publicAllowed: true, defaultAudience: { adults: true },
      resources: { memGB: 1, diskGB: 2, gpu: 'none', arch: ['x86_64', 'arm64'] },
      data: [{ what: 'Pages and uploads' }, { what: 'Its database', detail: 'dumped first with pg_dump' }], secrets: ['the database password', 'the secret key', 'the sign-in client’s secret'], extras: [], images: { count: 3, size: '0.8 GB' }, screenshots: ['A page'] },
    { id: 'freshrss', icon: 'icons/apps/freshrss.svg', homeFor: 'Sites and blogs you follow', name: 'FreshRSS', purpose: 'News', glyph: 'news', category: 'knowledge', tier: 'community', catalog: 'official', version: '1.27.1', tested: '28 Oct',
      blurb: 'The sites and blogs you follow, in one quiet list, with no feed deciding for you.',
      about: 'Reads RSS and Atom feeds; phone apps that speak its API work too.',
      signIn: { method: 'native', words: 'With their household account, straight into FreshRSS. Phone apps use a password HomersLab makes for each person.' },
      kidView: null, publicAllowed: true, defaultAudience: { adults: true },
      resources: { memGB: 0.2, diskGB: 1, gpu: 'none', arch: ['x86_64', 'arm64'] },
      data: [{ what: 'Feeds, articles kept and what each person has read' }], secrets: ['the sign-in client’s secret'], extras: [], images: { count: 1, size: '0.1 GB' }, screenshots: ['Reading'] },
    { id: 'gramps', icon: 'icons/apps/gramps-web.svg', homeFor: 'The family tree', kidFor: 'Your family tree', name: 'Gramps Web', purpose: 'Family tree', glyph: 'tree', category: 'knowledge', tier: 'community', catalog: 'official', version: '25.10.2', tested: '26 Oct',
      blurb: 'The family tree, with its photos and stories, for everyone to add to.',
      about: 'Ancestors, places and sources, edited together. Kids can browse it.',
      signIn: { method: 'native', words: 'With their household account, straight into Gramps Web.' },
      kidView: 'Read-only, living people hidden', publicAllowed: true, defaultAudience: { everyone: true },
      resources: { memGB: 1, diskGB: 2, grows: 'its photos', gpu: 'none', arch: ['x86_64', 'arm64'] },
      data: [{ what: 'The tree and its media' }, { what: 'Its search index', detail: 'rebuilt if lost' }], secrets: ['the secret key', 'the sign-in client’s secret'], extras: [], images: { count: 2, size: '0.9 GB' }, screenshots: ['A person', 'The tree'] },
    { id: 'openwebui', icon: 'icons/apps/open-webui.svg', homeFor: 'A chat assistant at home', name: 'Open WebUI', purpose: 'Assistant', glyph: 'assistant', category: 'assistants', tier: 'community', catalog: 'official', version: '0.7.4', tested: '5 Nov',
      blurb: 'A chat assistant running on the household’s own GPU. Nothing leaves the house.',
      about: 'Runs local models through Ollama. Conversations stay on the node.',
      signIn: { method: 'native', words: 'With their household account, straight into Open WebUI.' },
      kidView: null, publicAllowed: false, publicWhy: 'Its definition allows mesh only.', defaultAudience: { adults: true },
      resources: { memGB: 8, diskGB: 40, gpu: 'required', gpuWhy: 'running the models', gpuMin: 'NVIDIA, 8 GB or more', arch: ['x86_64'] },
      data: [{ what: 'Conversations and settings' }, { what: 'Not the models', detail: 'downloaded again if lost' }], secrets: ['the session secret', 'the sign-in client’s secret'],
      extras: [{ id: 'gpu', kind: 'device', needs: 'gpu', optional: false, words: 'Use the GPU on {node}', detail: '{gpu}, for running the models', without: 'It can’t run without a GPU.' }],
      images: { count: 2, size: '6.2 GB' }, screenshots: ['A conversation'] },
  ];
  for (const c of catalogs) c.apps = catalog.filter((a) => a.catalog === c.id).length;

  // what the household has installed. memNowGB is what each app uses now; dataGB what its backups hold
  const R = (s) => s.split('').map((c) => (c === 'p' ? 'pass' : c === 'f' ? 'fail' : null)); // twelve Sundays, oldest first
  const installed = [
    { id: 'paperless', node: 'study-pc', audience: { adults: true }, exposure: 'public', version: '2.18.4', since: '14 Mar', memNowGB: 1.9, dataGB: 18.4,
      update: { to: '2.19.0', tested: '4 Nov', notes: 'Faster search and a new editor for mail rules. Nothing for you to do.' }, auto: false,
      lastBackup: { at: '14:00', added: '2.1 MB' }, health: { state: 'working', since: '4 Nov', check: '14:05', ms: 182 },
      restore: R('pppppppppppf'), restoreMin: 6, restoreFail: { when: 'Sunday 8 Nov, 04:12', why: 'The restored copy did not pass its health check within 10 minutes: Paperless was still moving its database to a new layout.', note: 'Mailed and pushed to Inês at 04:13.' },
      history: [{ date: '3 Nov', from: '2.18.3', to: '2.18.4', result: 'updated' }, { date: '20 Oct', from: '2.18.2', to: '2.18.3', result: 'updated' }] },
    { id: 'jellyfin', node: 'attic-nas', audience: { everyone: true }, exposure: 'mesh', version: '10.11.2', since: '2 Apr', memNowGB: 2.6, dataGB: 2.1,
      update: { to: '10.11.3', tested: '6 Nov', notes: 'Fixes subtitles on Android TV.' }, auto: false,
      lastBackup: { at: '14:00', added: '0.4 MB' }, health: { state: 'working', since: '21 Oct', check: '14:05', ms: 96 },
      restore: R('ppppfppppppp'), restoreMin: 3,
      extrasApproved: [{ id: 'gpu', words: 'Uses the GPU on Attic NAS (Intel UHD), for transcoding', by: 'Inês', when: '2 Apr' }],
      history: [{ date: '21 Oct', from: '10.11.1', to: '10.11.2', result: 'updated' }, { date: '14 Oct', from: '10.11.0', to: '10.11.1', result: 'rolledback', why: 'The web page did not answer within 4 minutes, so HomersLab returned it to its snapshot from 14 Oct 02:00.' }] },
    { id: 'plex', node: 'study-pc', audience: { adults: true }, exposure: 'mesh', version: '1.42.2', since: '18 Jun', memNowGB: 1.4, dataGB: 1.2,
      update: null, auto: true, lastBackup: { at: '14:00', added: '0.2 MB' }, health: { state: 'working', since: '5 Nov', check: '14:05', ms: 71 },
      restore: R('pppppppppppp'), restoreMin: 2,
      extrasApproved: [{ id: 'gpu', words: 'Uses the GPU on Study PC (NVIDIA RTX 3060), for transcoding', by: 'Inês', when: '18 Jun' }],
      history: [{ date: '5 Nov', from: '1.42.1', to: '1.42.2', result: 'updated', auto: true }] },
    { id: 'navidrome', node: 'attic-nas', audience: { adults: true, people: ['rita'] }, exposure: 'mesh', version: '0.58.0', since: '2 Apr', memNowGB: 0.3, dataGB: 0.6,
      update: null, auto: true, lastBackup: { at: '14:00', added: '0.1 MB' }, health: { state: 'working', since: '2 Nov', check: '14:05', ms: 41 },
      restore: R('pppppppppppp'), restoreMin: 1, history: [{ date: '2 Nov', from: '0.57.1', to: '0.58.0', result: 'updated', auto: true }] },
    { id: 'kavita', node: 'attic-nas', audience: { everyone: true }, exposure: 'mesh', version: '0.8.8', since: '9 Apr', memNowGB: 0.5, dataGB: 0.9,
      update: null, auto: true, lastBackup: { at: '14:00', added: '60 kB' }, health: { state: 'working', since: '27 Oct', check: '14:05', ms: 63 },
      restore: R('pppppppppppp'), restoreMin: 2, history: [{ date: '27 Oct', from: '0.8.7', to: '0.8.8', result: 'updated', auto: true }] },
    { id: 'donetick', node: 'kitchen-pi', audience: { everyone: true }, exposure: 'mesh', version: '0.1.52', since: '12 Apr', memNowGB: 0.2, dataGB: 0.1,
      update: null, auto: true, lastBackup: { at: '14:00', added: '12 kB' }, health: { state: 'working', since: '1 Nov', check: '14:05', ms: 38 },
      restore: R('pppppppppppp'), restoreMin: 1, history: [{ date: '1 Nov', from: '0.1.51', to: '0.1.52', result: 'updated', auto: true }] },
    { id: 'actual', node: 'kitchen-pi', audience: { people: ['ines', 'tomas'] }, exposure: 'mesh', version: '25.11.0', since: '3 May', memNowGB: 0.3, dataGB: 0.2,
      update: null, auto: false, lastBackup: { at: '14:00', added: '8 kB' }, health: { state: 'working', since: '4 Nov', check: '14:05', ms: 52 },
      restore: R('pppppppppppp'), restoreMin: 1, history: [{ date: '4 Nov', from: '25.10.0', to: '25.11.0', result: 'updated' }] },
    { id: 'memos', node: 'kitchen-pi', audience: { adults: true }, exposure: 'mesh', version: '0.26.0', since: '27 Oct', memNowGB: 0.1, dataGB: 0.3,
      update: { to: '0.26.1', tested: '9 Nov', security: true, notes: 'A security release: fixes a stored cross-site scripting bug in shared memos.' }, auto: false,
      lastBackup: { at: '14:00', added: '4 kB' }, health: { state: 'working', since: '27 Oct', check: '14:05', ms: 44 },
      restore: R('----------pp'), restoreMin: 1, history: [],
      feedstock: { upstream: 'usememos/memos', version: '0.26.0', build: '0.26.0-hl.1', changes: 1, repo: 'github.com/homerslab/feedstocks/tree/main/memos',
        signer: 'github.com/homerslab/feedstocks/.github/workflows/build.yml', sbom: 'SPDX, 212 packages',
        patches: [{ file: '0001-sign-in-only-through-the-household.patch', what: 'Password sign-in is switched off for everyone; the household’s admins are Memos admins', upstream: 'Offered upstream as usememos/memos#5127, under review' }],
        next: { version: '0.26.1', applies: false, why: 'The sign-in code moved in 0.26.1, so the patch needs a rebase' } } },
    { id: 'karakeep', node: 'kitchen-pi', audience: { adults: true }, exposure: 'mesh', version: '0.27.1', since: '20 Oct', memNowGB: 1.9, dataGB: 3.8,
      update: null, auto: false, lastBackup: { at: '14:00', added: '1.3 MB' }, health: { state: 'working', since: '20 Oct', check: '14:05', ms: 120 },
      restore: R('---------ppp'), restoreMin: 4, history: [] },
  ];
  const removed = [
    { id: 'mealie', node: 'kitchen-pi', removedOn: '2 Oct', by: 'Inês', dataGB: 3.4, lastBackup: '2 Oct, 13:00', version: '3.3.2' },
  ];

  // the household's own sign-in and HomersLab itself are backed up and restore-tested too, but are not apps
  const platform = [
    { id: 'signin', name: 'The household’s sign-in', detail: 'Authentik, its database and people', node: 'study-pc', dataGB: 0.4, restore: R('pppppppppppp'), restoreMin: 3 },
    { id: 'manager', name: 'HomersLab itself', detail: 'Desired state and history (no secrets)', node: 'study-pc', dataGB: 0.1, restore: R('pppppppppppp'), restoreMin: 1 },
  ];

  const storedGB = 27.3; // after deduplication and compression; the apps' data below adds up to more
  const targets = [
    { id: 'nas', name: 'Attic NAS', kind: 'restic, on its backups disk', where: 'Over the mesh, inside the house', last: '14:03', storedGB, freeTB: 3.97, state: 'ok', note: 'The fast one for restores.' },
    { id: 'b2', name: 'Backblaze B2', kind: 'restic, S3-compatible', where: 'Off-site, in Amsterdam', last: '14:04', storedGB, freeTB: null, state: 'ok', immutable: '30 days', note: 'Nothing can be deleted for 30 days, not even by HomersLab.' },
    { id: 'proton', name: 'Proton Drive', kind: 'A copy, through Proton’s own command-line tool', where: 'Off-site, end-to-end encrypted', last: '14:05', storedGB, freeTB: 2.9, state: 'ok', note: 'A second off-site copy.' },
  ];
  const archive = { day: 'Sundays', at: '03:30', last: 'Sunday 8 Nov, 03:34', sizeMB: 214, to: ['Backblaze B2', 'Proton Drive'], key: household.recoveryKey,
    holds: ['every app’s database', 'every app’s settings', 'the household’s sign-in and its people', 'every app’s secrets'] };

  // backups kept for one app, newest first: every hour for 2 days, then the 02:00 one of each day for a month,
  // then Sundays for 3 months
  const BACKUP_EVERY = 'every hour';
  const RETENTION = 'Every hour for 2 days, every day for a month, then Sundays for 3 months';
  const points = (() => {
    const out = [];
    for (let i = 0; i < 48; i++) { const d = new Date('2026-11-09T14:00:00'); d.setHours(d.getHours() - i); out.push({ d, kind: 'hourly' }); }
    for (let i = 2; i <= 30; i++) { const d = new Date('2026-11-09T02:00:00'); d.setDate(d.getDate() - i); out.push({ d, kind: 'daily' }); }
    for (let i = 0; i < 9; i++) { const d = new Date('2026-10-04T02:00:00'); d.setDate(d.getDate() - i * 7); out.push({ d, kind: 'weekly' }); }
    return out;
  })();

  // the Store: what Discover features, its collections, and each app's latest news (the catalog's release notes, abridged)
  const store = {
    hero: { app: 'immich', kicker: 'Featured', title: 'Get your photos off your phone, and home', sub: 'Immich backs up every phone in the house, finds faces and places, and keeps every original here, not in someone else’s cloud.' },
    collections: [
      { id: 'start', name: 'Start here', sub: 'What most households install first', apps: ['immich', 'vaultwarden', 'paperless', 'jellyfin', 'opencloud', 'homeassistant'] },
      { id: 'family', name: 'For the whole family', sub: 'With a kid view, or fine for everyone', apps: ['donetick', 'mealie', 'audiobookshelf', 'kavita', 'gramps', 'grocy', 'memos'] },
      { id: 'watch', name: 'Watch, listen, read', sub: 'Your own library, on every screen', apps: ['jellyfin', 'plex', 'navidrome', 'audiobookshelf', 'kavita', 'calibreweb'] },
      { id: 'privacy', name: 'Privacy first', sub: 'What used to live in someone else’s cloud', apps: ['vaultwarden', 'immich', 'openwebui', 'bentopdf', 'karakeep', 'freshrss', 'actual'] },
    ],
    news: {
      immich: 'A map of every place your photos were taken, and faces found twice as fast on a GPU.',
      audiobookshelf: 'Sleep timers that fade out, and chapters for podcasts.',
      mealie: 'Plan a week of meals and turn it into one shopping list.',
      vaultwarden: 'Passkeys stored and synced like passwords.',
      homeassistant: 'A new energy dashboard and faster Zigbee pairing.',
      openwebui: 'Answers that cite the documents they came from.',
      calibreweb: 'Send a book to an e-reader from its page.',
      gramps: 'A timeline view of each person’s life.',
    },
  };
  // requests from members: an app from the catalog, or words when nobody knows which app ("Can't find it?").
  // Several people asking for one app make one request. States: waiting, approved (being installed), installed, notnow.
  const requests = [
    { id: 'rq-abs', app: 'audiobookshelf', state: 'waiting', asks: [
      { by: 'tomas', when: 'Yesterday 21:40', note: 'For the audiobooks on the NAS, so I can listen in the car.' },
      { by: 'rita', when: 'Today 08:15', note: 'Can I listen to stories on my tablet at bedtime?' }] },
    { id: 'rq-meals', text: 'Something to plan meals and keep our recipes', category: 'home', suggest: ['mealie', 'grocy'], state: 'waiting', asks: [
      { by: 'lucia', when: 'Today 09:30', note: 'Avó’s recipes are on paper. I’d like them where we can all find them.' }] },
    { id: 'rq-gramps', app: 'gramps', state: 'notnow', asks: [{ by: 'duarte', when: '7 Nov', note: 'For my school project about our family.' }],
      answer: { by: 'ines', when: 'Yesterday 18:05', reason: 'Avó is still putting the family in. We’ll open it together at Christmas.', fresh: true } },
    { id: 'rq-links', app: 'karakeep', state: 'installed', asks: [{ by: 'lucia', when: '14 Oct', note: 'To keep the links I mail myself.' }],
      answer: { by: 'ines', when: '20 Oct', words: 'Installed for the adults.' } },
  ];

  const dataGB = +(installed.reduce((t, a) => t + a.dataGB, 0) + removed.reduce((t, a) => t + a.dataGB, 0) + platform.reduce((t, a) => t + a.dataGB, 0)).toFixed(1);
  window.HL_DATA = { NOW, SUNDAYS, BACKUP_EVERY, RETENTION, household, people, devices, invites, nodes, storage, catalogs, categories, catalog, store, requests, installed, removed, platform, targets, archive, points, storedGB, dataGB };

  // ---------- facts that appear twice must agree ----------
  const byId = (list, id) => list.find((x) => x.id === id);
  for (const a of installed) { console.assert(byId(catalog, a.id), 'installed app has a definition', a.id); console.assert(byId(nodes, a.node), 'installed app is placed on a node', a.id); console.assert(a.restore.length === 12, 'twelve Sundays', a.id); }
  for (const s of storage) for (const u of s.usedBy) console.assert(byId(installed, u), 'storage user is installed', u);
  for (const d of devices) console.assert(byId(people, d.person), 'device belongs to a person', d.id);
  const appsData = installed.reduce((t, a) => t + a.dataGB, 0) + removed.reduce((t, a) => t + a.dataGB, 0) + platform.reduce((t, a) => t + a.dataGB, 0);
  console.assert(Math.abs(appsData - 31.5) < 0.05, 'apps data adds up to 31.5 GB', appsData);
  console.assert(storedGB < appsData, 'deduplicated is smaller than the data');
  console.assert(SUNDAYS[11].getDay() === 0 && SUNDAYS[11].getDate() === 8, 'the last Sunday is 8 Nov');
  console.assert(NOW.getDay() === 1, '9 Nov 2026 is a Monday');
  for (const a of installed) console.assert(a.lastBackup.at === '14:00', 'every app was backed up at the top of this hour', a.id);
  console.assert(points[0].d.getHours() === 14 && points[47].d.getHours() === 15 && points[48].kind === 'daily', 'hourly points cover the last 48 hours');
  console.assert(points.every((p, i) => i === 0 || p.d < points[i - 1].d), 'restore points are newest first');
  for (const c of catalog) console.assert(c.icon && c.homeFor, 'every app definition ships an icon and a line for Home', c.id);
  for (const c of catalog) console.assert(categories.some((k) => k.id === c.category), 'every app has a purpose', c.id);
  console.assert(installed.filter((a) => byId(catalog, a.id).category === 'films').length === 2, 'two apps for films side by side: Jellyfin and Plex');
})();
(() => {
  const D = window.HL_DATA, has = (id) => D.catalog.some((a) => a.id === id);
  for (const c of D.store.collections) for (const id of c.apps) console.assert(has(id), 'collection app is in the catalog', c.id, id);
  console.assert(has(D.store.hero.app), 'the featured app is in the catalog');
  for (const r of D.requests) {
    console.assert(r.app ? has(r.app) : !!r.text, 'a request names a catalog app or says what it is for', r.id);
    for (const a of r.asks) { const p = D.people.find((x) => x.id === a.by); console.assert(p, 'a request comes from a person', r.id); if (p.role === 'kid' && r.app) { const d = D.catalog.find((x) => x.id === r.app); console.assert(d.kidView || d.kidOk, 'kids ask only for kid apps', r.id); } }
    if (r.state === 'installed') console.assert(D.installed.some((a) => a.id === r.app), 'an installed request is installed', r.id);
    if (r.state === 'waiting' && r.app) console.assert(!D.installed.some((a) => a.id === r.app), 'a waiting request is for an app the household doesn’t have', r.id);
  }
})();
