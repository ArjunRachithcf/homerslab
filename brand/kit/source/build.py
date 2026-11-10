#!/usr/bin/env python3
"""Build the HomersLab logo kit.

    python3 build.py                 # the recommended teal (classic)
    python3 build.py --teal blue     # the same kit in the bluer teal

Reads harbour.py (the mark's geometry) and outlines.json (the wordmark in outlines, made by outline.py).
Needs only python3 and rsvg-convert (librsvg). Writes ../mark, ../lockups, ../palette, ../icons.
tests.py and guide.py (same folder) write ../tests and ../guide.html.
"""
import argparse, json, math, os, re, struct, subprocess, sys, tempfile

sys.dont_write_bytecode = True
HERE = os.path.dirname(os.path.abspath(__file__))
KIT = os.path.dirname(HERE)
sys.path.insert(0, HERE)
import harbour as H  # noqa: E402

BRAND = "HomersLab"
TAGLINE = "An app manager for your household’s own machines"
RECOMMENDED = "classic"

# --- palette -----------------------------------------------------------------------------------------------------
# Neutrals: Apple's system palette, as on classic's home page. CMYK of greys as black only (U.S. coated / SWOP).
NEUTRALS = [
    dict(key="ground", name="Ground", use="Page background", light="#F5F5F7", dark="#000000"),
    dict(key="card", name="Card", use="Cards, sheets; the dark app icon's tile", light="#FFFFFF", dark="#1C1C1E"),
    dict(key="text", name="Text", use="Text and the wordmark", light="#1D1D1F", dark="#F5F5F7"),
    dict(key="muted", name="Muted", use="Secondary text", light="#6E6E73", dark="#98989D"),
]
VARIANTS = {
    "classic": dict(key="classic", name="Classic teal", light="#1B6E66", dark="#5FB3A9",
                    about="classic's accent, unchanged: a green-leaning teal (OKLCH hue 185°)"),
    "blue": dict(key="blue", name="Blue teal", light="#186C78", dark="#5DB0BE",
                 about="classic's lightness and chroma with the hue turned 24° toward blue (OKLCH hue 209°)"),
}
ON_ACCENT = {"light": "#FFFFFF", "dark": "#000000"}     # text/marks on the accent (classic's --on-accent)
# Approximate print values. CMYK: sRGB -> U.S. Web Coated (SWOP), relative colorimetric with black-point
# compensation (Ghostscript's Artifex SWOP profile); greys as black only. Pantone: nearest PANTONE Solid Coated by
# CIEDE2000 (dE00) against Pantone's published sRGB values; verify against a physical guide before printing.
PRINT = {
    "#1B6E66": dict(cmyk=(85, 38, 60, 19), pantone="7719 C", de=1.7),
    "#5FB3A9": dict(cmyk=(62, 10, 39, 0), pantone="7472 C", de=2.2),
    "#186C78": dict(cmyk=(86, 44, 45, 15), pantone="7474 C", de=3.8),
    "#5DB0BE": dict(cmyk=(62, 14, 24, 0), pantone="7709 C", de=1.1),
    "#1D1D1F": dict(cmyk=(0, 0, 0, 100), pantone="Neutral Black C", de=1.7),
    "#1C1C1E": dict(cmyk=(0, 0, 0, 100), pantone="Neutral Black C", de=2.0),
    "#000000": dict(cmyk=(0, 0, 0, 100), pantone="Black 6 C", de=None),
    "#6E6E73": dict(cmyk=(0, 0, 0, 70), pantone="Cool Gray 10 C", de=3.9),
    "#98989D": dict(cmyk=(0, 0, 0, 48), pantone="Cool Gray 7 C", de=2.2),
    "#F5F5F7": dict(cmyk=None, pantone=None, de=None),     # paper: leave unprinted
    "#FFFFFF": dict(cmyk=None, pantone=None, de=None),
}


def rgb(h):
    h = h.lstrip("#")
    return tuple(int(h[i:i + 2], 16) for i in (0, 2, 4))


def luminance(h):
    def lin(c):
        c /= 255
        return c / 12.92 if c <= 0.04045 else ((c + 0.055) / 1.055) ** 2.4
    r, g, b = (lin(c) for c in rgb(h))
    return 0.2126 * r + 0.7152 * g + 0.0722 * b


