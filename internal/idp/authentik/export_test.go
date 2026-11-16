package authentik

import "time"

// SetLDAPRetire sets how long a closed LDAP application and its provider stay off the outpost before they go.
func SetLDAPRetire(a *Authentik, d time.Duration) { a.ldapRetire = d }

// LDAPRefreshEvery is the outpost's refresh_interval outside the tests.
const LDAPRefreshEvery = ldapRefreshEvery

// SetLDAPRefresh sets the outpost's refresh_interval for every adapter of the test binary, from its next call.
func SetLDAPRefresh(every string) { ldapRefresh = every }
