// HomersLab Admin prototype: uptime and incidents (ADR 0018). A year of uptime and response times per app, node and the
// public path; the incident log; the outside heartbeat. PROTOTYPE, throwaway: figures come from data-ops.js, where
// uptime is computed from the incidents.
(() => {
  const { D, S, cat, node, and, appIcon, route, render, act } = A;
  const { esc, dialog } = UI;
  const pct = (v) => (v >= 99.995 ? '100%' : `${v.toFixed(2)}%`);
  const dur = (min) => (min >= 60 ? `${Math.floor(min / 60)} h ${Math.round(min % 60)} min` : `${Math.round(min)} min`);
  const when = (s) => { const d = new Date(s); return `${UI.DAYS ? UI.DAYS[d.getDay()].slice(0, 3) : ''} ${d.getDate()} ${UI.MONTHS ? UI.MONTHS[d.getMonth()].slice(0, 3) : ''}, ${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`.trim(); };

  route(/^#\/nodes\/uptime$/, () => {
    const U = D.uptime, hb = D.heartbeat, incs = [...D.incidents].sort((a, b) => new Date(b.start) - new Date(a.start));
    const row = (label, sub, lead, u) => `<tr><td><div class="ident">${lead}<div class="names"><b>${label}</b><span>${sub}</span></div></div></td>
      <td class="up-days">${HL.days(u.days, { label: `${label}, ninety days` })}</td>
      <td class="n uptime-num">${pct(u.d1)}</td><td class="n uptime-num hide-sm ${u.d30 < 99.9 ? 'is-low' : ''}">${pct(u.d30)}</td><td class="n uptime-num hide-sm">${pct(u.y)}</td>
      <td class="hide-sm up-spark">${HL.spark(u.spark)}<span class="num">${[...u.spark].sort((a, b) => a - b)[12]} ms</span></td></tr>`;
    const head = (first) => `<thead><tr><th>${first}</th><th>Ninety days</th><th class="n">24 h</th><th class="n hide-sm">30 days</th><th class="n hide-sm">A year</th><th class="hide-sm">Response, today</th></tr></thead>`;
    return `<div class="page-head"><div><h1>Uptime</h1><p class="page-sub">A year of history for every app, node and the public path, and every incident with what fixed it. Only admins see this; the family hears on Home.</p></div></div>
      ${A.nodesSeg('uptime')}
      <div class="grid a-block" style="margin-bottom:28px">
        <section class="card span-6 stack" style="gap:12px" aria-labelledby="h-hb"><div class="card-head"><h2 class="card-title" id="h-hb">The outside heartbeat</h2><span class="pill pill-green"><span class="dot dot-green dot-sm"></span>Arriving</span></div>
          <p>Study PC pings ${esc(hb.service)} every ${esc(hb.every)}; the last ping was at ${esc(hb.last)}. If pings stop for ${esc(hb.grace)}, ${esc(hb.service)} alerts ${esc(hb.to)}.</p>
          <p class="subhead">${esc(hb.watchers)}. ${esc(hb.note)}</p></section>
        <section class="card span-6 stack" style="gap:12px" aria-labelledby="h-now"><div class="card-head"><h2 class="card-title" id="h-now">Right now</h2></div>
          <ul class="lines"><li>${I('check')}<span>Every app and node answers its checks<small>Checked every minute; the public path every 5 minutes, from outside</small></span></li><li>${I('alert')}<span>One open incident: Paperless-ngx’s restore test<small>The app itself works</small></span></li></ul></section></div>
      <section class="a-block" aria-labelledby="h-up" style="margin-bottom:36px"><div class="a-block-head"><div><h2 id="h-up">Uptime</h2><p>A day is orange when something was down for under an hour, red for an hour or more.</p></div></div>
        <div class="group"><table class="rtable has-lead up-table">${head('The public path and the nodes')}<tbody>
          ${row('The public path', 'Home and Paperless-ngx from the internet, through NetBird’s proxy', `<span class="up-glyph">${I('globe', 'icon icon-md')}</span>`, U.public)}
          ${U.nodes.map((u) => row(esc(node(u.id).name), esc(node(u.id).primary ? 'The primary' : 'A node'), `<span class="up-glyph">${I('nodes', 'icon icon-md')}</span>`, u)).join('')}
        </tbody></table></div>
        <div class="group" style="margin-top:var(--gap)"><table class="rtable has-lead up-table">${head('Apps')}<tbody>
          ${U.apps.map((u) => row(esc(cat(u.id).name), esc(node(u.node).name), appIcon(cat(u.id)), u)).join('')}
        </tbody></table></div></section>
      <section class="a-block" aria-labelledby="h-inc"><div class="a-block-head"><div><h2 id="h-inc">Incidents</h2><p>One stretch of something not working, from the first failed check to resolved. Planned work in the maintenance window is marked.</p></div></div>
        <div class="group lead-md">${incs.map((i) => `<button type="button" class="row row-link is-top" data-act="incident" data-id="${i.id}"><span class="row-lead">${I(i.level === 'critical' ? 'alert' : i.end ? 'check' : 'clock', `icon icon-md ${i.level === 'critical' ? 'inc-crit' : ''}`)}</span><span class="row-main"><span class="row-title">${esc(i.title)}</span><span class="row-sub wrap">${esc(when(i.start))} · ${i.end ? (i.noDowntime ? 'no downtime' : `down ${dur(D.minutes(i))}`) : 'open'}${i.planned ? ' · in the maintenance window' : ''} · ${esc(i.fix.split('.')[0])}</span></span>${i.end ? '<span class="pill">Resolved</span>' : '<span class="pill pill-orange">Open</span>'}${I('chevronRight', 'icon row-chev')}</button>`).join('')}</div></section>`;
  });
  act('incident', (t) => {
    const i = D.incidents.find((x) => x.id === t.dataset.id);
    const hits = i.hits.includes('all') ? 'Every node and app' : i.hits.length ? and(i.hits.map((h) => (h === 'public' ? 'the public path' : node(h)?.name || cat(h)?.name))) : 'Nothing went down';
    dialog({ title: esc(i.title), sub: `${esc(when(i.start))}${i.end ? ` to ${esc(when(i.end).split(', ')[1])}` : ', still open'}${i.noDowntime ? '' : ` · ${dur(D.minutes(i))}`}`, wide: true,
      body: `<div class="group"><div class="row is-top row-wrap"><span class="row-main"><span class="row-label">What broke</span><span class="row-sub wrap">${esc(i.what)}</span></span></div><div class="row is-top row-wrap"><span class="row-main"><span class="row-label">What was affected</span><span class="row-sub wrap">${esc(hits)}</span></span></div><div class="row is-top row-wrap"><span class="row-main"><span class="row-label">What fixed it</span><span class="row-sub wrap">${esc(i.fix)}</span></span></div></div>
        ${i.alerts.length ? `<div><p class="group-head">Alerts it sent</p><ul class="group stepup-list">${i.alerts.map((x) => `<li>${I('bell')}<span>${esc(x)}</span></li>`).join('')}</ul><p class="subhead" style="margin-top:8px">One alert per incident, then “resolved”; a reminder every 4 hours while it lasts.</p></div>` : ''}`,
      actions: [{ label: 'Close', value: 'ok', kind: 'primary' }] });
  });
})();
