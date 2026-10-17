#!/usr/bin/env python3
"""Renders the neXal app icon: the splash-screen mark (cloud, peer mesh, lock) on near-black.

  make-app-icon.py --mac  macos/Resources/AppIcon.icns
  make-app-icon.py --ios  ../nexal-ios/Resources/Assets.xcassets/AppIcon.appiconset

The geometry is copied from NexalSplashView.swift (180 x 340 design space); the cloud and lock are
drawn bold and the mesh dim so the mark still reads at 16 px. Needs Pillow.
"""
import argparse
import json
import math
import os
import re
import sys

from PIL import Image, ImageDraw

SS = 4  # supersampling
DESIGN_CENTER = (90.0, 127.0)  # centre of the mark inside the splash design space

CLOUD = [((68, 110), (52, 110), (54, 86), (70, 86)),
         ((70, 86), (74, 70), (96, 68), (100, 82)),
         ((100, 82), (116, 82), (124, 108), (112, 110))]
PEERS = [(30, 150), (60, 180), (120, 180), (150, 150)]
CLOUD_BASE = (90, 112)
LINKS = [((37, 150), (67, 180)), ((67, 180), (113, 180)), ((113, 180), (143, 150)), ((37, 150), (143, 150)),
         ((37, 150), (113, 180)), ((67, 180), (143, 150)), (CLOUD_BASE, (37, 143)), (CLOUD_BASE, (143, 143))]


def bezier(p0, p1, p2, p3, n=48):
    pts = []
    for i in range(n + 1):
        t = i / n
        u = 1 - t
        pts.append((u ** 3 * p0[0] + 3 * u * u * t * p1[0] + 3 * u * t * t * p2[0] + t ** 3 * p3[0],
                    u ** 3 * p0[1] + 3 * u * u * t * p1[1] + 3 * u * t * t * p2[1] + t ** 3 * p3[1]))
    return pts


def background(size, rounded):
    img = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    grad = Image.new("RGBA", (size, size))
    px = grad.load()
    for y in range(size):
        v = round(28 - 28 * (y / (size - 1)))  # #1c1c1c at the top down to black
        for x in range(size):
            px[x, y] = (v, v, v + 2 if v else 0, 255)
    if not rounded:
        return grad
    inset = round(size * 100 / 1024)  # macOS icon grid: 824 of 1024 is the body
    body = size - 2 * inset
    mask = Image.new("L", (size, size), 0)
    ImageDraw.Draw(mask).rounded_rectangle((inset, inset, inset + body - 1, inset + body - 1),
                                           radius=round(body * 0.2237), fill=255)
    img.paste(grad, (0, 0), mask)
    return img


def stroke(layer_draw, pts, width, fill, closed=False):
    if closed:
        pts = pts + [pts[0]]
    layer_draw.line(pts, fill=fill, width=max(1, round(width)), joint="curve")
    r = width / 2
    for x, y in (pts[0], pts[-1]):
        layer_draw.ellipse((x - r, y - r, x + r, y + r), fill=fill)


def draw_mark(canvas, mark_px):
    """Draws the mark so its design-space width of ~132 units spans mark_px, centred on the canvas."""
    size = canvas.size[0]
    k = mark_px / 132.0
    cx, cy = size / 2, size / 2

    def m(p):
        return (cx + (p[0] - DESIGN_CENTER[0]) * k, cy + (p[1] - DESIGN_CENTER[1]) * k)

    def layer():
        im = Image.new("RGBA", canvas.size, (0, 0, 0, 0))
        return im, ImageDraw.Draw(im)

    white = (255, 255, 255)
    # Mesh: links, then peers (black fill hides links under the nodes), dimmed.
    mesh, d = layer()
    for a, b in LINKS:
        stroke(d, [m(a), m(b)], 1.3 * k, white + (255,))
    for x, y in PEERS:
        box = (m((x - 6.5, y - 6.5)), m((x + 6.5, y + 6.5)))
        d.rounded_rectangle((box[0][0], box[0][1], box[1][0], box[1][1]), radius=3.2 * k, fill=(0, 0, 0, 255),
                            outline=white + (255,), width=round(1.9 * k))
        r = 1.8 * k
        px, py = m((x, y))
        d.ellipse((px - r, py - r, px + r, py + r), fill=white + (255,))
    mesh.putalpha(mesh.getchannel("A").point(lambda v: int(v * 0.42)))
    canvas.alpha_composite(mesh)

    # Cloud, bold and bright.
    cloud, d = layer()
    pts = []
    for seg in CLOUD:
        pts += bezier(*[m(p) for p in seg])
    stroke(d, pts, 3.4 * k, white + (255,), closed=True)
    canvas.alpha_composite(cloud)

    # Lock, enlarged, in front of the mesh.
    lock, d = layer()
    s = 1.5
    ox, oy = 90, 143

    def lp(x, y):
        return m((ox + (x - ox) * s, oy + 9 + (y - oy) * s))

    shackle = [lp(83, 134), lp(83, 128)]
    for i in range(0, 41):
        a = math.pi - math.pi * i / 40
        shackle.append(lp(90 + 7 * math.cos(a), 128 - 7 * math.sin(a)))
    shackle += [lp(97, 128), lp(97, 134)]
    stroke(d, shackle, 2.8 * k * s / 1.0, white + (255,))
    a, b = lp(78, 134), lp(102, 152)
    d.rounded_rectangle((a[0], a[1], b[0], b[1]), radius=3 * k * s, fill=(0, 0, 0, 255),
                        outline=white + (255,), width=round(2.6 * k * s))
    r = 2.2 * k * s
    hx, hy = lp(90, 143)
    d.ellipse((hx - r, hy - r, hx + r, hy + r), fill=white + (255,))
    canvas.alpha_composite(lock)


def render(px, rounded):
    big = px * SS
    canvas = background(big, rounded)
    body = (824 / 1024) if rounded else 1.0
    draw_mark(canvas, big * body * 0.74)
    return canvas.resize((px, px), Image.LANCZOS)


def write_icns(path):
    sizes = [16, 32, 64, 128, 256, 512, 1024]
    images = {s: render(s, True) for s in sizes}
    images[1024].save(path, format="ICNS", sizes=[(s, s) for s in sizes],
                      append_images=[images[s] for s in sizes[:-1]])
    print("wrote", path)


def write_ios(folder):
    contents = json.load(open(os.path.join(folder, "Contents.json")))
    for entry in contents["images"]:
        name = entry.get("filename")
        if not name:
            continue
        base, scale = re.match(r"icon-([\d.]+)@(\d)x\.png", name).groups()
        px = round(float(base) * int(scale))
        img = render(px, False).convert("RGB")  # iOS rejects alpha and applies its own corner mask
        img.save(os.path.join(folder, name), optimize=True)
        print("wrote", name, px)


if __name__ == "__main__":
    p = argparse.ArgumentParser()
    p.add_argument("--mac", metavar="OUT.icns")
    p.add_argument("--ios", metavar="APPICON.appiconset")
    p.add_argument("--preview", metavar="OUT.png", help="write a 1024 px preview of the macOS style")
    a = p.parse_args()
    if not (a.mac or a.ios or a.preview):
        p.error("nothing to do")
    if a.preview:
        render(1024, True).save(a.preview)
    if a.mac:
        write_icns(a.mac)
    if a.ios:
        write_ios(a.ios)
