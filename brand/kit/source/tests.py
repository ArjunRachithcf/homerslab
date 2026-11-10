#!/usr/bin/env python3
"""Run the logo-design skill's checks and the kit's own tests; write ../tests.

    python3 tests.py            # after build.py
Needs: rsvg-convert, ImageMagick (magick), Google Chrome or Chromium (screenshots), and the logo-design skill's
scripts (LOGO_SKILL, default ~/.claude/skills/logo-design). Third-party logos never go into ../tests: the shelf test
(the mark among other self-hosting logos) is written to ../../concepts/tests/ when its references are given:
    python3 tests.py --shelf-refs DIR_WITH_SVGS
"""
import argparse, base64, glob, html, json, os, re, shutil, subprocess, sys, tempfile, time

sys.dont_write_bytecode = True
HERE = os.path.dirname(os.path.abspath(__file__))
KIT = os.path.dirname(HERE)
TESTS = os.path.join(KIT, "tests")
sys.path.insert(0, HERE)
import build as B   # noqa: E402
import harbour as H  # noqa: E402

SKILL = os.path.expanduser(os.environ.get("LOGO_SKILL", "~/.claude/skills/logo-design")) + "/scripts"
CHROME = next((c for c in ("google-chrome", "google-chrome-stable", "chromium", "chromium-browser") if shutil.which(c)), None)
SYSFONT = '-apple-system, BlinkMacSystemFont, "SF Pro Text", "Inter", system-ui, sans-serif'


def run(cmd, **kw):
    return subprocess.run(cmd, check=True, capture_output=True, text=True, **kw).stdout