def contrast(a, b):
    la, lb = sorted((luminance(a), luminance(b)), reverse=True)
    return (la + 0.05) / (lb + 0.05)


def oklch(h):
    def lin(c):
        c /= 255
        return c / 12.92 if c <= 0.04045 else ((c + 0.055) / 1.055) ** 2.4
    r, g, b = (lin(c) for c in rgb(h))
    l = 0.4122214708 * r + 0.5363325363 * g + 0.0514459929 * b
    m = 0.2119034982 * r + 0.6806995451 * g + 0.1073969566 * b
    s = 0.0883024619 * r + 0.2817188376 * g + 0.6299787005 * b
    l, m, s = (math.copysign(abs(v) ** (1 / 3), v) for v in (l, m, s))
    L = 0.2104542553 * l + 0.7936177850 * m - 0.0040720468 * s
    A = 1.9779984951 * l - 2.4285922050 * m + 0.4505937099 * s
    B = 0.0259040371 * l + 0.7827717662 * m - 0.8086757660 * s
    return L, math.hypot(A, B), math.degrees(math.atan2(B, A)) % 360


def colour_record(h):
    p = PRINT.get(h, {})
    L, C, Hh = oklch(h)
    return dict(hex=h, rgb=list(rgb(h)), oklch=f"oklch({L:.3f} {C:.3f} {Hh:.1f})",
                cmyk=list(p["cmyk"]) if p.get("cmyk") else None, pantone=p.get("pantone"), pantone_de00=p.get("de"))


# --- SVG helpers -------------------------------------------------------------------------------------------------
TOKEN = re.compile(r"[MLHVCQAZ]|-?(?:\d+\.?\d*|\.\d+)(?:e-?\d+)?")
ARITY = {"M": 2, "L": 2, "H": 1, "V": 1, "C": 6, "Q": 4, "A": 7, "Z": 0}


def f2(v):
    s = f"{v:.2f}".rstrip("0").rstrip(".")
    return "0" if s in ("-0", "") else s


def transform_d(d, s=1.0, tx=0.0, ty=0.0):
    """Scale then translate an absolute-command path (M L H V C Q A Z)."""
    out, toks, i, cmd = [], TOKEN.findall(d), 0, None
    while i < len(toks):
        t = toks[i]
        if t in ARITY:
            cmd = t; i += 1
            out.append(cmd)
            if cmd == "Z":
                continue
        n = ARITY[cmd]
        args = [float(x) for x in toks[i:i + n]]; i += n
        if cmd == "H":
            vals = [args[0] * s + tx]
        elif cmd == "V":
            vals = [args[0] * s + ty]
        elif cmd == "A":
            vals = [args[0] * s, args[1] * s, args[2], args[3], args[4], args[5] * s + tx, args[6] * s + ty]
            out.append(" ".join(f2(v) if k not in (2, 3, 4) else str(int(v)) for k, v in enumerate(vals)) + " ")
            continue
        else:
            vals = [(v * s + (tx if k % 2 == 0 else ty)) for k, v in enumerate(args)]
        out.append(" ".join(f2(v) for v in vals) + " ")
    return re.sub(r" (?=[A-Z])", "", "".join(out)).strip()


def svg_doc(vb, body, title, desc=None, style=None):
    x, y, w, h = vb
    d = f"<desc>{desc}</desc>" if desc else ""
    st = f"<style>{style}</style>" if style else ""
    return (f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="{f2(x)} {f2(y)} {f2(w)} {f2(h)}" width="{f2(w)}" '
            f'height="{f2(h)}" role="img" aria-labelledby="title"><title id="title">{title}</title>{d}{st}{body}</svg>\n')


def path(d, fill):
    return f'<path fill="{fill}" d="{d}"/>'


# --- artwork -----------------------------------------------------------------------------------------------------
OUT = json.load(open(os.path.join(HERE, "outlines.json")))
S = 192          # symbol size in the lockups (the master's own units)
GAP = 48         # symbol to wordmark: one stem
BASELINE = 136   # horizontal lockup: the wordmark stands on the keel line (17 modules)
STACK_CAP = 56   # stacked lockup: cap height 7 modules
STACK_GAP = 40   # and 5 modules under the symbol


