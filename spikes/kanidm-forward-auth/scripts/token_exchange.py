"""Exchange an authorization code at Kanidm's token endpoint and print the ID token's claims as JSON.

Usage: token_exchange.py <client_id> <secret_file> <redirect_uri> <code> <code_verifier>
Prints claims only (no tokens). Exit 2 with the OAuth2 error on failure.
"""

import base64
import json
import os
import sys

sys.path.insert(0, os.path.dirname(__file__))
import kanidm_api as k  # noqa: E402

client_id, secret_file, redirect_uri, code, verifier = sys.argv[1:6]
secret = k.read_secret(secret_file)
try:
    st, tok, _ = k.request("POST", "/oauth2/token", form=True, basic=(client_id, secret), body={
        "grant_type": "authorization_code", "code": code, "redirect_uri": redirect_uri,
        "code_verifier": verifier, "client_id": client_id})
except k.ApiError as e:
    print(json.dumps({"error": e.status, "body": e.body[:300]}))
    sys.exit(2)


def claims(jwt):
    part = jwt.split(".")[1]
    return json.loads(base64.urlsafe_b64decode(part + "=" * (-len(part) % 4)))


idt = claims(tok["id_token"])
hdr = json.loads(base64.urlsafe_b64decode(tok["id_token"].split(".")[0] + "=="))
out = {k_: idt.get(k_) for k_ in ("iss", "aud", "sub", "preferred_username", "groups", "auth_time", "iat",
                                    "exp", "nonce", "amr", "acr", "email")}
out["id_token_alg"] = hdr.get("alg")
out["token_type"] = tok.get("token_type")
out["expires_in"] = tok.get("expires_in")
out["has_refresh_token"] = "refresh_token" in tok
out["access_token_format"] = "JWE (5 parts)" if tok["access_token"].count(".") == 4 else (
    "JWS (3 parts)" if tok["access_token"].count(".") == 2 else "opaque")
print(json.dumps(out))
