#!/usr/bin/env python3
"""Write ../guide.html, the one-page usage guide, from the same numbers build.py uses.

    python3 guide.py            # after build.py (and tests.py for the test links)
Self-contained: inline SVG, system fonts, no network.
"""
import html, json, math, os, sys

sys.dont_write_bytecode = True
HERE = os.path.dirname(os.path.abspath(__file__))
KIT = os.path.dirname(HERE)
sys.path.insert(0, HERE)
import build as B   # noqa: E402
import harbour as H  # noqa: E402

E = html.escape
REC = B.RECOMMENDED
VC, VB = B.VARIANTS["classic"], B.VARIANTS["blue"]
OUT = B.OUT


def svg(vb, body, w=None, h=None, label=None, cls=""):
    x, y, vw, vh = vb
    size = (f' width="{w}"' if w else "") + (f' height="{h}"' if h else "")
    aria = f'role="img" aria-label="{E(label)}"' if label else 'aria-hidden="true"'
    return f'<svg class="{cls}" viewBox="{B.f2(x)} {B.f2(y)} {B.f2(vw)} {B.f2(vh)}"{size} {aria}>{body}</svg>'


def mark(fill, P=H.MASTER, s=1.0, tx=0.0, ty=0.0):
    return f'<path fill="{fill}" d="{B.mark_d(P, s, tx, ty)}"/>'


def lockup(layout, sym, word, reversed_=False):
    fn = B.horizontal if layout == "horizontal" else B.stacked
    vb, body = fn(sym, word, reversed_)
    return vb, body


def rgb_txt(h):
    return " ".join(str(c) for c in B.rgb(h))


def cmyk_txt(h):
    c = B.PRINT.get(h, {}).get("cmyk")
    return " ".join(str(v) for v in c) if c else "paper (no ink)"


def pantone_txt(h):
    p = B.PRINT.get(h, {})
    if not p.get("pantone"):
        return "—"
    return f'{p["pantone"]}' + (f' <span class="de">ΔE00 {p["de"]}</span>' if p.get("de") is not None else "")


def oklch_txt(h):
    L, C, Hh = B.oklch(h)
    return f"{L:.3f} {C:.3f} {Hh:.0f}°"


# --- construction drawing ------------------------------------------------------------------------------------------
def construction(accent):
    P = H.MASTER
    m = 8
    grid = "".join(f'<line x1="{i * m}" y1="0" x2="{i * m}" y2="192" class="g{" g6" if i % 6 == 0 else ""}"/>'
                   f'<line x1="0" y1="{i * m}" x2="192" y2="{i * m}" class="g{" g6" if i % 6 == 0 else ""}"/>' for i in range(25))
    d, info = H.mark_d(P)
    cx, cy = info["hull_centre"]
    lcx, lcy = info["leech_centre"]
    lr = info["leech_radius"]
    guides = [
        ('<line x1="-14" y1="104" x2="206" y2="104" class="k"/>', (198, 102, "deck 13m")),
        ('<line x1="-14" y1="120" x2="206" y2="120" class="k"/>', (198, 118, "hull 15m")),
        ('<line x1="-14" y1="136" x2="206" y2="136" class="k"/>', (198, 134, "keel 17m")),
        ('<line x1="-14" y1="88" x2="206" y2="88" class="k"/>', (198, 86, "foot 11m")),
        ('<line x1="-14" y1="8" x2="206" y2="8" class="k"/>', (198, 6, "head 1m")),
        ('<line x1="80" y1="-14" x2="80" y2="206" class="k"/>', None),
        ('<line x1="128" y1="-14" x2="128" y2="206" class="k"/>', None),
    ]
    g = "".join(x for x, _ in guides)
    labels = "".join(f'<text x="{x}" y="{y}" class="lb">{t}</text>' for _, l in guides if l for x, y, t in [l])
    a0 = math.atan2(P["head"] - lcy, P["luff"] - lcx) - math.radians(9)
    a1 = math.atan2(P["foot"] - lcy, P["clew"] - lcx) + math.radians(6)
    p0 = (lcx + lr * math.cos(a0), lcy + lr * math.sin(a0))
    p1 = (lcx + lr * math.cos(a1), lcy + lr * math.sin(a1))
    circles = (f'<circle cx="{cx}" cy="{cy}" r="80" class="c"/><circle cx="{cx}" cy="{cy}" r="1.6" class="dot"/>'
               f'<path d="M{p0[0]:.2f} {p0[1]:.2f}A{lr:.2f} {lr:.2f} 0 0 1 {p1[0]:.2f} {p1[1]:.2f}" class="c c2"/>'
               f'<line x1="{lcx:.2f}" y1="{lcy:.2f}" x2="{P["clew"]}" y2="{P["foot"]}" class="c c2 rad"/>'
               f'<circle cx="{lcx:.2f}" cy="{lcy:.2f}" r="1.6" class="dot d2"/>')
    notes = (f'<text x="{cx + 3}" y="{cy - 3}" class="lb">R 10m</text>'
             f'<text x="{lcx + 3:.1f}" y="{lcy + 9:.1f}" class="lb l2">leech R {lr / 8:.1f}m, belly 23\u00b0</text>'
             '<text x="80" y="-17" class="lb mid">luff 10m</text><text x="128" y="-17" class="lb mid">clew 16m</text>'
             '<text x="24" y="214" class="lb mid">stem 6m</text><text x="96" y="214" class="lb mid">counter 12m</text>')
    body = (f'<g>{grid}</g><path d="{d}" class="ink" style="fill:var(--accent)"/>{circles}{g}{labels}{notes}')
    return svg((-30, -30, 290, 256), body, label="Construction of the mark on its 24-module grid", cls="construct")


