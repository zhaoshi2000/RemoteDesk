#!/usr/bin/env python3
"""The public release contract contains exactly two files; checksums live inside."""
from pathlib import Path
import sys
import tarfile
root = Path(sys.argv[1] if len(sys.argv) > 1 else "release")
expected = {"RemoteDesk-Server-linux-amd64.tar.gz", "RemoteDeskSetup.exe"}
actual = {p.name for p in root.iterdir() if p.is_file()}
if actual != expected:
    raise SystemExit(f"Release must have exactly two files; found {sorted(actual)}")
exe = root / "RemoteDeskSetup.exe"
with exe.open("rb") as f:
    if f.read(2) != b"MZ" or exe.stat().st_size < 65536:
        raise SystemExit("Client installer is missing or is not a real Windows PE")
with tarfile.open(root / "RemoteDesk-Server-linux-amd64.tar.gz") as tar:
    names = set(tar.getnames())
    for name in ["RemoteDesk-Server/bin/remote-server", "RemoteDesk-Server/install.sh", "RemoteDesk-Server/SHA256SUMS"]:
        if name not in names:
            raise SystemExit(f"Incomplete server package: missing {name}")
    forbidden = [n for n in names if n.endswith((".key", ".token")) or "/node_modules/" in n]
    if forbidden:
        raise SystemExit("Private state or build dependencies leaked into server release")
print("Release contract verified: server archive + Windows installer, and nothing else")
