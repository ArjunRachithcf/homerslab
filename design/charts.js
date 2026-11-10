// HomersLab charts: the few pictures of data Admin needs, as plain HTML and SVG strings, no library.
// HL.treemap (the disk analyzer's space map), HL.usage (a disk's use, stacked), HL.days (ninety days of uptime),
// HL.spark (a day of response times). Colours come from the --viz-* tokens, each with its own ink, so labels keep AA.
(() => {
  const esc = (s) => String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  const size = (gb) => (gb >= 1000 ? `${(gb / 1000).toFixed(gb >= 10000 ? 1 : 2)} TB` : gb >= 1 ? `${gb.toFixed(gb >= 100 ? 0 : 1)} GB` : `${Math.round(gb * 1000)} MB`);

  // ---------- a squarified treemap: rectangles whose areas are the sizes, as close to square as they can be ----------
  function squarify(items, x, y, w, h) {
    const out = [], total = items.reduce((s, i) => s + i.value, 0); if (!total) return out;
    const scale = (w * h) / total; let rest = items.map((i) => ({ ...i, area: i.value * scale }));
    while (rest.length) {
      const short = Math.min(w, h); let row = [rest[0]], best = worst(row, short);
      for (let k = 1; k < rest.length; k++) { const next = [...row, rest[k]], wv = worst(next, short); if (wv > best) break; row = next; best = wv; }
      const sum = row.reduce((s, r) => s + r.area, 0), thick = sum / short;
      let off = 0;
      for (const r of row) { const len = r.area / thick; if (w >= h) out.push({ ...r, x, y: y + off, w: thick, h: len }); else out.push({ ...r, x: x + off, y, w: len, h: thick }); off += len; }
      if (w >= h) { x += thick; w -= thick; } else { y += thick; h -= thick; }
      rest = rest.slice(row.length);
    }
    return out;
  }
  function worst(row, short) { const s = row.reduce((a, r) => a + r.area, 0), max = Math.max(...row.map((r) => r.area)), min = Math.min(...row.map((r) => r.area)); return Math.max((short * short * max) / (s * s), (s * s) / (short * short * min)); }
  // items: [{ id, label, value (GB), tone: 1–6, attrs }]; each tile is a button that drills down (or says why it can't)
  function treemap(items, { label = 'Space map', w = 1000, h = 420 } = {}) {
    const tiles = squarify(items.filter((i) => i.value > 0).sort((a, b) => b.value - a.value), 0, 0, w, h);
    return `<div class="treemap" role="group" aria-label="${esc(label)}" style="aspect-ratio:${w}/${h}">${tiles.map((t) => {
      const small = t.w / w < 0.09 || t.h / h < 0.12, tiny = t.w / w < 0.035 || t.h / h < 0.06;
      return `<button type="button" class="tm-tile tone-${t.tone || 1} ${small ? 'is-small' : ''} ${tiny ? 'is-tiny' : ''}" style="left:${(t.x / w) * 100}%;top:${(t.y / h) * 100}%;width:${(t.w / w) * 100}%;height:${(t.h / h) * 100}%" aria-label="${esc(t.label)}, ${size(t.value)}" ${t.attrs || ''}><span class="tm-name">${esc(t.label)}</span><span class="tm-size">${size(t.value)}</span></button>`;
    }).join('')}</div>`;
  }

  // ---------- a disk's use, stacked, with its legend ----------
  // segs: [{ label, gb, tone }]; total: the disk's size
  function usage(segs, total, { label = 'Use' } = {}) {
    const used = segs.reduce((s, x) => s + x.gb, 0), pct = (used / total) * 100;
    return `<div class="usage ${pct >= 90 ? 'is-high' : pct >= 80 ? 'is-warn' : ''}"><div class="usage-bar" role="img" aria-label="${esc(label)}: ${size(used)} of ${size(total)} used, ${Math.round(pct)}%">${segs.filter((x) => x.gb > 0).map((x) => `<span class="tone-${x.tone || 1}" style="width:${Math.max(0.4, (x.gb / total) * 100)}%"></span>`).join('')}</div>
      <ul class="usage-legend">${segs.filter((x) => x.gb > 0).map((x) => `<li><span class="usage-key tone-${x.tone || 1}" aria-hidden="true"></span>${esc(x.label)} <span class="num">${size(x.gb)}</span></li>`).join('')}<li class="usage-free"><span class="usage-key" aria-hidden="true"></span>Free <span class="num">${size(total - used)}</span></li></ul></div>`;
  }

  // ---------- ninety days of uptime: one bar a day ----------
  const DAYWORD = { ok: 'Working all day', partial: 'An incident, under an hour', down: 'Down for an hour or more', none: 'Not installed yet' };
  function days(list, { label = 'Ninety days' } = {}) {
    const bad = list.filter((d) => d === 'partial' || d === 'down').length;
    return `<div class="days" role="img" aria-label="${esc(label)}: ${bad ? `${bad} ${bad === 1 ? 'day' : 'days'} with an incident` : 'no incidents'}">${list.map((d) => `<span class="day is-${d}" title="${DAYWORD[d]}"></span>`).join('')}</div>`;
  }

  // ---------- a day of response times, as a quiet line ----------
  function spark(values, { label = 'Response times, last 24 hours', unit = 'ms' } = {}) {
    const w = 120, h = 28, max = Math.max(...values) * 1.1, min = Math.min(...values) * 0.9, step = w / (values.length - 1);
    const pts = values.map((v, i) => `${(i * step).toFixed(1)},${(h - ((v - min) / (max - min || 1)) * h).toFixed(1)}`).join(' ');
    const sorted = [...values].sort((a, b) => a - b), med = sorted[Math.floor(sorted.length / 2)];
    return `<svg class="spark" viewBox="0 0 ${w} ${h}" preserveAspectRatio="none" role="img" aria-label="${esc(label)}: median ${med} ${unit}, highest ${Math.max(...values)} ${unit}"><polyline points="${pts}" fill="none" vector-effect="non-scaling-stroke"/></svg>`;
  }

  // after the map is on screen: a tile smaller than a finger (24px) stops being a button; the list beside it has it
  function treemapFit(root = document) {
    for (const t of root.querySelectorAll('button.tm-tile')) {
      const r = t.getBoundingClientRect(); if (r.width >= 24 && r.height >= 24) continue;
      const s = document.createElement('span'); s.className = t.className + ' is-mark'; s.setAttribute('style', t.getAttribute('style')); s.setAttribute('aria-hidden', 'true'); s.innerHTML = t.innerHTML; t.replaceWith(s);
    }
  }

  window.HL = Object.assign(window.HL || {}, { treemap, treemapFit, usage, days, spark, size });
})();