# --- clear space ------------------------------------------------------------------------------------------------------
def clearspace(accent, text):
    vb, body = B.horizontal(accent, text)
    body = body.replace(f'fill="{accent}"', 'style="fill:var(--accent)"').replace(f'fill="{text}"', 'style="fill:var(--text)"')
    w, h = vb[2], vb[3]
    u = 48
    box = (f'<rect x="{-u}" y="{-u}" width="{w + 2 * u}" height="{h + 2 * u}" class="cs"/>'
           f'<rect x="0" y="0" width="{w}" height="{h}" class="cs2"/>')
    units = "".join(f'<rect x="{x}" y="{y}" width="{u}" height="{u}" class="cu"/>' for x, y in
                    ((-u, -u), (w, -u), (-u, h), (w, h), (-u, (h - u) / 2), (w, (h - u) / 2), ((w - u) / 2, -u), ((w - u) / 2, h)))
    return svg((-u - 6, -u - 6, w + 2 * u + 12, h + 2 * u + 12), units + box + body,
               label="Clear space: one stem width on every side", cls="clear")


# --- small renders used on the page --------------------------------------------------------------------------------
def tile_icon(v, look, size=96):
    tile = v["light"] if look == "light" else "#1C1C1E"
    mk = "#FFFFFF" if look == "light" else v["dark"]
    m = 824 * 0.5
    k = m / 192
    body = (f'<path fill="{tile}" d="{B.rounded_square(100, 100, 824, 185.4)}"/>'
            + mark(mk, H.REVERSED, k, 512 - m / 2, 512 - m / 2))
    return svg((0, 0, 1024, 1024), body, size, size, label=f"{v['name']} app icon, {look}")


def fav(fill, size=16, P=H.CUT16, n=16):
    return svg((0, 0, n, n), mark(fill, P), size, size, label="favicon")


def tabs(v):
    out = []
    for dark, strip, tab, txt in ((False, "#DEE1E6", "#FFFFFF", "#1F1F1F"), (True, "#202124", "#35363A", "#E8EAED")):
        icon = fav(v["dark"] if dark else v["light"])
        out.append(f'<div class="strip" style="background:{strip}"><div class="tab" style="background:{tab};color:{txt}">'
                   f'{icon}<span>HomersLab</span></div><div class="tab" style="color:{txt}">{icon}<span>Apps</span></div></div>')
    return "".join(out)


def spec_list(h, on, label):
    ratio = B.contrast(on, h)
    return (f'<dl class="spec"><dt>RGB</dt><dd>{rgb_txt(h)}</dd><dt>CMYK \u2248</dt><dd>{cmyk_txt(h)}</dd>'
            f'<dt>Pantone \u2248</dt><dd>{pantone_txt(h)}</dd><dt>OKLCH</dt><dd>{oklch_txt(h)}</dd>'
            f'<dt>{label}</dt><dd>{ratio:.2f}:1</dd></dl>')


def variant_card(v, recommended):
    vb, light = B.horizontal(v["light"], "#1D1D1F")
    _, dark = B.horizontal(v["dark"], "#F5F5F7", True)
    ct = B.contrast_table(v["light"], v["dark"])
    c_rows = "".join(
        f'<li><span class="ratio {"ok" if r["aa_text"] else ("mid" if r["aa_large_or_ui"] else "bad")}">{r["ratio"]:.2f}</span>{E(r["pair"])}</li>'
        for r in ct)
    badge = '<span class="badge">Recommended</span>' if recommended else '<span class="badge alt">Alternative</span>'
    return f'''
<article class="variant{' rec' if recommended else ''}">
  <header><h3>{E(v["name"])}</h3>{badge}</header>
  <p class="muted small">{E(v["about"])}.</p>
  <div class="pair">
    <div><div class="swatch" style="background:{v["light"]};color:#fff"><b>{v["light"]}</b><span>light mode</span></div>
      {spec_list(v["light"], "#FFFFFF", "White on it")}</div>
    <div><div class="swatch" style="background:{v["dark"]};color:#000"><b>{v["dark"]}</b><span>dark mode</span></div>
      {spec_list(v["dark"], "#000000", "Black on it")}</div>
  </div>
  <div class="demo light">{svg(vb, light, label=f"Lockup in {v['name']}, light")}</div>
  <div class="demo dark">{svg(vb, dark, label=f"Lockup in {v['name']}, dark")}</div>
  <div class="icons">{tile_icon(v, "light", 72)}{tile_icon(v, "dark", 72)}<div class="tabs">{tabs(v)}</div></div>
  <details><summary>Contrast (WCAG 2.2)</summary><ul class="contrast">{c_rows}</ul></details>
</article>'''