def mark_d(P, s=1.0, tx=0.0, ty=0.0):
    return transform_d(H.mark_d(P)[0], s, tx, ty)


def wordmark_d(reversed_=False, s=1.0, tx=0.0, ty=0.0):
    w = OUT["wordmark_reversed" if reversed_ else "wordmark"]
    return transform_d(w["d"], s, tx, ty), w


def horizontal(sym_fill, word_fill, reversed_=False):
    P = H.REVERSED if reversed_ else H.MASTER
    w = OUT["wordmark_reversed" if reversed_ else "wordmark"]
    x0, _, x1, _ = OUT["wordmark"]["bbox"]                    # the light wordmark fixes the layout for both
    wd, _ = wordmark_d(reversed_, 1.0, S + GAP - x0, BASELINE)
    width = math.ceil(S + GAP + (x1 - x0))                      # whole-unit canvas; the spare fraction goes right
    return (0, 0, width, S), path(mark_d(P), sym_fill) + path(wd, word_fill)


def stacked(sym_fill, word_fill, reversed_=False):
    P = H.REVERSED if reversed_ else H.MASTER
    x0, _, x1, y1 = OUT["wordmark"]["bbox"]
    k = STACK_CAP / OUT["wordmark"]["cap"]
    wordw = (x1 - x0) * k
    width = math.ceil(wordw)                                    # whole-unit canvas, the wordmark centred in it
    sx = (width - S) / 2
    base = S + STACK_GAP + STACK_CAP
    wd, _ = wordmark_d(reversed_, k, (width - wordw) / 2 - x0 * k, base)
    height = math.ceil(base + max(0.0, y1 * k))
    return (0, 0, width, height), path(mark_d(P, 1.0, sx, 0), sym_fill) + path(wd, word_fill)


# --- raster ------------------------------------------------------------------------------------------------------
def render(svg_text, png, w, h=None, bg=None):
    h = h or w
    with tempfile.NamedTemporaryFile("w", suffix=".svg", delete=False) as fh:
        fh.write(svg_text)
        tmp = fh.name
    cmd = ["rsvg-convert", "-w", str(w), "-h", str(h), tmp, "-o", png]
    if bg:
        cmd[1:1] = ["-b", bg]
    subprocess.run(cmd, check=True)
    os.unlink(tmp)


def png_size(p):
    with open(p, "rb") as fh:
        head = fh.read(24)
    return struct.unpack(">II", head[16:24])


def write_ico(pngs, ico):
    blobs = [open(p, "rb").read() for p in pngs]
    sizes = [png_size(p) for p in pngs]
    off = 6 + 16 * len(blobs)
    hdr, dirs = struct.pack("<HHH", 0, 1, len(blobs)), b""
    for (w, h), b in zip(sizes, blobs):
        dirs += struct.pack("<BBBBHHII", w % 256, h % 256, 0, 0, 1, 32, len(b), off)
        off += len(b)
    with open(ico, "wb") as fh:
        fh.write(hdr + dirs + b"".join(blobs))


def write(rel, text):
    p = os.path.join(KIT, rel)
    os.makedirs(os.path.dirname(p), exist_ok=True)
    with open(p, "w", encoding="utf-8") as fh:
        fh.write(text)
    return p


def rounded_square(x, y, size, r, xi=0.6):
    """A square with continuous-curvature corners (the app-icon tile)."""
    pth = H.Path()
    p = H.corner_extent(r, xi)
    x1, y1 = x + size, y + size
    pth.M((x + p, y)); pth.L((x1 - p, y)); H.corner(pth, (x1, y), (1, 0), (0, 1), r, xi)
    pth.L((x1, y1 - p)); H.corner(pth, (x1, y1), (0, 1), (-1, 0), r, xi)
    pth.L((x + p, y1)); H.corner(pth, (x, y1), (-1, 0), (0, -1), r, xi)
    pth.L((x, y + p)); H.corner(pth, (x, y), (0, -1), (1, 0), r, xi)
    pth.Z()
    return pth.d()


