#!/usr/bin/env python3
"""Fail closed: a release must contain a real, complete, locally bundled Vue build."""
from pathlib import Path
import hashlib
import json
import re
root = Path(__file__).resolve().parents[2] / "internal/server/static"
manifest = json.loads((root / "build-manifest.json").read_text())
if manifest.get("framework") != "Vue 3 + Element Plus" or manifest.get("base") != "/admin/":
    raise SystemExit("A successful Vue Admin production build is required")
assets = manifest.get("assets")
if not isinstance(assets, dict) or not assets or not any(p.endswith(".js") for p in assets):
    raise SystemExit("Missing compiled admin JS")
for name, expected in assets.items():
    if Path(name).name != name:
        raise SystemExit("Invalid manifest path")
    data = (root / "assets" / name).read_bytes()
    if hashlib.sha256(data).hexdigest() != expected:
        raise SystemExit(f"Asset checksum mismatch: {name}")
index = (root / "index.html").read_text()
for name in re.findall(r'(?:src|href)="/admin/assets/([^"?#]+)', index):
    if name not in assets:
        raise SystemExit(f"Unlisted entry asset: {name}")
if re.search(r'<script[^>]+src=["\']https?://', index, re.I):
    raise SystemExit("Runtime CDN scripts are not allowed in the deployable admin")
print("Verified complete bundled Vue Admin assets")
