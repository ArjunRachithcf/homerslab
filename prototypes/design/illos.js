// HomersLab illustrations: calm pictures made of the design system's own shapes and glass (tiles, orbs, chips and
// lines on a glow), never stock art. Each returns HTML for an .illo stage, 300 x 172; pieces are placed by their offset
// from the centre (--x, --y), sized by --s (or --w and --h), and arrive in the order --i on the bouncy spring.
// Decorative: every stage is aria-hidden, so the words around it carry the meaning. Needs icons.js first.
(() => {
  const esc = (v) => String(v ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]);
  const at = (x, y, i, more = '') => `style="--x:${x}px;--y:${y}px;--i:${i};${more}"`;
  const glow = (s = 200, x = 0, y = 0, color = '') => `<span class="illo-glow" ${at(x, y, 0, `--s:${s}px;${color ? `--glow:${color};` : ''}`)}></span>`;
  const tile = (icon, x, y, i, { s = 64, w, h, thick = true, r, cls = '' } = {}) =>
    `<span class="illo-tile ${thick ? 'glass-thick' : 'glass'} ${cls}" ${at(x, y, i, `--s:${s}px;${w ? `width:${w}px;` : ''}${h ? `height:${h}px;` : ''}${r ? `--r:${r}deg;` : ''}`)}>${icon}</span>`;
  const orb = (icon, x, y, i, s = 32, cls = '') => `<span class="illo-orb glass ${cls}" ${at(x, y, i, `--s:${s}px;`)}>${icon}</span>`;
  const chip = (icon, text, x, y, i) => `<span class="illo-chip glass-thick" ${at(x, y, i)}>${icon}${esc(text)}</span>`;
  const lines = (d) => `<svg class="illo-lines" viewBox="-150 -86 300 172" preserveAspectRatio="xMidYMid meet" aria-hidden="true" focusable="false">${d.map((p) => `<path d="${p}"/>`).join('')}</svg>`;
  const stage = (inner, cls = '') => `<div class="illo ${cls}" aria-hidden="true">${inner}</div>`;
  const I = (n, cls = 'icon') => window.I(n, cls);

  const ILLO = {
    // the machine, about to become the household's primary
    welcome: () => stage(`${glow(220)}${lines(['M-104 -34 Q-60 -40 -40 -14', 'M108 -38 Q64 -44 44 -16', 'M-96 44 Q-58 44 -42 22', 'M100 42 Q60 46 42 22'])}
      ${tile(window.MARK, 0, 0, 1, { s: 92 })}${orb(I('play'), -110, -38, 2)}${orb(I('folder'), 112, -40, 3)}${orb(I('calendar'), -100, 46, 4)}${orb(I('heart'), 104, 46, 5)}`),
    // you: your initial and your address, as you type them
    you: ({ name = '', mail = '' } = {}) => stage(`${glow(200, 0, -10)}
      <span class="illo-orb glass-thick" ${at(0, -22, 1, '--s:84px;')}><span class="illo-initial">${esc((name.trim()[0] || '?').toUpperCase())}</span></span>
      ${chip(I('mail'), mail || 'your mail address', 0, 52, 2)}`),
    // the household: its name on a plate, the people you will invite around it
    household: ({ name = '' } = {}) => stage(`${glow(220, 0, 0)}${lines(['M-70 40 L-20 6', 'M0 52 L0 12', 'M70 40 L20 6'])}
      ${chip(I('home'), name || 'Your household', 0, -14, 1)}${orb(I('user'), -78, 46, 2, 34)}${orb(I('user'), 0, 58, 3, 34)}${orb(I('user'), 78, 46, 4, 34)}`),
    // one sign-in, in front of every app
    signin: () => stage(`${glow(210)}${lines(['M-30 -10 L-98 -46', 'M30 -10 L98 -46', 'M-30 14 L-98 48', 'M30 14 L98 48'])}
      ${tile(I('key'), 0, 0, 1, { s: 78 })}${tile(I('play'), -106, -48, 2, { s: 42, thick: false })}${tile(I('folder'), 106, -48, 3, { s: 42, thick: false })}${tile(I('calendar'), -106, 50, 4, { s: 42, thick: false })}${tile(I('home'), 106, 50, 5, { s: 42, thick: false })}`),
    // the mesh: your machines and devices, joined wherever they are
    network: () => stage(`${glow(230, 0, 6)}${lines(['M-82 26 Q0 -10 82 26', 'M-82 26 L-14 -34', 'M82 26 L14 -34'])}
      ${tile(I('desktop'), 0, -44, 1, { s: 60 })}${tile(I('laptop'), -96, 34, 2, { s: 60 })}${tile(I('phone'), 96, 34, 3, { s: 60 })}${orb(I('mesh'), 0, 22, 4, 34)}`),
    // the domain: every app gets a name under it, with a certificate
    domain: ({ domain = '' } = {}) => stage(`${glow(210, -40, 0)}${lines(['M-70 0 Q-40 -26 -8 -28', 'M-70 6 Q-40 30 -6 30'])}
      ${tile(I('globe'), -108, 2, 1, { s: 68 })}${chip(I('lock'), `photos.${domain || 'your.domain'}`, 54, -28, 2)}${chip(I('lock'), `home.${domain || 'your.domain'}`, 62, 30, 3)}`),
    // mail: invites, passkey resets and alerts go out
    mail: () => stage(`${glow(200)}${lines(['M40 -10 L88 -40', 'M-40 10 L-90 40', 'M40 14 L92 44'])}
      ${tile(I('mail'), 0, 0, 1, { s: 86 })}${orb(I('bell'), 98, -44, 2)}${orb(I('key'), -98, 44, 3)}${orb(I('people'), 100, 48, 4)}`),
    // the recovery kit: a document, a key, a printer
    kit: () => stage(`${glow(210, 0, 0)}
      <span class="illo-doc glass-thick" ${at(-6, 0, 1, '--r:-5deg;')}><i></i><i></i><i></i><i class="short"></i></span>
      ${orb(I('key'), 34, 34, 2, 40)}${orb(I('printer'), -104, 30, 3)}${orb(I('download'), 104, -30, 4)}`),
    // joining the mesh: the phone's camera, or this laptop with a one-time key
    join: () => stage(`${glow(230)}${lines(['M-46 0 Q0 -30 46 0'])}
      ${tile(I('qrScan'), -84, 0, 1, { w: 66, h: 112, s: 66 })}${tile(I('laptop'), 92, 6, 2, { w: 112, h: 78, s: 78 })}${orb(I('mesh'), 2, -14, 3, 34)}`),
    // the passkey: a key in a ring that closes when it is made
    passkey: ({ made = false } = {}) => stage(`${glow(220, 0, 0, made ? 'var(--green)' : '')}
      <svg class="illo-ring ${made ? 'is-made' : ''}" ${at(0, 0, 1)} viewBox="0 0 132 132" aria-hidden="true" focusable="false"><circle cx="66" cy="66" r="62"/></svg>
      <span class="illo-orb glass-thick" ${at(0, 0, 1, '--s:104px;')}>${I('key', 'icon illo-key')}</span>
      ${made ? orb(I('check2'), 42, 38, 2, 36, 'is-ok') : ''}`),
    // done: the home network's door closes; the household's apps answer on the mesh
    done: () => stage(`${glow(230, 0, 0, 'var(--green)')}${lines(['M-90 -30 Q-50 -40 -38 -16', 'M90 -30 Q50 -40 38 -16', 'M0 66 L0 46'])}
      ${tile(I('lock'), 0, 0, 1, { s: 90, cls: 'is-ok' })}${orb(I('play'), -104, -34, 2)}${orb(I('folder'), 104, -34, 3)}${orb(I('home'), 0, 70, 4, 30)}`),
  };
  window.ILLO = ILLO;
})();
