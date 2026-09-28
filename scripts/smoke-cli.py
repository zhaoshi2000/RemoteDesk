#!/usr/bin/env python3
"""Run built Linux CLI binaries in an isolated temp directory (stdlib only).
The target is a loopback byte echo service, NOT an actual sshd login test.
No credentials or private state are copied into the output report.
"""
from __future__ import annotations
import hashlib
import json
import os
from pathlib import Path
import socket
import ssl
import subprocess
import tempfile
import threading
import time
import urllib.request

ROOT = Path(__file__).resolve().parent.parent
SERVER = ROOT / "dist/linux-amd64/remote-server"
AGENT = ROOT / "dist/linux-amd64/remote-agent"


def main() -> None:
    for binary in (SERVER, AGENT):
        if not binary.is_file():
            raise RuntimeError(f"Build the Linux binaries first: {binary}")
    results: list[str] = []
    processes: list[subprocess.Popen] = []
    logs = []
    connections: list[socket.socket] = []
    stop = threading.Event()

    def port(kind=socket.SOCK_STREAM) -> int:
        with socket.socket(socket.AF_INET, kind) as s:
            s.bind(("127.0.0.1", 0))
            return s.getsockname()[1]

    def run(*args: object) -> str:
        p = subprocess.run([str(a) for a in args], capture_output=True, text=True, timeout=25)
        if p.returncode:
            # No arguments are printed, to avoid accidentally exposing future credentials.
            raise RuntimeError(f"CLI exited {p.returncode}: {p.stderr[-2000:]}")
        return p.stdout

    with tempfile.TemporaryDirectory(prefix="remotedesk-cli-") as temp:
        root = Path(temp)
        echo = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        echo.bind(("127.0.0.1", 0))
        echo.listen(8)
        echo.settimeout(0.2)
        echo_port = echo.getsockname()[1]

        def echo_connection(c: socket.socket) -> None:
            with c:
                try:
                    while not stop.is_set():
                        chunk = c.recv(65536)
                        if not chunk:
                            break
                        c.sendall(chunk)
                except OSError:
                    pass

        def echo_loop() -> None:
            while not stop.is_set():
                try:
                    c, _ = echo.accept()
                    connections.append(c)
                    threading.Thread(target=echo_connection, args=(c,), daemon=True).start()
                except socket.timeout:
                    continue
                except OSError:
                    break

        threading.Thread(target=echo_loop, daemon=True).start()

        def start(label: str, *args: object) -> subprocess.Popen:
            f = open(root / f"{label}.log", "w", encoding="utf-8")
            logs.append(f)
            p = subprocess.Popen([str(a) for a in args], stdout=f, stderr=f)
            processes.append(p)
            return p

        def wait_until(check, seconds=12):
            deadline = time.monotonic() + seconds
            last = None
            while time.monotonic() < deadline:
                try:
                    value = check()
                    if value:
                        return value
                except (OSError, ValueError) as exc:
                    last = type(exc).__name__
                time.sleep(0.05)
            raise RuntimeError(f"Readiness check timed out ({last})")

        try:
            tcp, udp = port(), port(socket.SOCK_DGRAM)
            server_state = root / "server"
            run(SERVER, "init", "--dir", server_state, "--listen", f"127.0.0.1:{tcp}",
                "--stun", f"127.0.0.1:{udp}", "--hosts", "localhost,127.0.0.1")
            start("server", SERVER, "run", "--config", server_state / "server.json")
            context = ssl.create_default_context(cafile=str(server_state / "server.crt"))
            base = f"https://127.0.0.1:{tcp}"

            def request(path: str, admin=False):
                headers = {}
                if admin:
                    headers["Authorization"] = "Bearer " + (server_state / "admin.token").read_text().strip()
                req = urllib.request.Request(base + path, headers=headers)
                with urllib.request.urlopen(req, context=context, timeout=2) as response:
                    return json.load(response)

            wait_until(lambda: request("/healthz"))
            results.append("PASS compiled server: HTTPS readiness with certificate validation")
            identities = []
            for label in ("a", "b"):
                state = root / label
                run(AGENT, "init", "--state", state, "--server", base,
                    "--ca", server_state / "server.crt", "--name", f"CLI-{label}",
                    "--stun", f"127.0.0.1:{udp}", "--listen-tcp", "127.0.0.1:0",
                    "--listen-udp", "127.0.0.1:0", "--ssh-port", echo_port)
                run(AGENT, "register", "--state", state, "--token-file", server_state / "enrollment.token")
                identities.append(json.loads(run(AGENT, "identity", "--state", state)))
            for left, right in (("a", "b"), ("b", "a")):
                run(AGENT, "trust", "--state", root / left, "--peer-file", root / right / "public-identity.json")
                start(left, AGENT, "run", "--state", root / left)
            wait_until(lambda: request("/v1/admin/overview", True).get("online") == 2)
            results.append("PASS compiled agents: independent identity, register, explicit trust, heartbeat; admin reports 2 online")
            target = identities[1]["id"]
            out = run(AGENT, "probe", "--state", root / "a", "--peer", target)
            if "Both signed UDP directions are reachable" not in out:
                raise RuntimeError("UDP CLI probe did not succeed")
            results.append("PASS compiled probe CLI: signed UDP reachability in both directions on loopback")

            for force_relay in (False, True):
                name = "relay" if force_relay else "direct"
                local_port = port()
                args = [AGENT, "tunnel", "--state", root / "a", "--peer", target,
                        "--listen", f"127.0.0.1:{local_port}"]
                if force_relay:
                    args.append("--relay-only")
                process = start(name, *args)
                wait_until(lambda: "Local SSH tunnel:" in (root / f"{name}.log").read_text())
                data = os.urandom(1024 * 1024)
                send_errors = []
                with socket.create_connection(("127.0.0.1", local_port), timeout=12) as c:
                    c.settimeout(12)
                    def sender():
                        try:
                            c.sendall(data)
                        except OSError as exc:
                            send_errors.append(exc)
                    thread = threading.Thread(target=sender)
                    thread.start()
                    got = bytearray()
                    while len(got) < len(data):
                        chunk = c.recv(min(65536, len(data) - len(got)))
                        if not chunk:
                            break
                        got.extend(chunk)
                    thread.join(12)
                    if thread.is_alive() or send_errors or bytes(got) != data:
                        raise RuntimeError(f"{name} byte transfer mismatch")
                expected = "Relay / end-to-end TLS" if force_relay else "Direct TLS/TCP"
                wait_until(lambda: expected in (root / f"{name}.log").read_text())
                results.append(f"PASS compiled tunnel CLI: {name}, 1 MiB random bytes, exact echo match; path log verified")
                process.terminate()
                process.wait(timeout=8)
            results.append("LIMITATION echo target replaces sshd: Windows user authentication and interactive terminal NOT tested")
            results.append("LIMITATION loopback machine test only: real NAT, public WAN, desktop video and latency NOT tested")
        finally:
            stop.set()
            echo.close()
            for c in connections:
                try:
                    c.close()
                except OSError:
                    pass
            for p in reversed(processes):
                if p.poll() is None:
                    p.terminate()
                    try:
                        p.wait(timeout=6)
                    except subprocess.TimeoutExpired:
                        p.kill()
                        p.wait(timeout=3)
            for f in logs:
                f.close()
    for line in results:
        print(line)
    for binary in (SERVER, AGENT):
        print(f"SHA256 {binary.relative_to(ROOT)} {hashlib.sha256(binary.read_bytes()).hexdigest()}")


if __name__ == "__main__":
    main()
