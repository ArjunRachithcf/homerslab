"""Everything the identity provider adapter would do at install time, with the manager's API token only.

Creates: role groups (adults, kids, household), two persons with password + TOTP (through a credential
reset intent, like an invite), the guarded app's OAuth2 client, a step-up probe client, and an LDAP
reader service account. Secrets go to ../secrets only.
"""

import base64
import json
import os
import sys

sys.path.insert(0, os.path.dirname(__file__))
import kanidm_api as k  # noqa: E402

T = k.manager_token()
APP = "https://app.hlspike.test:28744"


def call(method, path, body=None, quiet=False):
    try:
        st, out, _ = k.request(method, path, token=T, body=body)
        if not quiet:
            print(f"  {method} {path} -> {st}")
        return out
    except k.ApiError as e:
        print(f"  {method} {path} -> {e}")
        raise


def ensure_group(name, members=()):
    try:
        call("POST", "/v1/group", {"attrs": {"name": [name]}})
    except k.ApiError as e:
        if e.status != 409 and "duplicate" not in e.body.lower():
            raise
    if members:
        call("POST", f"/v1/group/{name}/_attr/member", list(members))


def ensure_person(name, displayname, mail):
    try:
        call("POST", "/v1/person", {"attrs": {"name": [name], "displayname": [displayname]}})
    except k.ApiError as e:
        if e.status != 409 and "duplicate" not in e.body.lower():
            raise
    call("PUT", f"/v1/person/{name}/_attr/mail", [mail])


def enrol_password_totp(name):
    """Invite-style enrolment: an intent token (what a reset link carries), exchanged and used by API."""
    intent = call("GET", f"/v1/person/{name}/_credential/_update_intent/3600")
    print(f"  intent token for {name}: issued, expires at unix {intent['expiry_time']}")
    st, (session, status), _ = k.request("POST", "/v1/credential/_exchange_intent", body=intent["token"])
    print(f"  exchanged: primary={status['primary']} warnings={status['warnings']} can_commit={status['can_commit']}")
    password = base64.urlsafe_b64encode(os.urandom(24)).decode()
    st, status, _ = k.request("POST", "/v1/credential/_update", body=[{"password": password}, session])
    print(f"  password set: warnings={status['warnings']} can_commit={status['can_commit']}")
    st, status, _ = k.request("POST", "/v1/credential/_update", body=["totpgenerate", session])
    sec = status["mfaregstate"]["TotpCheck"]
    secret = bytes(sec["secret"])
    print(f"  TOTP generated: algo={sec['algo']} step={sec['step']} digits={sec['digits']}")
    code = k.totp(secret, sec["algo"], sec["step"], sec["digits"])
    st, status, _ = k.request("POST", "/v1/credential/_update", body=[{"totpverify": [code, "spike"]}, session])
    if status["mfaregstate"] != "None":
        raise RuntimeError(f"TOTP verify did not finish: {status['mfaregstate']}")
    print(f"  TOTP verified: primary={json.dumps(status['primary']['type_'])} can_commit={status['can_commit']}")
    k.request("POST", "/v1/credential/_commit", body=session)
    print("  committed")
    k.save_secret(f"{name}.json", json.dumps({
        "password": password,
        "totp_secret_b32": base64.b32encode(secret).decode(),
        "algo": sec["algo"], "step": sec["step"], "digits": sec["digits"],
    }))


def basic_client(name, displayname, redirect, scope_maps, consent=False):
    try:
        call("POST", "/v1/oauth2/_basic", {"attrs": {
            "name": [name], "displayname": [displayname],
            "oauth2_rs_origin_landing": [APP + "/"],
            "oauth2_strict_redirect_uri": ["true"],
        }})
    except k.ApiError as e:
        if e.status != 409 and "duplicate" not in e.body.lower():
            raise
    call("POST", f"/v1/oauth2/{name}/_attr/oauth2_rs_origin", [redirect])
    for group, scopes in scope_maps.items():
        call("POST", f"/v1/oauth2/{name}/_scopemap/{group}", scopes)
    call("PATCH", f"/v1/oauth2/{name}", {"attrs": {
        "oauth2_prefer_short_username": ["true"],
        "oauth2_consent_prompt_enable": ["true" if consent else "false"],
    }})
    return call("GET", f"/v1/oauth2/{name}/_basic_secret")


if __name__ == "__main__":
    print("account policy of idm_all_persons:")
    g = call("GET", "/v1/group/idm_all_persons", quiet=True)
    for a in sorted(g["attrs"]):
        if a.startswith(("credential_type", "auth_session", "privilege_expiry", "auth_password",
                         "webauthn", "allow_primary")):
            print(f"  {a} = {g['attrs'][a]}")

    print("groups and persons:")
    ensure_group("adults")
    ensure_group("kids")
    ensure_person("ada", "Ada (adult)", "ada@hlspike.test")
    ensure_person("kim", "Kim (kid)", "kim@hlspike.test")
    call("POST", "/v1/group/adults/_attr/member", ["ada"])
    call("POST", "/v1/group/kids/_attr/member", ["kim"])
    ensure_group("household", ["adults", "kids"])  # nested: an audience made of role groups

    for person in ("ada", "kim"):
        print(f"credentials for {person} (password + TOTP via a reset intent):")
        enrol_password_totp(person)

    print("OAuth2 client 'whoami' (guarded app; scope map: adults only):")
    secret = basic_client("whoami", "Whoami (guarded)", APP + "/oauth2/callback",
                          {"adults": ["openid", "profile", "email", "groups_name"]})
    k.save_secret("client_secret", secret, mode=0o644, subdir="oauth2-proxy")
    if not os.path.exists(os.path.join(k.SECRETS, "oauth2-proxy", "cookie_secret")):
        k.save_secret("cookie_secret", base64.urlsafe_b64encode(os.urandom(32)).decode(), mode=0o644,
                      subdir="oauth2-proxy")

    print("OAuth2 client 'stepup-probe' (adults only):")
    secret = basic_client("stepup-probe", "Step-up probe", APP + "/stepup/callback",
                          {"adults": ["openid", "profile", "groups_name"]})
    k.save_secret("stepup-probe.secret", secret)

    print("LDAP reader service account (managed by homerslab):")
    try:
        call("POST", "/v1/service_account", {"attrs": {
            "name": ["ldap_reader"], "displayname": ["LDAP reader for one app"],
            "entry_managed_by": ["homerslab"]}})
    except k.ApiError as e:
        if e.status != 409 and "duplicate" not in e.body.lower():
            raise
    tok = call("POST", "/v1/service_account/ldap_reader/_api_token",
               {"label": "ldap", "expiry": None, "read_write": False, "compact": False})
    k.save_secret("ldap_reader.token", tok)
    print("  read-only API token saved, length", len(tok))
