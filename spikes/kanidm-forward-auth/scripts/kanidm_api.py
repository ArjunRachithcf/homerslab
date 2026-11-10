"""Minimal Kanidm 1.12 REST client for the HomersLab spike (stdlib only).

Names under .hlspike.test resolve to 127.0.0.1 here (no /etc/hosts); TLS is verified against
Kanidm's generated CA. Secrets are read from and written to ../secrets only, never printed.
"""

import base64
import hashlib
import hmac
import json
import os
import socket
import ssl
import struct
import time
import urllib.error
import urllib.request

SPIKE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SECRETS = os.path.join(SPIKE, "secrets")
CA = os.path.join(SPIKE, "kanidm-ca.pem")
IDM = "https://idm.hlspike.test:28743"

_real_getaddrinfo = socket.getaddrinfo


def _getaddrinfo(host, *args, **kwargs):
    if isinstance(host, str) and host.endswith(".hlspike.test"):
        host = "127.0.0.1"
    return _real_getaddrinfo(host, *args, **kwargs)


socket.getaddrinfo = _getaddrinfo

CTX = ssl.create_default_context(cafile=CA)


class ApiError(Exception):
    def __init__(self, status, body):
        super().__init__(f"HTTP {status}: {body}")
        self.status = status
        self.body = body


def request(method, path, token=None, body=None, headers=None, base=IDM, form=False, basic=None):
    url = path if path.startswith("https://") else base + path
    data = None
    hdrs = {"Accept": "application/json"}
    if body is not None:
        if form:
            data = urllib.parse.urlencode(body).encode()
            hdrs["Content-Type"] = "application/x-www-form-urlencoded"
        else:
            data = json.dumps(body).encode()
            hdrs["Content-Type"] = "application/json"
    if token:
        hdrs["Authorization"] = f"Bearer {token}"
    if basic:
        hdrs["Authorization"] = "Basic " + base64.b64encode(f"{basic[0]}:{basic[1]}".encode()).decode()
    if headers:
        hdrs.update(headers)
    req = urllib.request.Request(url, data=data, method=method, headers=hdrs)
    try:
        with urllib.request.urlopen(req, context=CTX, timeout=30) as resp:
            raw = resp.read()
            out = json.loads(raw) if raw and raw.strip() else None
            return resp.status, out, dict(resp.headers)
    except urllib.error.HTTPError as e:
        raw = e.read().decode(errors="replace")
        raise ApiError(e.code, raw[:500]) from None


import urllib.parse  # noqa: E402  (after the monkeypatch on purpose)


def totp(secret: bytes, algo="sha256", step=30, digits=6, at=None):
    counter = int((at if at is not None else time.time()) // step)
    mac = hmac.new(secret, struct.pack(">Q", counter), getattr(hashlib, algo)).digest()
    off = mac[-1] & 0x0F
    code = (struct.unpack(">I", mac[off:off + 4])[0] & 0x7FFFFFFF) % (10 ** digits)
    return code


def auth(username, password=None, totp_secret=None, totp_algo="sha256", privileged=True):
    """Authenticate through /v1/auth and return the bearer token (UAT)."""
    st, r, h = request("POST", "/v1/auth", body={"step": {"init2": {
        "username": username, "issue": "token", "privileged": privileged}}})
    sid = h.get("X-KANIDM-AUTH-SESSION-ID") or h.get("x-kanidm-auth-session-id")
    mechs = r["state"]["choose"]
    mech = "passwordmfa" if "passwordmfa" in mechs else "password"
    sess = {"X-KANIDM-AUTH-SESSION-ID": sid}
    st, r, _ = request("POST", "/v1/auth", body={"step": {"begin": mech}}, headers=sess)
    for _ in range(4):
        state = r["state"]
        if "success" in state:
            return state["success"]
        if "denied" in state:
            raise RuntimeError(f"auth denied for {username}: {state['denied']}")
        allowed = state["continue"]
        if "totp" in allowed:
            cred = {"totp": totp(totp_secret, totp_algo)}
        elif "password" in allowed:
            cred = {"password": password}
        else:
            raise RuntimeError(f"unexpected auth state {allowed}")
        st, r, _ = request("POST", "/v1/auth", body={"step": {"cred": cred}}, headers=sess)
    raise RuntimeError("auth did not finish")


def save_secret(name, value, mode=0o600, subdir=None):
    d = os.path.join(SECRETS, subdir) if subdir else SECRETS
    os.makedirs(d, exist_ok=True)
    p = os.path.join(d, name)
    fd = os.open(p, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, mode)
    with os.fdopen(fd, "w") as f:
        f.write(value)
    os.chmod(p, mode)
    return p


def read_secret(name):
    with open(os.path.join(SECRETS, name)) as f:
        return f.read().strip()


def manager_token():
    return read_secret("homerslab.token")