# --- don'ts --------------------------------------------------------------------------------------------------------
def donts(A, AD):
    sym = mark(A)
    vb, lk = B.horizontal(A, "#1D1D1F")
    w = vb[2]
    k = 0.25
    small_lk = lambda extra="", body=lk: svg((0, 0, w, 192), f'<g {extra}>{body}</g>', label="example")
    items = [
        ("Stretch or squash it", svg((0, 0, 265, 192), f'<g transform="scale(1.38 1)">{sym}</g>', label="stretched")),
        ("Rotate it", svg((-20, -20, 232, 232), f'<g transform="rotate(-14 96 96)">{sym}</g>', label="rotated")),
        ("Recolour it outside the palette", svg((0, 0, 192, 192), mark("#D9480F"), label="recoloured")),
        ("Add shadows, outlines or gradients", svg((-12, -12, 216, 216),
            f'<defs><linearGradient id="dg" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="#7FD3C8"/><stop offset="1" stop-color="#0B3D38"/></linearGradient></defs>'
            f'<path d="{B.mark_d(H.MASTER, 1, 6, 6)}" fill="#000" opacity=".25"/><path d="{B.mark_d(H.MASTER)}" fill="url(#dg)" stroke="#E8B04B" stroke-width="5"/>', label="effects")),
        ("Rearrange or resize the lockup's parts", svg((0, 0, w, 192),
            f'<path fill="#1D1D1F" d="{B.transform_d(OUT["wordmark"]["d"], 1.0, -OUT["wordmark"]["bbox"][0], 136)}"/>'
            + mark(A, H.MASTER, 0.5, w - 96, 48), label="rearranged")),
        ("Retype the name in another font", svg((0, 0, w, 192), mark(A)
            + '<text x="240" y="136" font-family="Georgia, serif" font-style="italic" font-size="118" fill="#1D1D1F">HomersLab</text>', label="retyped")),
        ("Put the light-mode teal on dark, or the dark-mode teal on light", svg((0, 0, 400, 192),
            f'<rect x="0" y="0" width="192" height="192" rx="24" fill="#000"/>{mark(A, H.MASTER, 0.75, 24, 24)}'
            f'<rect x="208" y="0" width="192" height="192" rx="24" fill="#fff"/>{mark(AD, H.MASTER, 0.75, 232, 24)}', label="low contrast")),
        ("Change the mark: drop the sail, move it, redraw the hull", svg((0, 0, 192, 192),
            f'<path fill="{A}" d="{B.mark_d(dict(H.MASTER, luff=56, clew=104, head=40))}"/>', label="altered")),
        ("Place it on a busy photo without its tile", svg((0, 0, 192, 192),
            '<defs><linearGradient id="ph" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="#5E8C9A"/><stop offset=".45" stop-color="#C9B48A"/>'
            '<stop offset=".7" stop-color="#2F5A4F"/><stop offset="1" stop-color="#1B2A24"/></linearGradient></defs>'
            '<rect width="192" height="192" rx="20" fill="url(#ph)"/>'
            '<circle cx="40" cy="150" r="46" fill="#E7D7B0" opacity=".7"/><circle cx="160" cy="40" r="38" fill="#244A40" opacity=".8"/>'
            + mark(A, H.MASTER, 0.7, 29, 29), label="busy background")),
    ]
    return "".join(f'<figure class="dont">{s}<figcaption>{E(t)}</figcaption></figure>' for t, s in items)


def files_table():
    rows = [
        ("mark/homerslab-mark.svg", "The master: black, 192 × 192, tight. Any one-colour use; recolour this for the UI (fill=currentColor)."),
        ("mark/homerslab-mark-teal.svg", "The mark in the light-mode teal, for light backgrounds."),
        ("mark/homerslab-mark-teal-dark.svg · -white.svg", "For dark backgrounds and photos at 64 px and up: the reversed cut, 1 unit thinner on every edge."),
        ("mark/homerslab-mark-16*.svg · -32*.svg", "The pixel cuts. Draw them at exactly 16 or 32 px (or 2×). At 24 and 48 px use the master: it lands on whole pixels there."),
        ("mark/png/", "PNG renders of the mark (256–1024 px)."),
        ("lockups/homerslab-{horizontal,stacked}-{light,dark,black,white}.svg", "The two lockups in four looks; png/ has 1200 and 800 px renders of light and dark."),
        ("icons/favicon.svg · favicon.ico · favicon-16/32/48.png", "Browser icons. The SVG switches to the dark-mode teal; the ICO holds the 16 cut, the 32 cut and the master at 48."),
        ("icons/apple-touch-icon.png · icon-192/512.png · maskable-512.png", "Phone and PWA icons; site.webmanifest and head-snippet.html wire them up."),
        ("icons/app-icon-{light,dark}.svg + PNGs", "The desktop family app's icon (macOS grid: 824 px tile in 1024). Feed the 1024 PNG to <code>tauri icon</code>."),
        ("icons/social-preview.png · .svg", "GitHub's social preview, 1280 × 640."),
        ("palette/palette.json · tokens.css", "Every colour in both teals with HEX, RGB, CMYK, Pantone and OKLCH; CSS tokens for light and dark."),
        ("tests/", "The skill's audit and test sheet, favicon-on-tab and app-icon tests, small sizes, irradiation, contrast."),
        ("source/", "How it is made: harbour.py (geometry), outline.py (the wordmark from Figtree), build.py, tests.py, guide.py."),
    ]
    return "".join(f"<tr><td><code>{E(f)}</code></td><td>{t}</td></tr>" for f, t in rows)


