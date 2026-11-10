#!/usr/bin/env python3
"""The Harbour H, built from numbers. No dependencies.

Construction (master, 192 x 192 units, module m = 8 units, so the H is 24 x 24 modules):
  stems        6 m wide (48), full height; outer corners: continuous-curvature corner, radius 14, 60 % smoothing
               (as round to the eye as round 1's circular radius 12, without the kink where the curve starts)
  deck         the hull's top edge at 13 m (104): the H's crossbar
  hull         2 m deep at the stems (to 120); its underside is one circular arc of radius 10 m (80) centred at
               (12 m, 7 m) = (96, 56), so the keel sits at 17 m (136): a 3-4-5 construction (48, 64, 80)
  sail         luff (mast side) at 10 m (80), foot at 11 m (88), clew at 16 m (128); its leech is one circular
               arc from head to clew, bellied 23 degrees; every sail corner has radius 3; the head sits 1 m below
               the top of the stems
  gaps         sail to deck 2 m, sail to the right stem 2 m: equal, so the sail stays separate down to 24 px
At 24 px and 48 px the master lands on whole pixels (8 and 4 units per pixel); 16 px and 32 px have their own cuts.

`inset` moves every edge inward by that many units: the reversed (light-on-dark) artwork uses inset 1, about 4 % of
a stem, so it does not look heavier than the dark-on-light one (irradiation).
"""
import math


def fmt(v, prec=2):
    s = f"{v:.{prec}f}".rstrip("0").rstrip(".")
    return "0" if s in ("-0", "") else s


class Path:
    """Absolute SVG path builder."""

    def __init__(self, prec=2):
        self.parts, self.prec, self.cur = [], prec, None

    def _p(self, *pts):
        return " ".join(fmt(c, self.prec) for p in pts for c in p)

    def M(self, p):
        self.parts.append("M" + self._p(p)); self.cur = p; return self

    def L(self, p):
        if self.cur and abs(self.cur[0] - p[0]) < 1e-9 and abs(self.cur[1] - p[1]) < 1e-9:
            return self
        if self.cur and abs(self.cur[1] - p[1]) < 1e-9:
            self.parts.append("H" + fmt(p[0], self.prec))
        elif self.cur and abs(self.cur[0] - p[0]) < 1e-9:
            self.parts.append("V" + fmt(p[1], self.prec))
        else:
            self.parts.append("L" + self._p(p))
        self.cur = p; return self

    def C(self, c1, c2, p):
        self.parts.append("C" + self._p(c1, c2, p)); self.cur = p; return self

    def A(self, r, large, sweep, p):
        self.parts.append(f"A{fmt(r, self.prec)} {fmt(r, self.prec)} 0 {large} {sweep} " + self._p(p))
        self.cur = p; return self

    def Z(self):
        self.parts.append("Z"); return self

    def d(self):
        return "".join(self.parts)


def squircle(r, xi):
    """Figma's continuous-curvature corner for 90 degrees (as in figma-squircle)."""
    p = (1 + xi) * r
    arc = 90 * (1 - xi)
    L = math.sin(math.radians(arc / 2)) * r * math.sqrt(2)
    alpha = (90 - arc) / 2
    p34 = r * math.tan(math.radians(alpha / 2))
    beta = 45 * xi
    c = p34 * math.cos(math.radians(beta))
    d = c * math.tan(math.radians(beta))
    b = (p - L - c - d) / 3
    return dict(p=p, L=L, a=2 * b, b=b, c=c, d=d, r=r)


