"""One-time bootstrap: idm_admin creates HomersLab's own service account and a read-write API token.

After this, every operation uses only that token (scripts/setup.py), as the manager would.
"""

import sys

sys.path.insert(0, __import__("os").path.dirname(__file__))
import kanidm_api as k  # noqa: E402

MANAGER_GROUPS = [
    "idm_people_admins",           # persons: create, mail, credential reset intents, delete
    "idm_group_admins",            # role and audience groups
    "idm_oauth2_admins",           # OAuth2 clients, scope maps, secrets
    "idm_service_account_admins",  # the LDAP reader account and its token
    "idm_account_policy_admins",   # credential-type minimum, privilege expiry
]

tok = k.auth("idm_admin", k.read_secret("idm_admin.password"))

try:
    k.request("POST", "/v1/service_account", token=tok, body={"attrs": {
        "name": ["homerslab"],
        "displayname": ["HomersLab manager"],
        "entry_managed_by": ["idm_admins"],
    }})
    print("service account homerslab: created")
except k.ApiError as e:
    print("service account homerslab:", e)

for g in MANAGER_GROUPS:
    try:
        k.request("POST", f"/v1/group/{g}/_attr/member", token=tok, body=["homerslab"])
        print(f"homerslab -> {g}: ok")
    except k.ApiError as e:
        print(f"homerslab -> {g}: {e}")

st, token, _ = k.request("POST", "/v1/service_account/homerslab/_api_token", token=tok, body={
    "label": "spike-manager", "expiry": None, "read_write": True, "compact": False})
k.save_secret("homerslab.token", token)
print("read-write API token for homerslab saved to secrets/homerslab.token, length", len(token))

st, me, _ = k.request("GET", "/v1/self", token=token)
attrs = me["youare"]["attrs"]
print("token works: /v1/self ->", attrs["spn"][0], "| memberof:",
      sorted(m.split("@")[0] for m in attrs.get("memberof", [])))