CSS = """
:root{--ground:#F5F5F7;--card:#FFFFFF;--text:#1D1D1F;--muted:#6E6E73;--sep:rgba(60,60,67,.16);--fill:rgba(120,120,128,.12);
--accent:__A__;--on-accent:#FFFFFF;--bad:#D70015;--good:#248A3D;--warn:#B25000;--radius:22px;
--font:-apple-system,BlinkMacSystemFont,"SF Pro Text","Inter",system-ui,"Segoe UI",sans-serif;color-scheme:light dark}
@media (prefers-color-scheme:dark){:root:not([data-theme="light"]){--ground:#000000;--card:#1C1C1E;--text:#F5F5F7;--muted:#98989D;
--sep:rgba(84,84,88,.6);--fill:rgba(120,120,128,.24);--accent:__AD__;--on-accent:#000000;--bad:#FF6961;--good:#30D158;--warn:#FFB340}}
:root[data-theme="dark"]{--ground:#000000;--card:#1C1C1E;--text:#F5F5F7;--muted:#98989D;--sep:rgba(84,84,88,.6);
--fill:rgba(120,120,128,.24);--accent:__AD__;--on-accent:#000000;--bad:#FF6961;--good:#30D158;--warn:#FFB340}
*{box-sizing:border-box}html,body{margin:0}
body{background:var(--ground);color:var(--text);font:400 15px/1.5 var(--font);letter-spacing:-.01em;-webkit-font-smoothing:antialiased}
.wrap{max-width:1120px;margin:0 auto;padding:0 16px 64px}
a{color:var(--accent)}code{font:12.5px/1.4 ui-monospace,"SF Mono",Menlo,Consolas,monospace;background:var(--fill);padding:1px 5px;border-radius:6px}
h1{font-size:40px;line-height:1.08;letter-spacing:-.025em;margin:0 0 10px;font-weight:700}
h2{font-size:24px;letter-spacing:-.02em;margin:0 0 6px;font-weight:650}
h3{font-size:17px;margin:0;font-weight:650}
p{margin:0 0 12px;max-width:72ch}.muted{color:var(--muted)}.small{font-size:13px}
.hero{padding:64px 0 28px;display:grid;gap:28px}
.hero .logo{width:min(560px,100%)}
.hero .lede{font-size:19px;line-height:1.45;max-width:60ch}
.lockup-light{display:block}.lockup-dark{display:none}
@media (prefers-color-scheme:dark){:root:not([data-theme="light"]) .lockup-light{display:none}:root:not([data-theme="light"]) .lockup-dark{display:block}}:root[data-theme="dark"] .lockup-light{display:none}:root[data-theme="dark"] .lockup-dark{display:block}
section{background:var(--card);border-radius:var(--radius);padding:28px;margin:18px 0;box-shadow:0 1px 3px rgba(0,0,0,.05)}
@media (prefers-color-scheme:dark){section{box-shadow:none}}:root[data-theme="dark"] section{box-shadow:none}
.kicker{font-size:12px;font-weight:600;letter-spacing:.06em;text-transform:uppercase;color:var(--muted);margin:0 0 6px}
.grid2{display:grid;grid-template-columns:repeat(auto-fit,minmax(300px,1fr));gap:20px;align-items:start}
.grid3{display:grid;grid-template-columns:repeat(auto-fit,minmax(220px,1fr));gap:16px}
.tile{border-radius:16px;padding:20px;display:flex;align-items:center;justify-content:center;min-height:150px}
.tile svg{max-width:100%;height:auto}
.on-ground{background:var(--ground)}.on-light{background:#F5F5F7}.on-white{background:#fff;border:1px solid var(--sep)}
.on-dark{background:#000}.on-card-dark{background:#1C1C1E}.on-accent{background:__A__}
figure{margin:0}figcaption{font-size:12.5px;color:var(--muted);margin-top:8px;line-height:1.4}
.construct{width:100%;max-width:520px;height:auto}
.construct .g{stroke:var(--sep);stroke-width:.25}.construct .g6{stroke:var(--muted);stroke-opacity:.45;stroke-width:.35}
.construct .ink{opacity:.28}.construct .k{stroke:var(--text);stroke-opacity:.5;stroke-width:.35;stroke-dasharray:2 2}
.construct .c{fill:none;stroke:#E8505B;stroke-width:.6}.construct .c2{stroke:#7D5BD6}.construct .rad{stroke-dasharray:2 2;stroke-width:.4}
.construct .dot{fill:#E8505B}.construct .d2{fill:#7D5BD6}
.construct .lb{font:5.4px var(--font);fill:var(--text)}.construct .l2{fill:#7D5BD6}.construct .mid{text-anchor:middle}
.clear{width:100%;height:auto}.clear .cs{fill:none;stroke:#E8505B;stroke-width:2;stroke-dasharray:8 6}
.clear .cs2{fill:none;stroke:var(--muted);stroke-width:1;stroke-opacity:.5}.clear .cu{fill:#E8505B;fill-opacity:.12;stroke:#E8505B;stroke-opacity:.4}
.clear .cu2{fill:#E8505B;fill-opacity:.18}
table{border-collapse:collapse;width:100%;font-size:13.5px}
th,td{text-align:left;padding:9px 10px;border-bottom:1px solid var(--sep);vertical-align:top}
thead th{font-size:12px;color:var(--muted);font-weight:600}
.spec{display:grid;grid-template-columns:auto 1fr;gap:3px 12px;font-size:13px;margin:10px 2px 0}.spec dt{color:var(--muted)}.spec dd{margin:0;font-variant-numeric:tabular-nums}.de{color:var(--muted);font-size:11.5px}
.tablewrap{overflow-x:auto;-webkit-overflow-scrolling:touch}
.chip{display:inline-block;width:14px;height:14px;border-radius:4px;vertical-align:-2px;margin-right:8px;box-shadow:inset 0 0 0 1px rgba(127,127,127,.35)}
.variants{display:grid;grid-template-columns:repeat(auto-fit,minmax(min(100%,470px),1fr));gap:18px}
.variant{border:1px solid var(--sep);border-radius:18px;padding:20px;min-width:0}
.variant.rec{border:2px solid var(--accent)}
.variant header{display:flex;align-items:center;justify-content:space-between;gap:12px;margin-bottom:4px}
.badge{font-size:12px;font-weight:600;background:var(--accent);color:var(--on-accent);padding:3px 10px;border-radius:99px}
.badge.alt{background:var(--fill);color:var(--text)}
.pair{display:grid;grid-template-columns:1fr 1fr;gap:10px;margin:12px 0}
.swatch{border-radius:14px;padding:14px;min-height:84px;display:flex;flex-direction:column;justify-content:flex-end;font-size:13px}
.swatch b{font-size:15px}.swatch span{opacity:.85}
.demo{border-radius:14px;padding:18px 22px;margin:8px 0}.demo svg{width:100%;height:auto;max-height:54px;display:block}
.demo.light{background:#F5F5F7}.demo.dark{background:#000}
.icons{display:flex;gap:12px;align-items:center;flex-wrap:wrap;margin:8px 0;padding:12px;border-radius:14px;background:#E8E8ED}
.tabs{display:grid;gap:6px;flex:1;min-width:220px}
.strip{display:flex;gap:2px;padding:6px 8px 0;border-radius:10px 10px 0 0}
.tab{display:flex;align-items:center;gap:7px;height:30px;padding:0 10px;border-radius:8px 8px 0 0;font:12px var(--font);white-space:nowrap}
.tab svg{flex:none}
details{margin-top:10px}summary{cursor:pointer;font-weight:600;font-size:13.5px}
.contrast{list-style:none;padding:0;margin:8px 0 0;font-size:13px;display:grid;gap:4px}
.ratio{display:inline-block;min-width:52px;font-weight:650;font-variant-numeric:tabular-nums}
.ratio.ok{color:var(--good)}.ratio.mid{color:var(--warn)}.ratio.bad{color:var(--bad)}
.callout{background:var(--fill);border-radius:16px;padding:16px 18px;margin:16px 0 0}
.callout b{color:var(--text)}
.donts{display:grid;grid-template-columns:repeat(auto-fit,minmax(160px,1fr));gap:14px}
.dont{background:#F5F5F7;border-radius:14px;padding:14px;position:relative}.dont figcaption{color:#6E6E73}
.dont svg{width:100%;height:96px;display:block}
.dont::after{content:"";position:absolute;top:10px;right:10px;width:22px;height:22px;border-radius:50%;
background:var(--bad) url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 22 22'%3E%3Cpath d='M7 7l8 8M15 7l-8 8' stroke='white' stroke-width='2.2' stroke-linecap='round'/%3E%3C/svg%3E")}
.dos li{margin:4px 0}
.type-sample{display:grid;gap:14px}
.ui-sample{border:1px solid var(--sep);border-radius:14px;padding:14px 16px;display:flex;align-items:center;gap:14px;flex-wrap:wrap}
.ui-sample svg{height:28px;width:auto}
.ui-sample nav{display:flex;gap:16px;font-size:14px;color:var(--muted)}.ui-sample .btn{margin-left:auto;background:var(--accent);
color:var(--on-accent);font-weight:600;font-size:14px;padding:7px 14px;border-radius:99px}
.toc{display:flex;flex-wrap:wrap;gap:8px;margin-top:8px}.toc a{text-decoration:none;color:var(--text);background:var(--card);
padding:6px 12px;border-radius:99px;font-size:13.5px;box-shadow:0 1px 2px rgba(0,0,0,.05)}
.sizes td:first-child{white-space:nowrap}
@media (max-width:600px){h1{font-size:31px}section{padding:20px;border-radius:18px}.hero{padding-top:36px}}
"""