def corner(path, vertex, u, v, r, xi):
    """Convex right-turn corner at `vertex`, arriving along unit vector u, leaving along v.
    The path must stand at vertex - p*u; it ends at vertex + p*v."""
    if r <= 0:
        path.L(vertex)
        return
    q = squircle(r, xi)
    p, a, b, c, d, L = q["p"], q["a"], q["b"], q["c"], q["d"], q["L"]
    S = (vertex[0] - p * u[0], vertex[1] - p * u[1])
    g = lambda x, y: (S[0] + x * u[0] + y * v[0], S[1] + x * u[1] + y * v[1])
    if xi > 0:
        path.C(g(a, 0), g(a + b, 0), g(a + b + c, d))
        path.A(r, 0, 1, g(a + b + c + L, d + L))
        x0, y0 = a + b + c + L, d + L
        path.C(g(x0 + d, y0 + c), g(x0 + d, y0 + b + c), g(x0 + d, y0 + a + b + c))
    else:
        path.A(r, 0, 1, g(r, r))


def corner_extent(r, xi):
    return squircle(r, xi)["p"] if r > 0 else 0.0


def h_contour(P, path):
    i = P.get("inset", 0.0)
    X0, Y0, X1, Y1 = P["x0"] + i, P["y0"] + i, P["x1"] - i, P["y1"] - i
    lin, rin = P["x0"] + P["stem"] - i, P["x1"] - P["stem"] + i      # inner edges of the stems
    deck = P["deck"] + i
    r, xi = max(P["r"] - i, 0.0), P["xi"]
    p = corner_extent(r, xi)
    path.M((X0 + p, Y0))
    path.L((lin - p, Y0)); corner(path, (lin, Y0), (1, 0), (0, 1), r, xi)
    path.L((lin, deck)); path.L((rin, deck)); path.L((rin, Y0 + p))
    corner(path, (rin, Y0), (0, -1), (1, 0), r, xi)
    path.L((X1 - p, Y0)); corner(path, (X1, Y0), (1, 0), (0, 1), r, xi)
    path.L((X1, Y1 - p)); corner(path, (X1, Y1), (0, 1), (-1, 0), r, xi)
    path.L((rin + p, Y1)); corner(path, (rin, Y1), (-1, 0), (0, -1), r, xi)
    info = {}
    if P.get("hull_pts"):            # small cuts: a polygonal hull that lands on whole pixels
        path.L((rin, P["junction"] + i))
        for q in P["hull_pts"]:
            path.L((q[0], q[1] - i))
        path.L((lin, P["junction"] + i))
    else:
        cx = (P["x0"] + P["x1"]) / 2
        half = (P["x1"] - P["x0"]) / 2 - P["stem"]
        jy0, ky0 = P["junction"], P["keel"]
        yc = (ky0 ** 2 - jy0 ** 2 - half ** 2) / (2 * (ky0 - jy0))
        R = (ky0 - yc) - i
        hx = (rin - lin) / 2
        jy = yc + math.sqrt(R * R - hx * hx)
        path.L((rin, jy))
        path.A(R, 0, 1, (lin, jy))
        info = dict(hull_centre=(cx, yc), hull_radius=R + i)
    path.L((lin, Y1 - p)); corner(path, (lin, Y1), (0, 1), (-1, 0), r, xi)
    path.L((X0 + p, Y1)); corner(path, (X0, Y1), (-1, 0), (0, -1), r, xi)
    path.L((X0, Y0 + p)); corner(path, (X0, Y0), (0, -1), (1, 0), r, xi)
    path.Z()
    return info


def leech_circle(P):
    hx, hy, kx, ky = P["luff"], P["head"], P["clew"], P["foot"]
    beta = math.radians(P["belly"])
    ch = math.hypot(kx - hx, ky - hy)
    R = ch / (2 * math.sin(beta))
    mx, my = (hx + kx) / 2, (hy + ky) / 2
    nx, ny = -(ky - hy) / ch, (kx - hx) / ch
    if nx > 0:
        nx, ny = -nx, -ny
    dist = math.sqrt(R ** 2 - (ch / 2) ** 2)
    return mx + nx * dist, my + ny * dist, R