# --- the kit -----------------------------------------------------------------------------------------------------
def build(teal):
    V = VARIANTS[teal]
    A, AD = V["light"], V["dark"]
    TEXT_L, TEXT_D = "#1D1D1F", "#F5F5F7"
    made = []
    T = f"{BRAND}"

    # mark --------------------------------------------------------------------------------------------------------
    vb = (0, 0, 192, 192)
    marks = {
        "homerslab-mark.svg": (H.MASTER, "#000000", "one colour, black (the master)"),
        "homerslab-mark-teal.svg": (H.MASTER, A, f"{V['name']}, for light backgrounds"),
        "homerslab-mark-teal-dark.svg": (H.REVERSED, AD, f"{V['name']} (dark mode), for dark backgrounds; reversed cut"),
        "homerslab-mark-white.svg": (H.REVERSED, "#FFFFFF", "one colour, white, for dark backgrounds and photos; reversed cut"),
    }
    for name, (P, fill, desc) in marks.items():
        made.append(write(f"mark/{name}", svg_doc(vb, path(mark_d(P), fill), T, desc)))
    for cut, P in (("16", H.CUT16), ("32", H.CUT32)):
        n = int(cut)
        for suffix, fill, desc in (("", "#000000", "black"), ("-teal", A, V["name"]), ("-teal-dark", AD, f"{V['name']} (dark mode)")):
            made.append(write(f"mark/homerslab-mark-{cut}{suffix}.svg",
                              svg_doc((0, 0, n, n), path(mark_d(P), fill), T, f"{cut} px cut, {desc}: draw it at exactly {cut} px")))
    os.makedirs(os.path.join(KIT, "mark/png"), exist_ok=True)
    for name, size in (("homerslab-mark-teal", 256), ("homerslab-mark-teal", 512), ("homerslab-mark-teal", 1024),
                       ("homerslab-mark", 1024), ("homerslab-mark-white", 1024), ("homerslab-mark-teal-dark", 1024)):
        src = open(os.path.join(KIT, f"mark/{name}.svg")).read()
        render(src, os.path.join(KIT, f"mark/png/{name}-{size}.png"), size)

    # lockups -----------------------------------------------------------------------------------------------------
    looks = {
        "light": (A, TEXT_L, False, f"{V['name']} symbol, Text wordmark: for light backgrounds"),
        "dark": (AD, TEXT_D, True, f"{V['name']} (dark mode) symbol, light wordmark: for dark backgrounds; reversed cut"),
        "black": ("#000000", "#000000", False, "one colour, black"),
        "white": ("#FFFFFF", "#FFFFFF", True, "one colour, white; reversed cut"),
    }
    for layout, fn in (("horizontal", horizontal), ("stacked", stacked)):
        for look, (sf, wf, rev, desc) in looks.items():
            vbx, body = fn(sf, wf, rev)
            made.append(write(f"lockups/homerslab-{layout}-{look}.svg", svg_doc(vbx, body, T, f"{layout} lockup, {desc}")))
    os.makedirs(os.path.join(KIT, "lockups/png"), exist_ok=True)
    for layout, width in (("horizontal", 1200), ("stacked", 800)):
        for look in ("light", "dark"):
            src = open(os.path.join(KIT, f"lockups/homerslab-{layout}-{look}.svg")).read()
            vw, vh = [float(v) for v in re.search(r'viewBox="0 0 ([\d.]+) ([\d.]+)"', src).groups()]
            render(src, os.path.join(KIT, f"lockups/png/homerslab-{layout}-{look}-{width}.png"), width, round(width * vh / vw))

    # icons -------------------------------------------------------------------------------------------------------
    d16, d32 = mark_d(H.CUT16), mark_d(H.CUT32)
    fav_style = f"path{{fill:{A}}}@media (prefers-color-scheme:dark){{path{{fill:{AD}}}}}"
    made.append(write("icons/favicon.svg", svg_doc((0, 0, 16, 16), f'<path d="{d16}"/>', T,
                                                    "favicon: the 16 px cut; the dark-mode teal when the browser is dark",
                                                    style=fav_style).replace(' width="16" height="16"', "")))
    ico = []
    for n, d, src_vb in ((16, d16, (0, 0, 16, 16)), (32, d32, (0, 0, 32, 32)), (48, mark_d(H.MASTER), (0, 0, 192, 192))):
        p = os.path.join(KIT, f"icons/favicon-{n}.png")
        render(svg_doc(src_vb, path(d, A), T), p, n)
        ico.append(p)
    write_ico(ico, os.path.join(KIT, "icons/favicon.ico"))

    def tile_icon(size, fill_bg, mark_fill, scale, rounded, P=H.REVERSED, dy=0.0):
        """Mark on a tile, `size` px. scale = the mark's height as a share of the canvas."""
        m = size * scale
        k = m / 192
        tx, ty = (size - m) / 2, (size - m) / 2 + dy * size
        bg = (f'<path fill="{fill_bg}" d="{rounded_square(0, 0, size, size * 0.225)}"/>' if rounded
              else f'<rect width="{size}" height="{size}" fill="{fill_bg}"/>')
        return svg_doc((0, 0, size, size), bg + path(mark_d(P, k, tx, ty), mark_fill), T)

    jobs = [("apple-touch-icon.png", 180, False, 0.5), ("icon-192.png", 192, True, 0.5), ("icon-512.png", 512, True, 0.5),
            ("maskable-512.png", 512, False, 0.5)]
    for fname, size, rounded, scale in jobs:
        render(tile_icon(size, A, ON_ACCENT["light"], scale, rounded), os.path.join(KIT, "icons", fname), size)
    manifest = {"name": BRAND, "short_name": BRAND, "description": TAGLINE,
                "icons": [{"src": "/icon-192.png", "sizes": "192x192", "type": "image/png"},
                          {"src": "/icon-512.png", "sizes": "512x512", "type": "image/png"},
                          {"src": "/maskable-512.png", "sizes": "512x512", "type": "image/png", "purpose": "maskable"}],
                "theme_color": "#F5F5F7", "background_color": "#F5F5F7", "display": "standalone"}
    write("icons/site.webmanifest", json.dumps(manifest, indent=2) + "\n")
    write("icons/head-snippet.html",
          '<link rel="icon" href="/favicon.ico" sizes="32x32">\n'
          '<link rel="icon" href="/favicon.svg" type="image/svg+xml">\n'
          '<link rel="apple-touch-icon" href="/apple-touch-icon.png">\n'
          '<link rel="manifest" href="/site.webmanifest">\n'
          '<meta name="theme-color" content="#F5F5F7" media="(prefers-color-scheme: light)">\n'
          '<meta name="theme-color" content="#000000" media="(prefers-color-scheme: dark)">\n')

    # desktop app icon: macOS grid (824 px tile with continuous corners in a 1024 canvas), light and dark
    def app_icon(look):
        tile = A if look == "light" else "#1C1C1E"
        mk = "#FFFFFF" if look == "light" else AD
        m = 824 * 0.5
        k = m / 192
        body = (f'<path fill="{tile}" d="{rounded_square(100, 100, 824, 185.4)}"/>'
                + path(mark_d(H.REVERSED, k, 512 - m / 2, 512 - m / 2), mk))
        return svg_doc((0, 0, 1024, 1024), body, T, f"desktop app icon, {look}")
    for look in ("light", "dark"):
        src = app_icon(look)
        made.append(write(f"icons/app-icon-{look}.svg", src))
        for size in (1024, 512, 256, 128):
            render(src, os.path.join(KIT, f"icons/app-icon-{look}-{size}.png"), size)

    # GitHub social preview, 1280 x 640
    vbx, body = horizontal(A, TEXT_L, False)
    lw, lh = vbx[2], vbx[3]
    k = 168 / lh
    lx, ly = (1280 - lw * k) / 2, 196
    tg = OUT["tagline"]
    tk = 22 / tg["cap"]
    tw = (tg["bbox"][2] - tg["bbox"][0]) * tk
    tag = transform_d(tg["d"], tk, (1280 - tw) / 2 - tg["bbox"][0] * tk, ly + lh * k + 76)
    inner = re.sub(r'<path fill="([^"]+)" d="([^"]+)"/>',
                   lambda m: f'<path fill="{m.group(1)}" d="{transform_d(m.group(2), k, lx, ly)}"/>', body)
    social = svg_doc((0, 0, 1280, 640), f'<rect width="1280" height="640" fill="#F5F5F7"/>' + inner + path(tag, "#6E6E73"),
                     T, "GitHub social preview, 1280 x 640")
    made.append(write("icons/social-preview.svg", social))
    render(social, os.path.join(KIT, "icons/social-preview.png"), 1280, 640)

    # palette -----------------------------------------------------------------------------------------------------
    pal = dict(brand=BRAND, recommended=RECOMMENDED, built_with=teal,
               notes=["CMYK: sRGB to U.S. Web Coated (SWOP), relative colorimetric with black-point compensation; "
                      "greys as black only. Ask the printer for their profile (in Europe usually PSO Coated v3).",
                      "Pantone: nearest PANTONE Solid Coated by CIEDE2000 against Pantone's published sRGB values. "
                      "Approximate: check a physical guide before printing.",
                      "On the light accent use white; on the dark-mode accent use black (white on it is only 2.5:1)."],
               accents={}, neutrals=[], on_accent=ON_ACCENT)
    for key, v in VARIANTS.items():
        pal["accents"][key] = dict(name=v["name"], about=v["about"], light=colour_record(v["light"]),
                                   dark=colour_record(v["dark"]),
                                   contrast=contrast_table(v["light"], v["dark"]))
    for n in NEUTRALS:
        pal["neutrals"].append(dict(name=n["name"], use=n["use"], light=colour_record(n["light"]), dark=colour_record(n["dark"])))
    write("palette/palette.json", json.dumps(pal, indent=1) + "\n")
    css = [f"/* {BRAND} colour tokens. Built with: {VARIANTS[teal]['name']}. Light first, dark under prefers-color-scheme. */",
           ":root {", f"  --ground: #F5F5F7; --card: #FFFFFF; --text: #1D1D1F; --muted: #6E6E73;",
           f"  --accent: {A}; --on-accent: #FFFFFF;", "}",
           "@media (prefers-color-scheme: dark) {", "  :root {",
           f"    --ground: #000000; --card: #1C1C1E; --text: #F5F5F7; --muted: #98989D;",
           f"    --accent: {AD}; --on-accent: #000000;", "  }", "}"]
    other = [k for k in VARIANTS if k != teal][0]
    css.append(f"/* The other variant, {VARIANTS[other]['name']}: --accent: {VARIANTS[other]['light']} (light), "
               f"{VARIANTS[other]['dark']} (dark). */")
    write("palette/tokens.css", "\n".join(css) + "\n")
    return made


