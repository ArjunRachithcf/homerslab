"""Account policy: an 'admins' group whose members must use passkeys (credential_type_minimum = passkey).

A password + TOTP person signs in over REST before and after joining 'admins'.
"""

import base64
import json
import os
import sys

sys.path.insert(0, os.path.dirname(__file__))
import kanidm_api as k  # noqa: E402
import setup  # noqa: E402

T = k.manager_token()
setup.ensure_group("admins")
k.request("POST", "/v1/group/admins/_attr/class", token=T, body=["account_policy"])
k.request("PUT", "/v1/group/admins/_attr/credential_type_minimum", token=T, body=["passkey"])
st, g, _ = k.request("GET", "/v1/group/admins", token=T)
print("admins policy:", {a: g["attrs"][a] for a in g["attrs"] if a in ("credential_type_minimum", "class")})

setup.ensure_person("adm1", "Adm 1 (password + TOTP)", "adm1@hlspike.test")
setup.enrol_password_totp("adm1")
c = json.load(open(os.path.join(k.SECRETS, "adm1.json")))
secret = base64.b32decode(c["totp_secret_b32"])


def try_auth(label):
    try:
        tok = k.auth("adm1", c["password"], secret, c["algo"], privileged=False)
        print(f"{label}: password + TOTP sign-in works (token length {len(tok)})")
    except Exception as e:  # noqa: BLE001
        print(f"{label}: refused -> {str(e).split(': ', 1)[-1]}")


try_auth("adm1 outside admins")
k.request("POST", "/v1/group/admins/_attr/member", token=T, body=["adm1"])
import time  # noqa: E402
time.sleep(31)  # a new TOTP window: Kanidm refuses a reused code
try_auth("adm1 inside admins (passkey minimum)")
st, intent, _ = k.request("GET", "/v1/person/adm1/_credential/_update_intent/600", token=T)
st, (sess, status), _ = k.request("POST", "/v1/credential/_exchange_intent", body=intent["token"])
print("adm1's credential-update session now:", {"primary_state": status["primary_state"],
      "passkeys_state": status["passkeys_state"], "warnings": status["warnings"], "can_commit": status["can_commit"]})
k.request("POST", "/v1/credential/_cancel", body=sess)
