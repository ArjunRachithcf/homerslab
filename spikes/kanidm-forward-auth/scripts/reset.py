"""Credential reset for one person, by API: a new intent (what a reset link carries), the old TOTP
removed, a new password and TOTP enrolled, committed. Then the old credential must fail and the new
one must work. Usage: reset.py <person>
"""

import base64
import json
import os
import shutil
import sys

sys.path.insert(0, os.path.dirname(__file__))
import kanidm_api as k  # noqa: E402

person = sys.argv[1]
T = k.manager_token()
old_path = os.path.join(k.SECRETS, f"{person}.json")
old = json.load(open(old_path))
shutil.copy(old_path, os.path.join(k.SECRETS, f"{person}.old.json"))

st, intent, _ = k.request("GET", f"/v1/person/{person}/_credential/_update_intent/600", token=T)
st, (session, status), _ = k.request("POST", "/v1/credential/_exchange_intent", body=intent["token"])
print("reset session opened; current primary:", json.dumps(status["primary"]["type_"]))
label = status["primary"]["type_"]["PasswordMfa"][0][0]
st, status, _ = k.request("POST", "/v1/credential/_update", body=[{"totpremove": label}, session])
print(f"old TOTP '{label}' removed; warnings={status['warnings']} can_commit={status['can_commit']}")
password = base64.urlsafe_b64encode(os.urandom(24)).decode()
st, status, _ = k.request("POST", "/v1/credential/_update", body=[{"password": password}, session])
st, status, _ = k.request("POST", "/v1/credential/_update", body=["totpgenerate", session])
sec = status["mfaregstate"]["TotpCheck"]
secret = bytes(sec["secret"])
code = k.totp(secret, sec["algo"], sec["step"], sec["digits"])
st, status, _ = k.request("POST", "/v1/credential/_update", body=[{"totpverify": [code, "spike-reset"]}, session])
print("new password + TOTP:", json.dumps(status["primary"]["type_"]), "can_commit:", status["can_commit"])
k.request("POST", "/v1/credential/_commit", body=session)
print("committed")
k.save_secret(f"{person}.json", json.dumps({
    "password": password, "totp_secret_b32": base64.b32encode(secret).decode(),
    "algo": sec["algo"], "step": sec["step"], "digits": sec["digits"]}))

try:
    k.auth(person, old["password"], base64.b32decode(old["totp_secret_b32"]), old["algo"], privileged=False)
    print("old credential: STILL WORKS (unexpected)")
except Exception as e:  # noqa: BLE001
    print("old credential: refused ->", str(e).split(":")[0])
tok = k.auth(person, password, secret, sec["algo"], privileged=False)
print("new credential: works, token length", len(tok))
