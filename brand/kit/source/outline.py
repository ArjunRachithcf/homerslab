#!/usr/bin/env python3
"""Outline the wordmark (and the social card's tagline) from Figtree into outlines.json.

Needs: fontTools, skia-pathops, HarfBuzz's hb-shape. Run once; build.py reads outlines.json and needs none of them.
    python3 outline.py            # writes outlines.json next to this file

Wordmark: "HomersLab", Figtree SemiBold (wght 600), cap height 88 units (the symbol is 192), tracking -10/1000 em,
H-o tightened by 20 and s-L by 15 (/1000 em), every convex corner rounded at a quarter of the stem width (as round as the
symbol's stem corners look). The reversed wordmark (light on dark) is wght 580, about 3 % thinner.
"""
import json, os, subprocess
from fontTools.ttLib import TTFont
from fontTools.pens.recordingPen import DecomposingRecordingPen
from fontTools.pens.transformPen import TransformPen
from fontTools.varLib.instancer import instantiateVariableFont
import pathops

HERE = os.path.dirname(os.path.abspath(__file__))
FONT = os.path.join(HERE, "fonts", "Figtree-VF.ttf")
CAP = 88.0
TRACK = -10
PAIRS = {"Ho": -20, "sL": -15}


def fmt(v):
    s = f"{v:.2f}".rstrip("0").rstrip(".")
    return "0" if s in ("-0", "") else s


def to_d(p, snap=True):
    """SVG path data; with snap, straight edges within 3 degrees of vertical/horizontal are made exact."""
    import math
    p = pathops.Path(p)
    p.convertConicsToQuads()
    out, cur = [], None
    for verb, pts in p.segments:
        if verb == "moveTo":
            cur = pts[0]
            out.append("M" + " ".join(fmt(c) for c in cur))
        elif verb == "lineTo":
            x, y = pts[0]
            if snap and cur is not None:
                dx, dy = x - cur[0], y - cur[1]
                if math.hypot(dx, dy) >= 5:
                    ang = math.degrees(math.atan2(abs(dy), abs(dx)))
                    if 0 < 90 - ang <= 3:
                        x = cur[0]
                    elif 0 < ang <= 3:
                        y = cur[1]
            cur = (x, y)
            out.append("L" + fmt(x) + " " + fmt(y))
        elif verb == "qCurveTo":
            pts = list(pts)
            offs, end = pts[:-1], pts[-1]
            for k in range(len(offs)):
                on = end if k == len(offs) - 1 else ((offs[k][0] + offs[k + 1][0]) / 2, (offs[k][1] + offs[k + 1][1]) / 2)
                out.append("Q" + " ".join(fmt(c) for c in (*offs[k], *on)))
            cur = end
        elif verb == "curveTo":
            out.append("C" + " ".join(fmt(c) for pt in pts for c in pt))
            cur = pts[-1]
        elif verb == "closePath":
            out.append("Z")
    return "".join(out)


def shape(text, wght, cap, track=0.0, pairs=None):
    """Glyph outlines placed by HarfBuzz (kerning on), baseline y=0, x from 0, y down."""
    tt = instantiateVariableFont(TTFont(FONT), {"wght": wght})
    upem, capu = tt["head"].unitsPerEm, tt["OS/2"].sCapHeight
    s = cap / capu
    gl = json.loads(subprocess.run(["hb-shape", FONT, text, "--output-format=json", "--no-glyph-names",
                                    f"--variations=wght={wght}"], capture_output=True, text=True, check=True).stdout)
    order, gs = tt.getGlyphOrder(), tt.getGlyphSet()
    x, union = 0.0, pathops.Path()
    pairs = pairs or {}
    for k, g in enumerate(gl):
        rp = DecomposingRecordingPen(gs)
        gs[order[g["g"]]].draw(TransformPen(rp, (s, 0, 0, -s, (x + g.get("dx", 0)) * s, -g.get("dy", 0) * s)))
        p = pathops.Path(); rp.replay(p.getPen())
        union = pathops.op(union, p, pathops.PathOp.UNION, fix_winding=True)
        x += g["ax"]
        if k < len(gl) - 1:
            x += track / 1000 * upem + pairs.get(text[k:k + 2], 0) / 1000 * upem
    return union, x * s, s, tt


def stem_width(wght, cap):
    p, _, _, _ = shape("H", wght, cap)
    y = -cap * 0.2
    band = pathops.Path(); band.moveTo(-10, y); band.lineTo(1000, y); band.lineTo(1000, y + 0.5); band.lineTo(-10, y + 0.5); band.close()
    segs = sorted(c.bounds for c in pathops.op(p, band, pathops.PathOp.INTERSECTION).contours)
    return segs[0][2] - segs[0][0]


def round_convex(p, r):
    """Opening with a disk of radius r: convex corners become radius-r arcs, concave corners stay sharp."""
    p = pathops.Path(p); p.simplify(fix_winding=True)
    band = pathops.Path(p); band.stroke(2 * r, pathops.LineCap.BUTT_CAP, pathops.LineJoin.ROUND_JOIN, 4)
    band.convertConicsToQuads()
    eroded = pathops.op(p, band, pathops.PathOp.DIFFERENCE, fix_winding=True)
    band2 = pathops.Path(eroded); band2.stroke(2 * r, pathops.LineCap.ROUND_CAP, pathops.LineJoin.ROUND_JOIN, 4)
    band2.convertConicsToQuads()
    out = pathops.op(eroded, band2, pathops.PathOp.UNION, fix_winding=True)
    out.simplify(fix_winding=True)
    return out


def wordmark(wght):
    p, adv, s, tt = shape("HomersLab", wght, CAP, TRACK, PAIRS)
    stem = stem_width(wght, CAP)
    p = round_convex(p, stem / 4)
    x0, y0, x1, y1 = p.bounds
    return dict(d=to_d(p), wght=wght, cap=CAP, stem=round(stem, 3), radius=round(stem / 4, 3),
                bbox=[round(v, 3) for v in (x0, y0, x1, y1)], xheight=round(tt["OS/2"].sxHeight * s, 3))


def tagline(text, wght, cap):
    p, adv, s, tt = shape(text, wght, cap, 0)
    x0, y0, x1, y1 = p.bounds
    return dict(d=to_d(p), wght=wght, cap=cap, text=text, bbox=[round(v, 3) for v in (x0, y0, x1, y1)])


if __name__ == "__main__":
    data = dict(font="Figtree (SIL OFL 1.1), github.com/erikdkennedy/figtree", track=TRACK, pairs=PAIRS,
                wordmark=wordmark(600), wordmark_reversed=wordmark(580),
                tagline=tagline("An app manager for your household’s own machines", 500, 100))
    with open(os.path.join(HERE, "outlines.json"), "w") as fh:
        json.dump(data, fh, indent=1)
    print("wrote outlines.json:", {k: (v["bbox"], v.get("stem"), v.get("radius")) for k, v in data.items() if isinstance(v, dict) and "bbox" in v})
