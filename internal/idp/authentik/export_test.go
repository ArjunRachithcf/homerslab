package authentik

import "time"

// SetLDAPRetire sets how long a closed LDAP application and its provider stay off the outpost before they go.
func SetLDAPRetire(a *Authentik, d time.Duration) { a.ldapRetire = d }

// RoleMapping is the name of Homeward's role mapping, which every guard's provider carries.
const RoleMapping = roleMapping
