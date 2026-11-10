"""Manager-token operations the browser test triggers mid-run.

  admin.py scopemap-set <client> <group> <scope>...   change a client's audience
  admin.py scopemap-del <client> <group>
  admin.py client-delete <client>
  admin.py person-add <name> <group>                  create + enrol password/TOTP (invite by API)
  admin.py person-delete <name>
  admin.py sessions <name>                            list the person's OAuth2 sessions (no secrets)
"""

import os
import sys

sys.path.insert(0, os.path.dirname(__file__))
import kanidm_api as k  # noqa: E402
import setup  # noqa: E402

cmd, *args = sys.argv[1:]
T = k.manager_token()

if cmd == "scopemap-set":
    client, group, *scopes = args
    k.request("POST", f"/v1/oauth2/{client}/_scopemap/{group}", token=T, body=scopes)
    st, c, _ = k.request("GET", f"/v1/oauth2/{client}", token=T)
    print("scope maps now:", c["attrs"].get("oauth2_rs_scope_map"))
elif cmd == "scopemap-del":
    client, group = args
    k.request("DELETE", f"/v1/oauth2/{client}/_scopemap/{group}", token=T)
    st, c, _ = k.request("GET", f"/v1/oauth2/{client}", token=T)
    print("scope maps now:", c["attrs"].get("oauth2_rs_scope_map"))
elif cmd == "client-delete":
    (client,) = args
    st, _, _ = k.request("DELETE", f"/v1/oauth2/{client}", token=T)
    print(f"DELETE /v1/oauth2/{client} -> {st}")
    try:
        k.request("GET", f"/oauth2/openid/{client}/.well-known/openid-configuration")
        print("discovery still answers (unexpected)")
    except k.ApiError as e:
        print(f"discovery for {client} now -> HTTP {e.status}")
elif cmd == "person-add":
    name, group = args
    setup.ensure_person(name, f"{name} (test)", f"{name}@hlspike.test")
    k.request("POST", f"/v1/group/{group}/_attr/member", token=T, body=[name])
    setup.enrol_password_totp(name)
elif cmd == "person-delete":
    (name,) = args
    st, _, _ = k.request("DELETE", f"/v1/person/{name}", token=T)
    print(f"DELETE /v1/person/{name} -> {st}")
elif cmd == "sessions":
    (name,) = args
    st, p, _ = k.request("GET", f"/v1/person/{name}/_attr/oauth2_session", token=T)
    print(f"{name} oauth2 sessions: {len(p or [])}")
else:
    sys.exit(f"unknown command {cmd}")