def contrast_table(light, dark):
    rows = [
        ("White text on the accent (light mode)", "#FFFFFF", light, 4.5),
        ("Accent on white (links, icons; light mode)", light, "#FFFFFF", 4.5),
        ("Accent on Ground #F5F5F7", light, "#F5F5F7", 4.5),
        ("Dark-mode accent on black", dark, "#000000", 4.5),
        ("Dark-mode accent on Card #1C1C1E", dark, "#1C1C1E", 4.5),
        ("Black text on the dark-mode accent", "#000000", dark, 4.5),
        ("White text on the dark-mode accent (avoid)", "#FFFFFF", dark, 4.5),
        ("Light accent on black (avoid: use the dark-mode accent)", light, "#000000", 4.5),
        ("Dark-mode accent on white (avoid)", dark, "#FFFFFF", 4.5),
    ]
    out = []
    for label, fg, bg, need in rows:
        c = contrast(fg, bg)
        out.append(dict(pair=label, fg=fg, bg=bg, ratio=round(c, 2), aa_text=c >= 4.5, aa_large_or_ui=c >= 3))
    return out


if __name__ == "__main__":
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--teal", choices=sorted(VARIANTS), default=RECOMMENDED)
    a = ap.parse_args()
    files = build(a.teal)
    print(f"built the kit in {VARIANTS[a.teal]['name']}: {len(files)} SVG files plus PNG/ICO renders")