def sail_contour(P, path):
    i = P.get("inset", 0.0)
    cx, cy, R0 = leech_circle(P)
    Lx, F, R = P["luff"] + i, P["foot"] - i, R0 - i
    rh, rt, rc = (max(P[k] - i, 0.2) for k in ("rh", "rt", "rc"))
    # head: fillet tangent to the luff and inside the leech circle
    dx = Lx + rh - cx
    sq = math.sqrt((R - rh) ** 2 - dx ** 2)
    yh = min((cy - sq, cy + sq), key=lambda y: abs(y - P["head"]))
    k = R / (R - rh)
    head_arc = (cx + k * (Lx + rh - cx), cy + k * (yh - cy))
    # clew: fillet tangent to the foot and inside the leech circle
    dy = F - rc - cy
    sq = math.sqrt((R - rc) ** 2 - dy ** 2)
    xc = min((cx - sq, cx + sq), key=lambda x: abs(x - P["clew"]))
    k2 = R / (R - rc)
    clew_arc = (cx + k2 * (xc - cx), cy + k2 * (F - rc - cy))
    path.M((Lx, yh))
    path.A(rh, 0, 1, head_arc)
    path.A(R, 0, 1, clew_arc)
    path.A(rc, 0, 1, (xc, F))
    path.L((Lx + rt, F))
    path.A(rt, 0, 1, (Lx, F - rt))
    path.Z()
    return dict(sail_top=yh - rh, leech_centre=(cx, cy), leech_radius=R0)


def mark_d(P, prec=2):
    path = Path(prec)
    info = h_contour(P, path)
    info.update(sail_contour(P, path))
    return path.d(), info


def solve_head(P, top):
    """Find the sharp head height whose filleted sail top lands at `top`."""
    lo, hi = top - 10, top + 10
    for _ in range(80):
        mid = (lo + hi) / 2
        Q = dict(P, head=mid)
        t = mark_d(Q)[1]["sail_top"]
        lo, hi = (mid, hi) if t < top else (lo, mid)
    return round((lo + hi) / 2, 4)


# --- the drawings ----------------------------------------------------------------------------------------------
MODULE = 8
MASTER = dict(x0=0, y0=0, x1=192, y1=192, stem=48, r=14, xi=0.6,
              deck=104, junction=120, keel=136,
              luff=80, foot=88, clew=128, belly=23, rh=3, rt=3, rc=3, inset=0.0, size=192)
MASTER["head"] = solve_head(MASTER, 8.0)             # sail top 1 module under the cap line
REVERSED = dict(MASTER, inset=1.0)                   # light-on-dark artwork, 1 unit thinner on every edge

# 16 px cut: every straight edge on a whole pixel; the hull flat-keeled so it stays a boat, the sail as big as the
# counter allows (head 0.6 px under the top, 1 px clear of the deck and of the right stem)
CUT16 = dict(x0=0, y0=0, x1=16, y1=16, stem=4, r=1, xi=0.6,
             deck=8, junction=10, keel=11, hull_pts=[(10, 11), (6, 11)],
             luff=6, head=0.6, foot=7, clew=11, belly=23, rh=0.2, rt=0.2, rc=0.2, inset=0.0, size=16)

# 32 px cut: the master's proportions snapped to whole pixels (deck 17, hull to 20, keel 23, sail 14..22 x 1..15)
CUT32 = dict(x0=0, y0=0, x1=32, y1=32, stem=8, r=2, xi=0.6,
             deck=17, junction=20, keel=23,
             luff=14, head=0.6, foot=15, clew=22, belly=23, rh=0.5, rt=0.5, rc=0.5, inset=0.0, size=32)


def construction(P=MASTER):
    """Numbers for the guide's construction drawing."""
    d, info = mark_d(P)
    return dict(module=MODULE, **{k: P[k] for k in ("stem", "deck", "junction", "keel", "luff", "foot", "clew", "belly")},
                head=P["head"], **info)


if __name__ == "__main__":
    import json
    for name, P in (("master", MASTER), ("reversed", REVERSED), ("16", CUT16), ("32", CUT32)):
        d, info = mark_d(P)
        print(name, json.dumps({k: (round(v, 3) if isinstance(v, float) else v) for k, v in info.items()}, default=str))
