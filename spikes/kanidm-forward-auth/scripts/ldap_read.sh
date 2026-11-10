#!/usr/bin/env bash
# LDAPS read checks with the ldap_reader service account (RO API token, member of idm_account_mail_read).
set -u
SPIKE="$(cd "$(dirname "$0")/.." && pwd)"
export LDAP_TOKEN; LDAP_TOKEN="$(cat "$SPIKE/secrets/ldap_reader.token")"
B=dc=idm,dc=hlspike,dc=test
H=ldaps://idm.hlspike.test:3636
dc() { docker compose -p hlspike-kanidm -f "$SPIKE/compose.yaml" exec -T -e LDAP_TOKEN ldapclient sh -c "$1" 2>&1; }

echo "--- bind: ldapwhoami -D dn=token -w <RO API token>"
dc "ldapwhoami -H $H -x -D dn=token -w \"\$LDAP_TOKEN\"; echo exit=\$?"
echo "--- persons in adults: (&(class=person)(memberof=spn=adults@idm.hlspike.test,$B))"
dc "ldapsearch -LLL -o ldif-wrap=no -H $H -x -D dn=token -w \"\$LDAP_TOKEN\" -b $B '(&(class=person)(memberof=spn=adults@idm.hlspike.test,$B))' uid mail entryuuid"
echo "--- persons in kids"
dc "ldapsearch -LLL -o ldif-wrap=no -H $H -x -D dn=token -w \"\$LDAP_TOKEN\" -b $B '(&(class=person)(memberof=spn=kids@idm.hlspike.test,$B))' uid mail"
echo "--- group entries visible to the reader: (class=group)"
dc "ldapsearch -LLL -H $H -x -D dn=token -w \"\$LDAP_TOKEN\" -b $B '(class=group)' name | grep -c '^dn:'"
echo "--- write: ldapmodify displayname (must be refused), then the entry re-read"
dc "out=\$(printf 'dn: spn=ada@idm.hlspike.test,$B\nchangetype: modify\nreplace: displayname\ndisplayname: x\n' | ldapmodify -H $H -x -D dn=token -w \"\$LDAP_TOKEN\" 2>&1); rc=\$?; echo \"\$out\" | grep -v '^\$'; echo exit=\$rc"
dc "ldapsearch -LLL -H $H -x -D dn=token -w \"\$LDAP_TOKEN\" -b $B '(name=ada)' displayname | grep displayname"
echo "--- bad token"
dc "ldapwhoami -H $H -x -D dn=token -w not-a-token 2>&1 | head -2"
echo "--- plain ldap:// and StartTLS on 3636"
dc "timeout 10 ldapwhoami -H ldap://idm.hlspike.test:3636 -x 2>&1 | head -1; timeout 10 ldapwhoami -ZZ -H ldap://idm.hlspike.test:3636 -x 2>&1 | head -1"
echo "--- TLS: certificate verified against Kanidm's CA (LDAPTLS_CACERT); without it"
dc "LDAPTLS_CACERT= LDAPTLS_REQCERT=demand ldapwhoami -H $H -x -D dn=token -w \"\$LDAP_TOKEN\" 2>&1 | head -1"