def main():
    pal = json.load(open(os.path.join(KIT, "palette", "palette.json")))
    BUILT = B.VARIANTS[pal["built_with"]]                   # the teal the kit's files are in
    A, AD = BUILT["light"], BUILT["dark"]
    vb, light = B.horizontal(A, "#1D1D1F")
    _, dark = B.horizontal(AD, "#F5F5F7", True)
    hero = (f'<div class="logo">{svg(vb, light, label="HomersLab", cls="lockup-light")}'
            f'{svg(vb, dark, label="HomersLab", cls="lockup-dark")}</div>')
    vbs, st_light = B.stacked(A, "#1D1D1F")
    _, st_dark = B.stacked(AD, "#F5F5F7", True)
    _, h_black = B.horizontal("#000000", "#000000")
    _, h_white = B.horizontal("#FFFFFF", "#FFFFFF", True)
    w = OUT["wordmark"]

    neutrals = "".join(
        f'<tr><th>{E(n["name"])}<div class="muted small">{E(n["use"])}</div></th>'
        f'<td><span class="chip" style="background:{n["light"]}"></span>{n["light"]}</td>'
        f'<td><span class="chip" style="background:{n["dark"]}"></span>{n["dark"]}</td>'
        f'<td>{rgb_txt(n["light"])}<br>{rgb_txt(n["dark"])}</td><td>{cmyk_txt(n["light"])}<br>{cmyk_txt(n["dark"])}</td>'
        f'<td>{pantone_txt(n["light"])}<br>{pantone_txt(n["dark"])}</td></tr>' for n in B.NEUTRALS)

    versions = f'''
<div class="grid3">
  <figure><div class="tile on-light">{svg((0, 0, 192, 192), mark(A), 120, 120, "Symbol, teal")}</div><figcaption><b>Symbol</b> · <code>mark/homerslab-mark-teal.svg</code>. App icons, favicons, avatars, wherever the name is already there.</figcaption></figure>
  <figure><div class="tile on-light">{svg(vb, light, label="Horizontal lockup")}</div><figcaption><b>Horizontal lockup</b>, the default · <code>lockups/homerslab-horizontal-light.svg</code></figcaption></figure>
  <figure><div class="tile on-light">{svg(vbs, st_light, 200, None, "Stacked lockup")}</div><figcaption><b>Stacked lockup</b> for square spaces · <code>lockups/homerslab-stacked-light.svg</code></figcaption></figure>
  <figure><div class="tile on-dark">{svg(vb, dark, label="Horizontal lockup, dark")}</div><figcaption><b>Dark</b> · the dark-mode teal and a light wordmark, drawn 1 unit thinner (the reversed cut, so it doesn't look heavier) · <code>-dark.svg</code></figcaption></figure>
  <figure><div class="tile on-white">{svg(vb, h_black, label="One colour, black")}</div><figcaption><b>One colour</b> · black for print, stamps, engraving · <code>-black.svg</code></figcaption></figure>
  <figure><div class="tile on-accent">{svg(vb, h_white, label="One colour, white")}</div><figcaption><b>Reversed</b> · white on the teal, on dark or on photos (with its tile) · <code>-white.svg</code></figcaption></figure>
</div>'''

    sm = []
    for n, P, label, note in ((16, H.CUT16, "16 px cut", "Its own drawing: every edge on a whole pixel, the hull flat-keeled, the sail as big as the counter allows (1 px clear of the deck and the stem)."),
                              (24, H.MASTER, "24 px: the master", "The master is built on 8-unit modules (24 × 24), so at 24 px and 48 px every straight edge lands on a pixel."),
                              (32, H.CUT32, "32 px cut", "The master's proportions snapped to the 32 px grid: deck at 17 px, hull to 20, keel 23, sail 14–22 × 1–15.")):
        vbn = (0, 0, P["size"], P["size"]) if P is not H.MASTER else (0, 0, 192, 192)
        sm.append(f'<figure><div class="tile on-light" style="gap:18px;min-height:120px">{svg(vbn, mark(A, P), n, n, label)}'
                  f'{svg(vbn, mark(A, P), n * 4, n * 4, label)}</div><figcaption><b>{label}</b>. {note}</figcaption></figure>')

    page = f'''<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>HomersLab logo guide</title>
<meta name="description" content="How to use the HomersLab logo: versions, clear space, minimum sizes, colour, type and icons.">
<link rel="icon" href="icons/favicon.svg" type="image/svg+xml">
<style>{CSS.replace("__A__", A).replace("__AD__", AD)}</style></head>
<body><div class="wrap">

<header class="hero">
  {hero}
  <div>
    <h1>The Harbour H</h1>
    <p class="lede">An H for Home, Homer and Harbour: its crossbar is a hull and a sail rises in the counter above it. The ship, safe home.
    Below the hull, the counter is a doorway.</p>
    <p class="muted small">Logo guide · 2026-11-10 · kit built in {E(B.VARIANTS[pal["built_with"]]["name"])} · the colour is still to be chosen between two teals (<a href="#colour">Colour</a>).</p>
    <nav class="toc"><a href="#mark">The mark</a><a href="#versions">Versions</a><a href="#space">Clear space and sizes</a><a href="#colour">Colour</a><a href="#type">Type</a><a href="#icons">Icons</a><a href="#donts">Do and don’t</a><a href="#files">Files</a></nav>
  </div>
</header>

<section id="mark">
  <p class="kicker">The mark</p>
  <div class="grid2">
    <div>
      <h2>Built from a few round numbers</h2>
      <p>The H is 24 × 24 modules of 8 units. Each stem is 6 modules wide with continuous-curvature corners (radius 14, 60 % smoothing,
      the kind Apple uses: as round to the eye as round 1’s, without a kink where the curve starts). The deck, the H’s crossbar, sits at 13 modules; the hull is 2 modules deep at the stems and its underside is one
      circle of radius 10 modules, so the keel lands on 17 (a 3–4–5 triangle: 48, 64, 80). The sail stands on 11 modules, its mast side at 10,
      its clew at 16; its back edge is one arc bellied 23°, every corner radius 3. The sail keeps 2 modules from the deck and from the stem,
      so it stays a separate shape down to 24 px.</p>
      <p class="muted small">Refined from round 1: equal gaps around the sail (the right one was 11 units and closed up at 32 px), the sail rebuilt as
      one arc, smooth corners, the hull on a clean circle, everything on the module grid. Its visual centre sits 1 % above the geometric
      centre, where the eye expects it.</p>
    </div>
    <figure>{construction(A)}<figcaption>Construction. Grey lines: modules (bold every 6). Red: the hull’s circle. Violet: the sail’s leech.</figcaption></figure>
  </div>
  <h3 style="margin-top:18px">Small sizes</h3>
  <p class="muted small">Below 48 px the mark has two hand-fitted cuts. Use the file for the size you draw; never scale the 16 px cut up.</p>
  <div class="grid3">{"".join(sm)}</div>
</section>

<section id="versions">
  <p class="kicker">Versions</p>
  <h2>Symbol, two lockups, four looks</h2>
  <p>The horizontal lockup is the default. Its wordmark stands on the keel line with capitals 11 modules tall, one stem (6 modules) from the
  symbol. The stacked lockup sets the wordmark 7 modules tall, 5 modules under the symbol. The wordmark never appears without the symbol.</p>
  {versions}
</section>

<section id="space">
  <p class="kicker">Clear space and minimum sizes</p>
  <div class="grid2">
    <div>
      <h2>One stem of air</h2>
      <p>Keep a clear space of one stem width, a quarter of the symbol’s height, on every side of the symbol or a lockup. It scales with the logo.
      Nothing else enters it: no text, no edges, no other logos.</p>
      <div class="tablewrap"><table class="sizes"><thead><tr><th>Version</th><th>Screen, at least</th><th>Print, at least</th></tr></thead><tbody>
      <tr><td>Symbol</td><td>16 px (the 16 px cut); 24 px with the master</td><td>5 mm</td></tr>
      <tr><td>Horizontal lockup</td><td>24 px tall (108 px wide)</td><td>6 mm tall (27 mm wide)</td></tr>
      <tr><td>Stacked lockup</td><td>64 px tall</td><td>16 mm tall</td></tr>
      <tr><td>App icon / tiles</td><td>the mark at half the tile’s width</td><td>—</td></tr>
      </tbody></table></div>
    </div>
    <figure>{clearspace(A, "#1D1D1F")}<figcaption>The dashed line is the clear space; each square is one stem (48 units).</figcaption></figure>
  </div>
</section>

<section id="colour">
  <p class="kicker">Colour</p>
  <h2>One teal, light and dark, on Apple’s neutrals</h2>
  <p>One accent colour carries the brand; everything else is Apple’s system greys, as on classic’s home page. The accent has a light-mode
  value for light backgrounds and a lighter dark-mode value for dark ones. Two teals are on the table: classic’s, and one turned toward blue
  (“teal blue”). Same lightness and chroma, so contrast is the same; only the hue differs.</p>
  <div class="variants">{variant_card(VC, REC == "classic")}{variant_card(VB, REC == "blue")}</div>
  <div class="callout"><p><b>Recommendation: Classic teal.</b> Self-hosting is a blue category (Home Assistant, Nextcloud, Portainer, TrueNAS,
  CasaOS); the green-leaning teal stands apart from all of them, and on a phone it doesn’t join the crowd of blue icons. It is the colour the
  family already knows from classic’s home page, it reads warmer next to the brief’s “calm, warm”, and it has close Pantone matches
  (7719 C, ΔE00 1.7; 7472 C, 2.2) where the blue teal’s light value has none closer than 3.8. Pick the blue teal if “teal blue” meant
  bluer: it passes every check too. To switch the kit: <code>python3 source/build.py --teal blue</code>, then
  <code>tests.py --teal blue</code> and <code>guide.py</code> in the same folder.</p></div>
  <h3 style="margin:22px 0 6px">Neutrals</h3>
  <div class="tablewrap"><table><thead><tr><th>Token</th><th>Light</th><th>Dark</th><th>RGB</th><th>CMYK ≈</th><th>Pantone ≈</th></tr></thead>
  <tbody>{neutrals}</tbody></table></div>
  <h3 style="margin:22px 0 6px">Rules</h3>
  <ul class="dos">
    <li>On the light-mode teal, use white (6.1:1). On the dark-mode teal, use black (8.5:1); white on it is only 2.5:1.</li>
    <li>The light-mode teal is for light backgrounds, the dark-mode teal for dark ones. Swapped, they fall to 3.5:1 and 2.5:1.</li>
    <li>Accent as text or icons: both teals pass AA (4.5:1) on Ground and Card in their own mode.</li>
    <li>Greyscale and one-colour versions carry the whole mark; no meaning sits in colour alone.</li>
  </ul>
  <p class="muted small">CMYK: converted to U.S. Web Coated (SWOP), relative colorimetric; greys as black only. Ask the printer for theirs (in Europe usually
  PSO Coated v3). Pantone: the nearest PANTONE Solid Coated by CIEDE2000 against Pantone’s published sRGB values; approximate, so check a
  physical guide before printing. All values: <code>palette/palette.json</code>, CSS: <code>palette/tokens.css</code>.</p>
</section>

<section id="type">
  <p class="kicker">Type</p>
  <div class="grid2">
    <div>
      <h2>Figtree for the name, the system font for everything else</h2>
      <p>The product speaks in the system font (SF on Apple devices, Inter elsewhere, as in classic). SF’s licence doesn’t allow it in a
      logo, so the wordmark is set in <b>Figtree SemiBold</b> (SIL Open Font License, logo use allowed) and converted to outlines: no font is
      needed to show it.</p>
      <p>Why Figtree: its proportions and low stroke contrast sit easily next to SF, so the name and the interface look related; its round
      o, e and a echo the hull’s arc and soften the heavy symbol, which suits “calm, warm”; and it is different enough from SF and
      Inter that the name reads as a logo, not as a label. Inter was the other candidate; it is what the UI already shows on Linux and Windows,
      so the name would look like plain text there.</p>
      <p class="muted small">Custom: tracking −10/1000 em, H–o closed by 20 and s–L by 15, every outer corner rounded at a quarter of
      the stem width, as round as the symbol’s corners look. On dark backgrounds the wordmark is weight 580 instead of 600, about 3 %
      thinner. Figtree also sets brand moments such as the social card; never retype the wordmark, use the files.</p>
    </div>
    <div class="type-sample">
      <div class="ui-sample">{svg(vb, f'<path style="fill:var(--accent)" d="{B.mark_d(H.MASTER)}"/><path style="fill:var(--text)" d="{B.transform_d(w["d"], 1.0, B.S + B.GAP - w["bbox"][0], B.BASELINE)}"/>', label="HomersLab")}
        <nav><span>Apps</span><span>Catalog</span><span>People</span></nav><span class="btn">Install</span></div>
      <p class="muted small">The lockup next to live text in this device’s system font: SF on a Mac or iPhone, the platform’s UI font elsewhere.</p>
      <div class="tablewrap"><table><tbody>
        <tr><th>Wordmark</th><td>Figtree SemiBold 600, outlined (580 when reversed)</td></tr>
        <tr><th>Brand moments</th><td>Figtree 500–600 (social cards, a website hero)</td></tr>
        <tr><th>Interface, documents</th><td><code>-apple-system, BlinkMacSystemFont, "Inter", system-ui, sans-serif</code></td></tr>
        <tr><th>Licences</th><td>Figtree and Inter: SIL OFL 1.1. SF: Apple’s licence, system UI only.</td></tr>
      </tbody></table></div>
    </div>
  </div>
</section>

<section id="icons">
  <p class="kicker">Icons</p>
  <h2>Favicon, phone, desktop</h2>
  <div class="grid3">
    <figure><div class="tile on-light" style="gap:16px">{fav(A, 16)}{fav(A, 32, H.CUT32, 32)}{svg((0, 0, 192, 192), mark(A), 48, 48, "favicon 48")}</div>
      <figcaption><b>Favicon</b> · <code>favicon.svg</code> (16 px cut, switches to the dark-mode teal in dark browsers), <code>favicon.ico</code> with 16/32/48.
      Paste <code>icons/head-snippet.html</code> into the page’s head.</figcaption></figure>
    <figure><div class="tile on-light" style="gap:16px">{svg((0, 0, 180, 180), f'<rect width="180" height="180" rx="40" fill="{A}"/>' + mark("#FFFFFF", H.REVERSED, 90 / 192, 45, 45), 72, 72, "apple-touch-icon")}
      {svg((0, 0, 512, 512), f'<circle cx="256" cy="256" r="256" fill="{A}"/>' + mark("#FFFFFF", H.REVERSED, 256 / 192, 128, 128), 72, 72, "maskable")}</div>
      <figcaption><b>Phone and PWA</b> · apple-touch-icon (180), icon-192/512, maskable-512 (shown masked to a circle; the mark stays inside the 80 % safe zone).</figcaption></figure>
    <figure><div class="tile on-light" style="gap:16px">{tile_icon(BUILT, "light", 88)}{tile_icon(BUILT, "dark", 88)}</div>
      <figcaption><b>Desktop family app</b> · <code>app-icon-light.svg</code>, <code>app-icon-dark.svg</code>: the macOS grid, white or dark-mode teal mark at half the tile.</figcaption></figure>
  </div>
  <p class="muted small" style="margin-top:14px">Tested on browser tabs (light and dark Chrome, Firefox, Safari colours, 1× and 2×): the SVG favicon holds 4.1–4.9:1 on dark tabs
  and 4.6–6.1:1 on light ones. Browsers that read only the ICO show the light-mode teal on dark tabs at about 2:1: there, the shape carries it.
  See <code>tests/favicon-tabs-1x.png</code> and <code>-2x.png</code>.</p>
</section>

<section id="donts">
  <p class="kicker">Do and don’t</p>
  <div class="grid2">
    <div><h2>Do</h2><ul class="dos">
      <li>Use the light files on light backgrounds and the dark files on dark ones.</li>
      <li>Use the symbol alone where the name is already present: app icons, tabs, avatars, the app’s own header next to its title.</li>
      <li>Give it its clear space, and the size file that matches the size you draw (16, 24, 32, 48 px).</li>
      <li>On photos, use the white version on a calm area, or the app-icon tile.</li>
      <li>In the UI, inline the master’s path with <code>fill="currentColor"</code> and let the tokens colour it.</li>
    </ul></div>
    <div><h2>Don’t</h2><p class="muted">Each of these breaks the mark. Use the files as they are.</p></div>
  </div>
  <div class="donts">{donts(A, AD)}</div>
</section>

<section id="files">
  <p class="kicker">Files</p>
  <h2>What’s in the kit</h2>
  <div class="tablewrap"><table><tbody>{files_table()}</tbody></table></div>
  <p class="muted small" style="margin-top:12px">Not done here: a trademark search for the name and the mark (EUIPO, Swissreg, USPTO) and a
  reverse-image search before the logo goes public; Pantone proofing if anything is printed in spot colour.</p>
</section>

</div></body></html>
'''
    with open(os.path.join(KIT, "guide.html"), "w", encoding="utf-8") as fh:
        fh.write(page)
    print("wrote guide.html", len(page) // 1024, "KB")


if __name__ == "__main__":
    main()
