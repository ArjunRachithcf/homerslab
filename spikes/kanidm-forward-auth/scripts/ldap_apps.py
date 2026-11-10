"""LDAP applications (per-person, per-app passwords for apps that check a password over LDAP bind).

The app is linked to a group (its audience); a person binds as name=<p>,app=<app>,<basedn>.
Secrets (the generated app passwords) go to ../secrets only.
"""

import json
import os
import sys

sys.path.insert(0, os.path.dirname(__file__))
import kanidm_api as k  # noqa: E402

M = k.manager_token()

# bootstrap amendment: idm_admin grants the manager the application admin role
adm = k.auth("idm_admin", k.read_secret("idm_admin.password"))
k.request("POST", "/v1/group/idm_application_admins/_attr/member", token=adm, body=["homerslab"])
print("homerslab -> idm_application_admins: ok (granted by idm_admin)")

try:
    st, app, _ = k.request("POST", "/scim/v1/Application", token=M, body={
        "name": "media-ldap", "displayname": "Media server (LDAP)", "linked_group": {"value": "adults"}})
    print("POST /scim/v1/Application ->", st, "| linked group:", json.dumps(app.get("linked_group")))
except k.ApiError as e:
    print("POST /scim/v1/Application ->", e)
st, app, _ = k.request("GET", "/scim/v1/Application/media-ldap", token=M)
app_uuid = app["id"]
print("application media-ldap uuid", app_uuid)


def make_password(person):
    body = {"applicationUuid": app_uuid, "label": "spike"}
    try:
        st, pw, _ = k.request("POST", f"/scim/v1/Person/{person}/Application/_create_password", token=M, body=body)
        how = "manager token"
    except k.ApiError as e:
        print(f"  manager token cannot create {person}'s app password: {e}")
        c = json.load(open(os.path.join(k.SECRETS, f"{person}.json")))
        import base64
        tok = k.auth(person, c["password"], base64.b32decode(c["totp_secret_b32"]), c["algo"], privileged=True)
        st, pw, _ = k.request("POST", f"/scim/v1/Person/{person}/Application/_create_password", token=tok, body=body)
        how = f"{person}'s own privileged session"
    k.save_secret(f"{person}.media-ldap.password", pw["secret"])
    print(f"  {person}: app password created with {how} (label {pw['label']}, length {len(pw['secret'])})")


for p in ("ada", "kim"):
    try:
        make_password(p)
    except Exception as e:  # noqa: BLE001
        print(f"  {p}: {str(e)[:200]}")