def shoot(html_file, png, w, h, dpr=1, trim=None):
    """Screenshot a page with headless Chrome at a device pixel ratio."""
    with tempfile.TemporaryDirectory() as tmp:
        shot = os.path.join(tmp, "shot.png")
        proc = subprocess.Popen([CHROME, "--headless=new", "--disable-gpu", "--hide-scrollbars", "--no-first-run",
                                 "--no-default-browser-check", "--disable-extensions", "--password-store=basic",
                                 "--use-mock-keychain", f"--force-device-scale-factor={dpr}", f"--window-size={w},{h}",
                                 f"--screenshot={shot}", f"--user-data-dir={tmp}/profile", "file://" + os.path.abspath(html_file)],
                                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        deadline, last = time.time() + 60, -1
        while time.time() < deadline:
            if os.path.exists(shot):
                size = os.path.getsize(shot)
                if size and size == last:
                    break
                last = size
            if proc.poll() is not None and os.path.exists(shot):
                break
            time.sleep(0.3)
        try:
            proc.wait(timeout=1)
        except subprocess.TimeoutExpired:
            proc.kill()
        shutil.move(shot, png)
    if trim:
        subprocess.run(["magick", png, "-background", trim, "-gravity", "south", "-splice", "0x1", "-trim", "+repage",
                        "-bordercolor", trim, "-border", "0x24", png], check=True)


def uri(svg_text):
    return "data:image/svg+xml;base64," + base64.b64encode(svg_text.encode()).decode()


def png_uri(path):
    return "data:image/png;base64," + base64.b64encode(open(path, "rb").read()).decode()


def page(title, body, extra_css=""):
    return (f"<!doctype html><html lang='en'><head><meta charset='utf-8'><title>{html.escape(title)}</title>"
            f"<style>*{{box-sizing:border-box}}body{{margin:0;padding:28px 32px;background:#F5F5F7;color:#1D1D1F;"
            f"font:13px/1.45 {SYSFONT}}}h1{{font-size:20px;margin:0 0 4px}}h2{{font-size:15px;margin:26px 0 8px}}"
            f"p.note{{color:#6E6E73;margin:0 0 14px;max-width:980px}}.cap{{font-size:11px;color:#6E6E73}}{extra_css}</style>"
            f"</head><body>{body}</body></html>")


# --- 1. the skill's audit ----------------------------------------------------------------------------------------
def audit():
    groups = [
        ("Masters and one-colour files (on white)", "#FFFFFF",
         ["mark/homerslab-mark.svg", "mark/homerslab-mark-16.svg", "mark/homerslab-mark-32.svg",
          "lockups/homerslab-horizontal-black.svg", "lockups/homerslab-stacked-black.svg"]),
        ("Colour files for light backgrounds (on Ground #F5F5F7)", "#F5F5F7",
         ["mark/homerslab-mark-teal.svg", "mark/homerslab-mark-16-teal.svg", "mark/homerslab-mark-32-teal.svg",
          "lockups/homerslab-horizontal-light.svg", "lockups/homerslab-stacked-light.svg", "icons/favicon.svg"]),
        ("Files for dark backgrounds (on black)", "#000000",
         ["mark/homerslab-mark-teal-dark.svg", "mark/homerslab-mark-white.svg", "mark/homerslab-mark-16-teal-dark.svg",
          "mark/homerslab-mark-32-teal-dark.svg", "lockups/homerslab-horizontal-dark.svg",
          "lockups/homerslab-stacked-dark.svg", "lockups/homerslab-horizontal-white.svg", "lockups/homerslab-stacked-white.svg"]),
        ("Icons with their own tile", None, ["icons/app-icon-light.svg", "icons/app-icon-dark.svg", "icons/social-preview.svg"]),
    ]
    out = [f"# svg_audit.py (logo-design skill) on the final kit, {time.strftime('%Y-%m-%d')}",
           "# Expected findings: app-icon-light (a white mark on its own tile, deliberate); social-preview (an outlined",
           "# tagline, so many anchors and small glyph parts: it is a card, not a logo); the wordmark's anchor count (INFO).", ""]
    for title, bg, files in groups:
        cmd = [sys.executable, os.path.join(SKILL, "svg_audit.py")] + [os.path.join(KIT, f) for f in files]
        if bg:
            cmd += ["--bg", bg]
        res = subprocess.run(cmd, capture_output=True, text=True)
        text = res.stdout.replace(KIT + "/", "")
        out += [f"## {title}" + (f" (--bg {bg})" if bg else ""), "", text.strip(), ""]
    with open(os.path.join(TESTS, "audit.txt"), "w") as fh:
        fh.write("\n".join(out) + "\n")
    return "\n".join(out)


# --- 2. the skill's test sheet -----------------------------------------------------------------------------------
def preview(accent):
    files = ["mark/homerslab-mark.svg", "mark/homerslab-mark-teal.svg", "mark/homerslab-mark-32-teal.svg",
             "mark/homerslab-mark-16-teal.svg", "lockups/homerslab-horizontal-light.svg", "lockups/homerslab-stacked-light.svg"]
    names = ["Mark (master, black)", "Mark (teal)", "32 px cut", "16 px cut", "Horizontal lockup", "Stacked lockup"]
    out = os.path.join(TESTS, "preview.html")
    subprocess.run([sys.executable, os.path.join(SKILL, "preview_sheet.py"), *[os.path.join(KIT, f) for f in files],
                    "--names", *names, "--name", B.BRAND, "--brand-color", accent, "-o", out], check=True, capture_output=True)
    shoot(out, os.path.join(TESTS, "preview.png"), 1440, 9000, 1, trim="#fafaf8")


# --- 3. favicons on browser tabs ---------------------------------------------------------------------------------
TABS = [  # (label, strip, active tab, text)  approximate 2025 browser colours
    ("Chrome light", "#DEE1E6", "#FFFFFF", "#1F1F1F"), ("Firefox light", "#F0F0F4", "#FFFFFF", "#15141A"),
    ("Safari light", "#E3E3E3", "#F7F7F7", "#262626"), ("Chrome dark", "#202124", "#35363A", "#E8EAED"),
    ("Firefox dark", "#1C1B22", "#42414D", "#FBFBFE"), ("Safari dark", "#232323", "#3B3B3B", "#E5E5E5"),
]


def favicon_svgs(v):
    d16 = B.mark_d(H.CUT16)
    light = B.svg_doc((0, 0, 16, 16), B.path(d16, v["light"]), "f")
    dark = B.svg_doc((0, 0, 16, 16), B.path(d16, v["dark"]), "f")
    # the common alternative: the master shrunk onto a teal tile
    tile = B.svg_doc((0, 0, 16, 16), f'<path fill="{v["light"]}" d="{B.rounded_square(0, 0, 16, 3.6)}"/>'
                     + B.path(B.mark_d(H.REVERSED, 10.5 / 192, 2.75, 2.75), "#FFFFFF"), "f")
    return light, dark, tile


def favicon_pngs(v, tmp):
    out = {}
    for n, P, vb in ((16, H.CUT16, 16), (32, H.CUT32, 32)):
        p = os.path.join(tmp, f"{v['key']}-{n}.png")
        B.render(B.svg_doc((0, 0, vb, vb), B.path(B.mark_d(P), v["light"]), "f"), p, n)
        out[n] = png_uri(p)
    return out


def favicon_tabs():
    css = (".strip{display:flex;gap:2px;align-items:flex-end;padding:8px 10px 0;border-radius:10px 10px 0 0;width:max-content}"
           ".tab{display:flex;align-items:center;gap:8px;width:172px;height:34px;padding:0 12px;border-radius:9px 9px 0 0;"
           "font-size:12px;white-space:nowrap;overflow:hidden}.tab img{width:16px;height:16px;flex:none}.tab span:nth-child(2){overflow:hidden;text-overflow:ellipsis}.tab .x{margin-left:auto;opacity:.6}"
           ".row{display:flex;gap:18px;flex-wrap:wrap;margin:6px 0 4px}.lab{font-size:11px;color:#6E6E73;margin:2px 0 10px}"
           ".grid{display:grid;grid-template-columns:repeat(3,max-content);gap:6px 18px}")
    body = ["<h1>Favicon on browser tabs</h1><p class='note'>The 16 px favicon at its real size (16 CSS px) on approximate "
            "light and dark tab colours of Chrome, Firefox and Safari. Each strip: the active tab, then a background tab. "
            "<b>SVG</b> = icons/favicon.svg (the 16 px cut; the dark-mode teal when the browser is dark). <b>ICO/PNG</b> = "
            "the fallback for browsers without SVG favicons (light-mode teal everywhere). <b>Tile</b> = the usual "
            "alternative, the mark shrunk onto a teal tile, for comparison only. Screenshots at 1x and 2x.</p>"]
    tmp = tempfile.mkdtemp()
    for key in ("classic", "blue"):
        v = B.VARIANTS[key]
        light, dark, tile = favicon_svgs(v)
        pngs = favicon_pngs(v, tmp)
        rec = " (recommended)" if key == B.RECOMMENDED else ""
        body.append(f"<h2>{v['name']}{rec}: light {v['light']}, dark {v['dark']}</h2><div class='grid'>")
        for label, strip, active, text in TABS:
            is_dark = "dark" in label
            cells = []
            for kind in ("SVG", "ICO/PNG", "Tile"):
                if kind == "SVG":
                    img = f"<img src='{uri(dark if is_dark else light)}' alt=''>"
                elif kind == "ICO/PNG":
                    img = f"<img src='{pngs[16]}' srcset='{pngs[16]} 1x, {pngs[32]} 2x' alt=''>"
                else:
                    img = f"<img src='{uri(tile)}' alt=''>"
                cells.append(
                    f"<div><div class='strip' style='background:{strip}'>"
                    f"<div class='tab' style='background:{active};color:{text}'>{img}<span>{B.BRAND}</span><span class='x'>✕</span></div>"
                    f"<div class='tab' style='background:{strip};color:{text}'>{img}<span>{B.BRAND} — Apps</span>"
                    f"<span class='x'>✕</span></div></div><div class='lab'>{label} · {kind}</div></div>")
            body.append("".join(cells))
        body.append("</div>")
    page_file = os.path.join(TESTS, "favicon-tabs.html")
    with open(page_file, "w") as fh:
        fh.write(page("Favicon on browser tabs", "".join(body), css))
    shoot(page_file, os.path.join(TESTS, "favicon-tabs-1x.png"), 1280, 1500, 1, trim="#F5F5F7")
    shoot(page_file, os.path.join(TESTS, "favicon-tabs-2x.png"), 1280, 1500, 2, trim="#F5F5F7")
    shutil.rmtree(tmp)


# --- 4. app icons ------------------------------------------------------------------------------------------------
def app_icons():
    css = (".dock{display:flex;gap:22px;align-items:flex-end;padding:16px 22px;border-radius:24px;width:max-content;margin:6px 0}"
           ".dock img{display:block}.set{display:flex;gap:28px;flex-wrap:wrap}.lab{font-size:11px;color:#6E6E73;margin:4px 0 14px}"
           ".phone{display:grid;grid-template-columns:repeat(4,60px);gap:18px 16px;padding:22px;border-radius:30px;"
           "width:max-content}.phone img{width:60px;height:60px;border-radius:13.4px}.phone span{display:block;font-size:10px;"
           "text-align:center;margin-top:4px}.m{display:flex;gap:16px}.m img{width:96px;height:96px}")
    body = ["<h1>App icons</h1><p class='note'>The desktop family app's icon (macOS grid: an 824 px tile in a 1024 canvas) "
            "light and dark, on a light and a dark dock at 128, 64 and 32 px, scaled by the browser as an OS would. "
            "Below: the PWA icons as phones show them (iOS masks apple-touch-icon; Android masks maskable-512 to its "
            "launcher shape, here a circle and a squircle), in both teals.</p>"]
    tmp = tempfile.mkdtemp()
    for key in ("classic", "blue"):
        v = B.VARIANTS[key]
        rec = " (recommended)" if key == B.RECOMMENDED else ""
        body.append(f"<h2>{v['name']}{rec}</h2><div class='set'>")
        icons = {}
        for look in ("light", "dark"):
            tilec = v["light"] if look == "light" else "#1C1C1E"
            mk = "#FFFFFF" if look == "light" else v["dark"]
            m = 824 * 0.5
            k = m / 192
            icons[look] = B.svg_doc((0, 0, 1024, 1024), f'<path fill="{tilec}" d="{B.rounded_square(100, 100, 824, 185.4)}"/>'
                                    + B.path(B.mark_d(H.REVERSED, k, 512 - m / 2, 512 - m / 2), mk), "a")
        for dock, label in (("rgba(246,246,248,.85);border:1px solid #D8D8DC", "light dock"), ("#2C2C2E", "dark dock")):
            imgs = "".join(f"<img src='{uri(icons[look])}' width='{s}' height='{s}' alt=''>"
                           for look in ("light", "dark") for s in (128, 64, 32))
            body.append(f"<div><div class='dock' style='background:{dock}'>{imgs}</div><div class='lab'>{label}: light icon "
                        f"128/64/32, dark icon 128/64/32</div></div>")
        # phones
        tile = B.svg_doc((0, 0, 180, 180), f'<rect width="180" height="180" fill="{v["light"]}"/>'
                         + B.path(B.mark_d(H.REVERSED, 90 / 192, 45, 45), "#FFFFFF"), "t")
        mask = B.svg_doc((0, 0, 512, 512), f'<rect width="512" height="512" fill="{v["light"]}"/>'
                         + B.path(B.mark_d(H.REVERSED, 256 / 192, 128, 128), "#FFFFFF"), "m")
        squares = [uri(B.svg_doc((0, 0, 60, 60), '<rect width="60" height="60" fill="%s"/>' % c, "x"))
                   for c in ("#FF9F0A", "#0A84FF", "#30D158", "#BF5AF2", "#FF375F", "#64D2FF", "#8E8E93")]
        filler = "".join(f"<div><img src='{s}' alt=''><span>App</span></div>" for s in squares)
        for bg, txt in (("linear-gradient(160deg,#cfd8e3,#eef1f5)", "#1D1D1F"), ("linear-gradient(160deg,#1c2430,#05070a)", "#F5F5F7")):
            body.append(f"<div><div class='phone' style='background:{bg};color:{txt}'>"
                        f"<div><img src='{uri(tile)}' alt=''><span>{B.BRAND}</span></div>{filler}</div>"
                        f"<div class='lab'>apple-touch-icon on a home screen</div></div>")
        body.append(f"<div><div class='m'><img src='{uri(mask)}' style='border-radius:50%' alt=''>"
                    f"<img src='{uri(mask)}' style='border-radius:30%' alt=''><img src='{uri(mask)}' alt=''></div>"
                    f"<div class='lab'>maskable-512: circle, squircle, unmasked (the mark stays in the 80 % safe zone)</div></div>")
        body.append("</div>")
    page_file = os.path.join(TESTS, "app-icons.html")
    with open(page_file, "w") as fh:
        fh.write(page("App icons", "".join(body), css))
    shoot(page_file, os.path.join(TESTS, "app-icons.png"), 1400, 2400, 1, trim="#F5F5F7")
    shutil.rmtree(tmp)


# --- 5. small sizes and irradiation (rendered by librsvg, enlarged without smoothing) ------------------------------
def small_sizes(accent, accent_dark):
    tmp = tempfile.mkdtemp()
    rows = []
    for bgc, fill, P_master, label in (("#FFFFFF", accent, H.MASTER, "light"), ("#000000", accent_dark, H.MASTER, "dark")):
        tiles = []
        for n, P, vb, note in ((16, H.CUT16, 16, "16 px cut"), (24, P_master, 192, "master"), (32, H.CUT32, 32, "32 px cut"),
                               (48, P_master, 192, "master"), (32, P_master, 192, "master (no cut)"),
                               (16, P_master, 192, "master (no cut)")):
            svg = B.svg_doc((0, 0, vb, vb), B.path(B.mark_d(P), fill), "s")
            p = os.path.join(tmp, f"{label}-{n}-{len(tiles)}.png")
            B.render(svg, p, n, n, bg=bgc)
            k = 192 // n
            subprocess.run(["magick", p, "-filter", "point", "-resize", f"{k * 100}%", "-background", bgc, "-gravity", "center",
                            "-extent", "200x200", "(", p, "-background", bgc, "-gravity", "center", "-extent", "200x44", ")",
                            "-append", "(", "-size", "200x24", "xc:#F5F5F7", "-fill", "#1D1D1F", "-pointsize", "13",
                            "-gravity", "center", "-annotate", "+0+0", f"{n} px \u00b7 {note}", ")", "-append", p], check=True)
            tiles.append(p)
        r = os.path.join(tmp, f"row-{label}.png")
        subprocess.run(["magick", *tiles, "-background", "#F5F5F7", "-splice", "10x0", "+append", r], check=True)
        rows.append(r)
    subprocess.run(["magick", *rows, "-background", "#F5F5F7", "-splice", "0x10", "-append", "-bordercolor", "#F5F5F7",
                    "-border", "10", os.path.join(TESTS, "small-sizes.png")], check=True)
    # irradiation: the master black on white, the master white on black, the reversed cut white on black
    tiles = []
    for P, fill, bgc, note in ((H.MASTER, "#000000", "#FFFFFF", "master, black on white"),
                               (H.MASTER, "#FFFFFF", "#000000", "master, white on black: looks heavier"),
                               (H.REVERSED, "#FFFFFF", "#000000", "reversed cut (inset 1), white on black")):
        p = os.path.join(tmp, f"irr-{len(tiles)}.png")
        B.render(B.svg_doc((-24, -24, 240, 240), B.path(B.mark_d(P), fill), "i"), p, 300, 300, bg=bgc)
        q = os.path.join(tmp, f"irr-s-{len(tiles)}.png")
        B.render(B.svg_doc((-8, -8, 208, 208), B.path(B.mark_d(P), fill), "i"), q, 52, 52, bg=bgc)
        subprocess.run(["magick", p, "(", q, "-background", bgc, "-gravity", "center", "-extent", "300x80", ")", "-append",
                        "(", "-size", "300x26", "xc:#F5F5F7", "-fill", "#1D1D1F", "-pointsize", "13", "-gravity", "center",
                        "-annotate", "+0+0", note, ")", "-append", p], check=True)
        tiles.append(p)
    subprocess.run(["magick", *tiles, "-background", "#F5F5F7", "-splice", "10x0", "+append", "-bordercolor", "#F5F5F7",
                    "-border", "10", os.path.join(TESTS, "irradiation.png")], check=True)
    shutil.rmtree(tmp)


# --- 6. contrast ---------------------------------------------------------------------------------------------------
def contrast_report():
    lines = [f"# WCAG 2.2 contrast, {time.strftime('%Y-%m-%d')}. AA: 4.5:1 for text, 3:1 for large text and UI/graphics.", ""]
    for key, v in B.VARIANTS.items():
        rec = " (recommended)" if key == B.RECOMMENDED else ""
        lines.append(f"## {v['name']}{rec}: light {v['light']}, dark {v['dark']}")
        for r in B.contrast_table(v["light"], v["dark"]):
            verdict = "AA text" if r["aa_text"] else ("AA large/UI only" if r["aa_large_or_ui"] else "fails")
            lines.append(f"  {r['ratio']:5.2f}:1  {verdict:17s} {r['pair']}  ({r['fg']} on {r['bg']})")
        for label, fg, bg in (("16 px favicon, dark teal on Chrome's dark tab", v["dark"], "#35363A"),
                              ("16 px favicon, dark teal on Firefox's dark tab", v["dark"], "#42414D"),
                              ("16 px favicon, light teal on Chrome's dark tab (ICO/PNG fallback)", v["light"], "#35363A"),
                              ("16 px favicon, light teal on Safari's dark tab (ICO/PNG fallback)", v["light"], "#3B3B3B"),
                              ("16 px favicon, light teal on a light tab", v["light"], "#FFFFFF"),
                              ("16 px favicon, light teal on Chrome's light tab strip", v["light"], "#DEE1E6")):
            c = B.contrast(fg, bg)
            lines.append(f"  {c:5.2f}:1  {'graphic ok (3:1)' if c >= 3 else 'graphic weak (<3:1)':17s} {label}")
        lines.append("")
    lines.append("## Neutrals")
    for fg, bg, label in (("#1D1D1F", "#F5F5F7", "Text on Ground (light)"), ("#6E6E73", "#F5F5F7", "Muted on Ground (light)"),
                          ("#6E6E73", "#FFFFFF", "Muted on Card (light)"), ("#F5F5F7", "#000000", "Text on Ground (dark)"),
                          ("#98989D", "#000000", "Muted on Ground (dark)"), ("#98989D", "#1C1C1E", "Muted on Card (dark)")):
        lines.append(f"  {B.contrast(fg, bg):5.2f}:1  {label} ({fg} on {bg})")
    text = "\n".join(lines) + "\n"
    with open(os.path.join(TESTS, "contrast.txt"), "w") as fh:
        fh.write(text)
    return text


# --- 7. the shelf test (third-party logos: outside the kit) --------------------------------------------------------
def shelf(refs_dir):
    out_dir = os.path.join(os.path.dirname(KIT), "concepts", "tests")
    tmp = tempfile.mkdtemp()
    mine = []
    for key in ("classic", "blue"):
        v = B.VARIANTS[key]
        p = os.path.join(tmp, f"HomersLab-{key}.svg")
        with open(p, "w") as fh:
            fh.write(B.svg_doc((0, 0, 192, 192), B.path(B.mark_d(H.MASTER), v["light"]), B.BRAND))
        mine.append(p)
    refs = sorted(glob.glob(os.path.join(refs_dir, "*.svg")))
    out = os.path.join(out_dir, "shelf-final.html")
    subprocess.run([sys.executable, os.path.join(SKILL, "preview_sheet.py"), *mine, "--names", "HomersLab (classic teal)",
                    "HomersLab (blue teal)", "--refs", *refs, "--compare-only", "--name", B.BRAND, "-o", out],
                   check=True, capture_output=True)
    shoot(out, os.path.join(out_dir, "shelf-final.png"), 1400, 3000, 1, trim="#fafaf8")
    shutil.rmtree(tmp)
    return out


if __name__ == "__main__":
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--teal", choices=sorted(B.VARIANTS), default=B.RECOMMENDED, help="the variant the kit was built in")
    ap.add_argument("--shelf-refs", help="folder of third-party SVG logos for the shelf test (written outside the kit)")
    ap.add_argument("--only", nargs="*", help="audit preview tabs icons small contrast shelf")
    a = ap.parse_args()
    os.makedirs(TESTS, exist_ok=True)
    v = B.VARIANTS[a.teal]
    want = set(a.only or ["audit", "preview", "tabs", "icons", "small", "contrast", "shelf"])
    if "audit" in want:
        audit(); print("tests/audit.txt")
    if "preview" in want:
        preview(v["light"]); print("tests/preview.html, preview.png")
    if "tabs" in want:
        favicon_tabs(); print("tests/favicon-tabs.html, -1x.png, -2x.png")
    if "icons" in want:
        app_icons(); print("tests/app-icons.html, app-icons.png")
    if "small" in want:
        small_sizes(v["light"], v["dark"]); print("tests/small-sizes.png, irradiation.png")
    if "contrast" in want:
        contrast_report(); print("tests/contrast.txt")
    if "shelf" in want and a.shelf_refs:
        print(shelf(a.shelf_refs))
