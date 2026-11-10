
### Instance status (no token)

`GET /api/instance` -> **200** (no token)
```json
{
  "setup_required": true
}
```

`GET /api/instance/version` -> **401** (no token)
```json
{
  "message": "no valid authentication provided",
  "code": 401
}
```

### Bootstrap: POST /api/setup (no token; only while no account exists)

`POST /api/setup` -> **422** (no token)
request: `{"email":"owner@home.test","name":"Spike Owner","password":"<redacted>","create_pat":true,"pat_expire_in":366}`
```json
{
  "message": "pat_expire_in must be between 1 and 365",
  "code": 422
}
```

`GET /api/instance` -> **200** (no token)
```json
{
  "setup_required": true
}
```

`POST /api/setup` -> **200** (no token)
request: `{"email":"owner@home.test","name":"Spike Owner","password":"<redacted>","create_pat":true,"pat_expire_in":1}`
```json
{
  "email": "owner@home.test",
  "personal_access_token": "<redacted>",
  "user_id": "CiQxNzcxMTE1Yi0wM2Q5LTQ4MmMtODRmZC1iODY1MWRiMWEzYjcSBWxvY2Fs"
}
```

`POST /api/setup` -> **412** (no token)
request: `{"email":"second@home.test","name":"Second","password":"<redacted>","create_pat":true}`
```json
{
  "message": "setup already completed",
  "code": 412
}
```

`GET /api/instance` -> **200** (no token)
```json
{
  "setup_required": false
}
```

### Owner token works; account

`GET /api/users/current` -> **200**
```json
{
  "auto_groups": [],
  "email": "owner@home.test",
  "id": "CiQxNzcxMTE1Yi0wM2Q5LTQ4MmMtODRmZC1iODY1MWRiMWEzYjcSBWxvY2Fs",
  "idp_id": "local",
  "is_blocked": false,
  "is_current": true,
  "is_service_user": false,
  "issued": "api",
  "last_login": "0001-01-01T00:00:00Z",
  "name": "Spike Owner",
  "pending_approval": false,
  "permissions": {
    "is_restricted": false,
    "modules": {
      "accounts": {
        "create": true,
        "delete": true,
        "read": true,
        "update": true
      },
      "agent_network": {
        "create": true,
        "delete": true,
        "read": true,
        "update": true
      },
      "agent_network.budgets": {
        "create": true,
        "delete": true,
        "read": true,
        "update": true
      },
      "agent_network.guardrails": {
        "create": true,
        "delete": true,
        "read": true,
        "update": true
      },
      "agent_network.logs": {
        "create": true,
        "delete": true,
        "read": true,
        "update": true
      },
      "agent_network.policies": {
        "create": true,
        "delete": true,
        "read": true,
        "update": true
      },
      "agent_network.providers": {
        "create": true,
        "delete": true,
        "read": true,
        "update": true
      },
      "agent_network.settings": {
        "create": true,
        "delete": true,
        "read": true,
        "update": true
      },
      "agent_network.usage": {
        "create": true,
        "delete": true,
        "read": true,
        "update": true
      },
      "dns": {
        "create": true,
        "delete": true,
        "read": true,
        "update": true
      },
      "events": {
        "create": true,
        "delete": true,
        "read": true,
        "update": true
      },
      "groups": {
        "create": true,
        "delete": true,
        "read": true,
        "update": true
      },
      "identity_providers": {
        "create": true,
        "delete": true,
        "read": true,
        "update": true
      },
      "nameservers": {
        "create": true,
        "delete": true,
        "read": true,
        "update": true
      },
      "networks": {
        "create": true,
        "delete": true,
        "read": true,
        "update": true
      },
      "pats": {
        "create": true,
        "delete": true,
        "read": true,
        "update": true
      },
      "peers": {
        "create": true,
        "delete": true,
        "read": true,
        "update": true
      },
      "policies": {
        "create": true,
        "delete": true,
        "read": true,
        "update": true
      },
      "remote_jobs": {
        "create": true,
        "delete": true,
        "read": true,
        "update": true
      },
      "routes": {
        "create": true,
        "delete": true,
        "read": true,
        "update": true
      },
      "services": {
        "create": true,
        "delete": true,
        "read": true,
        "update": true
      },
      "settings": {
        "create": true,
        "delete": true,
        "read": true,
        "update": true
      },
      "setup_keys": {
        "create": true,
        "delete": true,
        "read": true,
        "update": true
      },
      "users": {
        "create": true,
        "delete": true,
        "read": true,
        "update": true
      }
    }
  },
  "role": "owner",
  "status": "active"
}
```

`GET /api/users/CiQxNzcxMTE1Yi0wM2Q5LTQ4MmMtODRmZC1iODY1MWRiMWEzYjcSBWxvY2Fs/tokens` -> **200**
```json
[
  {
    "created_at": "2026-11-09T18:53:12.022524991Z",
    "created_by": "CiQxNzcxMTE1Yi0wM2Q5LTQ4MmMtODRmZC1iODY1MWRiMWEzYjcSBWxvY2Fs",
    "expiration_date": "2026-11-10T18:53:12.022524991Z",
    "id": "dbp1d67i9k7g00a1mfgg",
    "last_used": "2026-11-09T18:53:22.03965232Z",
    "name": "setup-token"
  }
]
```

`GET /api/accounts` -> **200**
```json
[
  {
    "created_at": "2026-11-09T18:53:11.987981181Z",
    "created_by": "CiQxNzcxMTE1Yi0wM2Q5LTQ4MmMtODRmZC1iODY1MWRiMWEzYjcSBWxvY2Fs",
    "domain": "netbird.selfhosted",
    "domain_category": "private",
    "id": "dbp1d5vi9k7g00a1mfeg",
    "onboarding": {
      "onboarding_flow_pending": true,
      "signup_form_pending": true
    },
    "settings": {
      "agent_network_only": false,
      "auto_update_always": false,
      "auto_update_version": "disabled",
      "dns_domain": "",
      "embedded_idp_enabled": true,
      "extra": {
        "network_traffic_logs_enabled": false,
        "network_traffic_logs_groups": null,
        "network_traffic_packet_counter_enabled": false,
        "peer_approval_enabled": false,
        "user_approval_required": true
      },
      "groups_propagation_enabled": true,
      "ipv6_enabled_groups": [
        "dbp1d5vi9k7g00a1mffg"
      ],
      "jwt_allow_groups": [],
      "jwt_groups_claim_name": "",
      "jwt_groups_enabled": false,
      "lazy_connection_enabled": true,
      "local_auth_disabled": false,
      "local_mfa_enabled": false,
      "metrics_push_enabled": false,
      "network_range": "100.74.0.0/16",
      "network_range_v6": "fd60:f740:8b9c:b6bd::/64",
      "peer_expose_enabled": false,
      "peer_expose_groups": null,
      "peer_hostname_collision_rejected": false,
      "peer_inactivity_expiration": 600,
      "peer_inactivity_expiration_enabled": false,
      "peer_login_expiration": 86400,
      "peer_login_expiration_enabled": true,
      "regular_users_view_blocked": true,
      "routing_peer_dns_resolution_enabled": true
    }
  }
]
```

`GET /api/accounts` -> **200**
```json
[
  {
    "created_at": "2026-11-09T18:53:11.987981181Z",
    "created_by": "CiQxNzcxMTE1Yi0wM2Q5LTQ4MmMtODRmZC1iODY1MWRiMWEzYjcSBWxvY2Fs",
    "domain": "netbird.selfhosted",
    "domain_category": "private",
    "id": "dbp1d5vi9k7g00a1mfeg",
    "onboarding": {
      "onboarding_flow_pending": true,
      "signup_form_pending": true
    },
    "settings": {
      "agent_network_only": false,
      "auto_update_always": false,
      "auto_update_version": "disabled",
      "dns_domain": "",
      "embedded_idp_enabled": true,
      "extra": {
        "network_traffic_logs_enabled": false,
        "network_traffic_logs_groups": null,
        "network_traffic_packet_counter_enabled": false,
        "peer_approval_enabled": false,
        "user_approval_required": true
      },
      "groups_propagation_enabled": true,
      "ipv6_enabled_groups": [
        "dbp1d5vi9k7g00a1mffg"
      ],
      "jwt_allow_groups": [],
      "jwt_groups_claim_name": "",
      "jwt_groups_enabled": false,
      "lazy_connection_enabled": true,
      "local_auth_disabled": false,
      "local_mfa_enabled": false,
      "metrics_push_enabled": false,
      "network_range": "100.74.0.0/16",
      "network_range_v6": "fd60:f740:8b9c:b6bd::/64",
      "peer_expose_enabled": false,
      "peer_expose_groups": null,
      "peer_hostname_collision_rejected": false,
      "peer_inactivity_expiration": 600,
      "peer_inactivity_expiration_enabled": false,
      "peer_login_expiration": 86400,
      "peer_login_expiration_enabled": true,
      "regular_users_view_blocked": true,
      "routing_peer_dns_resolution_enabled": true
    }
  }
]
```

### Account network range (before any peer joins)

`PUT /api/accounts/dbp1d5vi9k7g00a1mfeg` -> **200**
request: `{"settings":{"agent_network_only":false,"auto_update_always":false,"auto_update_version":"disabled","dns_domain":"","embedded_idp_enabled":true,"extra":{"network_traffic_logs_enabled":false,"network_traffic_logs_groups":null,"network_traffic_packet_counter_enabled":false,"peer_approval_enabled":false,"user_approval_required":true},"groups_propagation_enabled":true,"ipv6_enabled_groups":["dbp1d5vi9k7g00a1mffg"],"jwt_allow_groups":[],"jwt_groups_claim_name":"","jwt_groups_enabled":false,"lazy_connection_enabled":true,"local_auth_disabled":false,"local_mfa_enabled":false,"metrics_push_enabled":false,"network_range":"100.88.0.0/16","network_range_v6":"fd60:f740:8b9c:b6bd::/64","peer_expose_enabled":false,"peer_expose_groups":null,"peer_hostname_collision_rejected":false,"peer_inactivity_expiration":600,"peer_inactivity_expiration_enabled":false,"peer_login_expiration":86400,"peer_login_expiration_enabled":true,"regular_users_view_blocked":true,"routing_peer_dns_resolution_enabled":true}}`
```json
{
  "created_at": "2026-11-09T18:53:11.987981181Z",
  "created_by": "CiQxNzcxMTE1Yi0wM2Q5LTQ4MmMtODRmZC1iODY1MWRiMWEzYjcSBWxvY2Fs",
  "domain": "netbird.selfhosted",
  "domain_category": "private",
  "id": "dbp1d5vi9k7g00a1mfeg",
  "onboarding": {
    "onboarding_flow_pending": true,
    "signup_form_pending": true
  },
  "settings": {
    "agent_network_only": false,
    "auto_update_always": false,
    "auto_update_version": "disabled",
    "dns_domain": "",
    "embedded_idp_enabled": false,
    "extra": {
      "network_traffic_logs_enabled": false,
      "network_traffic_logs_groups": null,
      "network_traffic_packet_counter_enabled": false,
      "peer_approval_enabled": false,
      "user_approval_required": true
    },
    "groups_propagation_enabled": true,
    "ipv6_enabled_groups": [
      "dbp1d5vi9k7g00a1mffg"
    ],
    "jwt_allow_groups": [],
    "jwt_groups_claim_name": "",
    "jwt_groups_enabled": false,
    "lazy_connection_enabled": true,
    "local_auth_disabled": false,
    "local_mfa_enabled": false,
    "metrics_push_enabled": false,
    "network_range": "100.88.0.0/16",
    "network_range_v6": "fd60:f740:8b9c:b6bd::/64",
    "peer_expose_enabled": false,
    "peer_expose_groups": null,
    "peer_hostname_collision_rejected": false,
    "peer_inactivity_expiration": 600,
    "peer_inactivity_expiration_enabled": false,
    "peer_login_expiration": 86400,
    "peer_login_expiration_enabled": true,
    "regular_users_view_blocked": true,
    "routing_peer_dns_resolution_enabled": true
  }
}
```

### Service user and its token (as the owner)

`POST /api/users` -> **200**
request: `{"name":"homerslab","role":"admin","auto_groups":[],"is_service_user":true}`
```json
{
  "auto_groups": [],
  "email": "",
  "id": "b55e57a1-059a-4ef6-a817-2e41aa3f3148",
  "is_blocked": false,
  "is_current": false,
  "is_service_user": true,
  "issued": "api",
  "last_login": "0001-01-01T00:00:00Z",
  "name": "homerslab",
  "pending_approval": false,
  "role": "admin",
  "status": "active"
}
```

`POST /api/users/b55e57a1-059a-4ef6-a817-2e41aa3f3148/tokens` -> **422**
request: `{"name":"bad-366","expires_in":366}`
```json
{
  "message": "expiration has to be between 1 and 365",
  "code": 422
}
```

`POST /api/users/b55e57a1-059a-4ef6-a817-2e41aa3f3148/tokens` -> **422**
request: `{"name":"bad-0","expires_in":0}`
```json
{
  "message": "expiration has to be between 1 and 365",
  "code": 422
}
```

`POST /api/users/b55e57a1-059a-4ef6-a817-2e41aa3f3148/tokens` -> **200**
request: `{"name":"homerslab-1","expires_in":365}`
```json
{
  "personal_access_token": {
    "created_at": "2026-11-09T18:53:44.217567794Z",
    "created_by": "CiQxNzcxMTE1Yi0wM2Q5LTQ4MmMtODRmZC1iODY1MWRiMWEzYjcSBWxvY2Fs",
    "expiration_date": "2027-11-09T18:53:44.217567794Z",
    "id": "dbp1de7i9k7g00a1mfng",
    "name": "homerslab-1"
  },
  "plain_token": "<redacted>"
}
```

### Rotation, done by the service user itself

`GET /api/users/current` -> **403**
```json
{
  "message": "permission denied",
  "code": 403
}
```

`POST /api/users/b55e57a1-059a-4ef6-a817-2e41aa3f3148/tokens` -> **200**
request: `{"name":"homerslab-2","expires_in":365}`
```json
{
  "personal_access_token": {
    "created_at": "2026-11-09T18:53:44.750952522Z",
    "created_by": "b55e57a1-059a-4ef6-a817-2e41aa3f3148",
    "expiration_date": "2027-11-09T18:53:44.750952522Z",
    "id": "dbp1de7i9k7g00a1mfp0",
    "name": "homerslab-2"
  },
  "plain_token": "<redacted>"
}
```

`GET /api/users/current` -> **403**
```json
{
  "message": "permission denied",
  "code": 403
}
```

`DELETE /api/users/b55e57a1-059a-4ef6-a817-2e41aa3f3148/tokens/dbp1de7i9k7g00a1mfng` -> **200**
```json
{}
```

`GET /api/groups` -> **404**
```json
{
  "message": "invalid token: pat: <hash redacted> redacted> not found",
  "code": 404
}
```

`GET /api/users/b55e57a1-059a-4ef6-a817-2e41aa3f3148/tokens` -> **200**
```json
[
  {
    "created_at": "2026-11-09T18:53:44.750952522Z",
    "created_by": "b55e57a1-059a-4ef6-a817-2e41aa3f3148",
    "expiration_date": "2027-11-09T18:53:44.750952522Z",
    "id": "dbp1de7i9k7g00a1mfp0",
    "last_used": "2026-11-09T18:53:44.999190761Z",
    "name": "homerslab-2"
  }
]
```

`GET /api/groups` -> **200**
```json
[
  {
    "id": "dbp1d5vi9k7g00a1mffg",
    "issued": "api",
    "name": "All",
    "peers": null,
    "peers_count": 0,
    "resources": null,
    "resources_count": 0
  }
]
```

### Groups

`POST /api/groups` -> **200**
request: `{"name":"household"}`
```json
{
  "id": "dbp1dlvi9k7g00a1mfsg",
  "issued": "api",
  "name": "household",
  "peers": null,
  "peers_count": 0,
  "resources": null,
  "resources_count": 0
}
```

`POST /api/groups` -> **200**
request: `{"name":"edges"}`
```json
{
  "id": "dbp1dlvi9k7g00a1mfu0",
  "issued": "api",
  "name": "edges",
  "peers": null,
  "peers_count": 0,
  "resources": null,
  "resources_count": 0
}
```

`POST /api/groups` -> **409**
request: `{"name":"edges"}`
```json
{
  "message": "group with name edges already exists",
  "code": 409
}
```

### Policies

`GET /api/policies` -> **200**
```json
[
  {
    "description": "This is a default rule that allows connections between all the resources",
    "enabled": true,
    "id": "dbp1d5vi9k7g00a1mfg0",
    "name": "Default",
    "rules": [
      {
        "action": "accept",
        "bidirectional": true,
        "description": "This is a default rule that allows connections between all the resources",
        "destinations": [
          {
            "id": "dbp1d5vi9k7g00a1mffg",
            "name": "All",
            "peers_count": 0,
            "resources_count": 0
          }
        ],
        "enabled": true,
        "id": "dbp1d5vi9k7g00a1mfg0",
        "name": "Default",
        "protocol": "all",
        "sources": [
          {
            "id": "dbp1d5vi9k7g00a1mffg",
            "name": "All",
            "peers_count": 0,
            "resources_count": 0
          }
        ]
      }
    ],
    "source_posture_checks": null
  }
]
```

`DELETE /api/policies/dbp1d5vi9k7g00a1mfg0` -> **200**
```json
{}
```

`POST /api/policies` -> **200**
request: `{"name":"household-to-edges-https","description":"household devices reach the edges on TCP 443","enabled":true,"rules":[{"name":"https","enabled":true,"action":"accept","bidirectional":false,"protocol":"tcp","ports":["443"],"sources":["dbp1dlvi9k7g00a1mfsg"],"destinations":["dbp1dlvi9k7g00a1mfu0"]}]}`
```json
{
  "description": "household devices reach the edges on TCP 443",
  "enabled": true,
  "id": "dbp1dm7i9k7g00a1mg10",
  "name": "household-to-edges-https",
  "rules": [
    {
      "action": "accept",
      "bidirectional": false,
      "description": "",
      "destinations": [
        {
          "id": "dbp1dlvi9k7g00a1mfu0",
          "name": "edges",
          "peers_count": 0,
          "resources_count": 0
        }
      ],
      "enabled": true,
      "id": "dbp1dm7i9k7g00a1mg10",
      "name": "https",
      "ports": [
        "443"
      ],
      "protocol": "tcp",
      "sources": [
        {
          "id": "dbp1dlvi9k7g00a1mfsg",
          "name": "household",
          "peers_count": 0,
          "resources_count": 0
        }
      ]
    }
  ],
  "source_posture_checks": null
}
```

### Setup keys

`POST /api/setup-keys` -> **200**
request: `{"name":"too-short","type":"reusable","expires_in":3600,"auto_groups":["dbp1dlvi9k7g00a1mfu0"],"usage_limit":0}`
```json
{
  "allow_extra_dns_labels": false,
  "auto_groups": [
    "dbp1dlvi9k7g00a1mfu0"
  ],
  "ephemeral": false,
  "expires": "2026-11-09T19:54:27.253690796Z",
  "id": "dbp1dovi9k7g00a1mg2g",
  "key": "<redacted>",
  "last_used": "0001-01-01T00:00:00Z",
  "name": "too-short",
  "revoked": false,
  "state": "valid",
  "type": "reusable",
  "updated_at": "2026-11-09T18:54:27.253707418Z",
  "usage_limit": 0,
  "used_times": 0,
  "valid": true
}
```

`POST /api/setup-keys` -> **200**
request: `{"name":"homerslab-nodes","type":"reusable","expires_in":86400,"auto_groups":["dbp1dlvi9k7g00a1mfu0"],"usage_limit":0,"ephemeral":false}`
```json
{
  "allow_extra_dns_labels": false,
  "auto_groups": [
    "dbp1dlvi9k7g00a1mfu0"
  ],
  "ephemeral": false,
  "expires": "2026-11-10T18:54:27.904665224Z",
  "id": "dbp1dovi9k7g00a1mg3g",
  "key": "<redacted>",
  "last_used": "0001-01-01T00:00:00Z",
  "name": "homerslab-nodes",
  "revoked": false,
  "state": "valid",
  "type": "reusable",
  "updated_at": "2026-11-09T18:54:27.904678075Z",
  "usage_limit": 0,
  "used_times": 0,
  "valid": true
}
```

`POST /api/setup-keys` -> **200**
request: `{"name":"anna-phone","type":"one-off","expires_in":86400,"auto_groups":["dbp1dlvi9k7g00a1mfsg"],"usage_limit":1,"ephemeral":false}`
```json
{
  "allow_extra_dns_labels": false,
  "auto_groups": [
    "dbp1dlvi9k7g00a1mfsg"
  ],
  "ephemeral": false,
  "expires": "2026-11-10T18:54:28.31469435Z",
  "id": "dbp1dp7i9k7g00a1mg4g",
  "key": "<redacted>",
  "last_used": "0001-01-01T00:00:00Z",
  "name": "anna-phone",
  "revoked": false,
  "state": "valid",
  "type": "one-off",
  "updated_at": "2026-11-09T18:54:28.314706642Z",
  "usage_limit": 1,
  "used_times": 0,
  "valid": true
}
```

`GET /api/setup-keys` -> **200**
```json
[
  {
    "allow_extra_dns_labels": false,
    "auto_groups": [
      "dbp1dlvi9k7g00a1mfu0"
    ],
    "ephemeral": false,
    "expires": "2026-11-09T19:54:27.253690796Z",
    "id": "dbp1dovi9k7g00a1mg2g",
    "key": "<redacted>",
    "last_used": "0001-01-01T00:00:00Z",
    "name": "too-short",
    "revoked": false,
    "state": "valid",
    "type": "reusable",
    "updated_at": "2026-11-09T18:54:27.253707418Z",
    "usage_limit": 0,
    "used_times": 0,
    "valid": true
  },
  {
    "allow_extra_dns_labels": false,
    "auto_groups": [
      "dbp1dlvi9k7g00a1mfu0"
    ],
    "ephemeral": false,
    "expires": "2026-11-10T18:54:27.904665224Z",
    "id": "dbp1dovi9k7g00a1mg3g",
    "key": "<redacted>",
    "last_used": "0001-01-01T00:00:00Z",
    "name": "homerslab-nodes",
    "revoked": false,
    "state": "valid",
    "type": "reusable",
    "updated_at": "2026-11-09T18:54:27.904678075Z",
    "usage_limit": 0,
    "used_times": 0,
    "valid": true
  },
  {
    "allow_extra_dns_labels": false,
    "auto_groups": [
      "dbp1dlvi9k7g00a1mfsg"
    ],
    "ephemeral": false,
    "expires": "2026-11-10T18:54:28.31469435Z",
    "id": "dbp1dp7i9k7g00a1mg4g",
    "key": "<redacted>",
    "last_used": "0001-01-01T00:00:00Z",
    "name": "anna-phone",
    "revoked": false,
    "state": "valid",
    "type": "one-off",
    "updated_at": "2026-11-09T18:54:28.314706642Z",
    "usage_limit": 1,
    "used_times": 0,
    "valid": true
  }
]
```

`GET /api/setup-keys` -> **200**
```json
[
  {
    "allow_extra_dns_labels": false,
    "auto_groups": [
      "dbp1dlvi9k7g00a1mfu0"
    ],
    "ephemeral": false,
    "expires": "2026-11-09T19:54:27.253690796Z",
    "id": "dbp1dovi9k7g00a1mg2g",
    "key": "<redacted>",
    "last_used": "0001-01-01T00:00:00Z",
    "name": "too-short",
    "revoked": false,
    "state": "valid",
    "type": "reusable",
    "updated_at": "2026-11-09T18:54:27.253707418Z",
    "usage_limit": 0,
    "used_times": 0,
    "valid": true
  },
  {
    "allow_extra_dns_labels": false,
    "auto_groups": [
      "dbp1dlvi9k7g00a1mfu0"
    ],
    "ephemeral": false,
    "expires": "2026-11-10T18:54:27.904665224Z",
    "id": "dbp1dovi9k7g00a1mg3g",
    "key": "<redacted>",
    "last_used": "0001-01-01T00:00:00Z",
    "name": "homerslab-nodes",
    "revoked": false,
    "state": "valid",
    "type": "reusable",
    "updated_at": "2026-11-09T18:54:27.904678075Z",
    "usage_limit": 0,
    "used_times": 0,
    "valid": true
  },
  {
    "allow_extra_dns_labels": false,
    "auto_groups": [
      "dbp1dlvi9k7g00a1mfsg"
    ],
    "ephemeral": false,
    "expires": "2026-11-10T18:54:28.31469435Z",
    "id": "dbp1dp7i9k7g00a1mg4g",
    "key": "<redacted>",
    "last_used": "0001-01-01T00:00:00Z",
    "name": "anna-phone",
    "revoked": false,
    "state": "valid",
    "type": "one-off",
    "updated_at": "2026-11-09T18:54:28.314706642Z",
    "usage_limit": 1,
    "used_times": 0,
    "valid": true
  }
]
```

`DELETE /api/setup-keys/dbp1dovi9k7g00a1mg2g` -> **200**
```json
{}
```

### Peers after joining with the setup keys

`GET /api/peers` -> **200**
```json
[
  {
    "accessible_peers_count": 0,
    "approval_required": false,
    "city_name": "",
    "connected": true,
    "connection_ip": "10.231.0.31",
    "country_code": "",
    "created_at": "2026-11-09T18:54:41.849350727Z",
    "dns_label": "device.netbird.selfhosted",
    "ephemeral": false,
    "extra_dns_labels": [],
    "geoname_id": 0,
    "groups": [
      {
        "id": "dbp1d5vi9k7g00a1mffg",
        "name": "All",
        "peers_count": 2,
        "resources_count": 0
      },
      {
        "id": "dbp1dlvi9k7g00a1mfsg",
        "name": "household",
        "peers_count": 1,
        "resources_count": 0
      }
    ],
    "hostname": "device",
    "id": "dbp1dsfi9k7g00a1mge0",
    "inactivity_expiration_enabled": false,
    "ip": "100.88.26.210",
    "ipv6": "fd60:f740:8b9c:b6bd:ad5e:e085:938f:18f3",
    "kernel_version": "7.2.11-200.fc44.x86_64",
    "last_login": "2026-11-09T18:54:41.849350727Z",
    "last_seen": "2026-11-09T18:54:42Z",
    "local_flags": {
      "block_inbound": false,
      "block_lan_access": false,
      "disable_client_routes": false,
      "disable_dns": false,
      "disable_firewall": false,
      "disable_server_routes": false,
      "lazy_connection_enabled": false,
      "remote_jobs_allowed": false,
      "rosenpass_enabled": false,
      "rosenpass_permissive": false,
      "server_ssh_allowed": false
    },
    "login_expiration_enabled": false,
    "login_expired": false,
    "name": "device",
    "network_addresses": [
      {
        "mac": "1a:a1:60:8b:35:2e",
        "net_ip": "10.231.0.31/24"
      }
    ],
    "os": "Alpine Linux 3.24.3",
    "serial_number": "Default string",
    "ssh_enabled": false,
    "ui_version": "",
    "user_id": "",
    "version": "0.82.0"
  },
  {
    "accessible_peers_count": 0,
    "approval_required": false,
    "city_name": "",
    "connected": true,
    "connection_ip": "10.231.0.30",
    "country_code": "",
    "created_at": "2026-11-09T18:54:41.277488458Z",
    "dns_label": "edge.netbird.selfhosted",
    "ephemeral": false,
    "extra_dns_labels": [],
    "geoname_id": 0,
    "groups": [
      {
        "id": "dbp1d5vi9k7g00a1mffg",
        "name": "All",
        "peers_count": 2,
        "resources_count": 0
      },
      {
        "id": "dbp1dlvi9k7g00a1mfu0",
        "name": "edges",
        "peers_count": 1,
        "resources_count": 0
      }
    ],
    "hostname": "edge",
    "id": "dbp1dsfi9k7g00a1mg9g",
    "inactivity_expiration_enabled": false,
    "ip": "100.88.7.186",
    "ipv6": "fd60:f740:8b9c:b6bd:30d7:c880:e67d:80ad",
    "kernel_version": "7.2.11-200.fc44.x86_64",
    "last_login": "2026-11-09T18:54:41.277488458Z",
    "last_seen": "2026-11-09T18:54:41Z",
    "local_flags": {
      "block_inbound": false,
      "block_lan_access": false,
      "disable_client_routes": false,
      "disable_dns": false,
      "disable_firewall": false,
      "disable_server_routes": false,
      "lazy_connection_enabled": false,
      "remote_jobs_allowed": false,
      "rosenpass_enabled": false,
      "rosenpass_permissive": false,
      "server_ssh_allowed": false
    },
    "login_expiration_enabled": false,
    "login_expired": false,
    "name": "edge",
    "network_addresses": [
      {
        "mac": "b6:23:aa:96:f9:c9",
        "net_ip": "10.231.0.30/24"
      }
    ],
    "os": "Alpine Linux 3.24.3",
    "serial_number": "Default string",
    "ssh_enabled": false,
    "ui_version": "",
    "user_id": "",
    "version": "0.82.0"
  }
]
```

`GET /api/setup-keys` -> **200**
```json
[
  {
    "allow_extra_dns_labels": false,
    "auto_groups": [
      "dbp1dlvi9k7g00a1mfu0"
    ],
    "ephemeral": false,
    "expires": "2026-11-10T18:54:27.904665224Z",
    "id": "dbp1dovi9k7g00a1mg3g",
    "key": "<redacted>",
    "last_used": "2026-11-09T18:54:41.279290111Z",
    "name": "homerslab-nodes",
    "revoked": false,
    "state": "valid",
    "type": "reusable",
    "updated_at": "2026-11-09T18:54:27.904678075Z",
    "usage_limit": 0,
    "used_times": 1,
    "valid": true
  },
  {
    "allow_extra_dns_labels": false,
    "auto_groups": [
      "dbp1dlvi9k7g00a1mfsg"
    ],
    "ephemeral": false,
    "expires": "2026-11-10T18:54:28.31469435Z",
    "id": "dbp1dp7i9k7g00a1mg4g",
    "key": "<redacted>",
    "last_used": "2026-11-09T18:54:41.851040981Z",
    "name": "anna-phone",
    "revoked": false,
    "state": "overused",
    "type": "one-off",
    "updated_at": "2026-11-09T18:54:28.314706642Z",
    "usage_limit": 1,
    "used_times": 1,
    "valid": false
  }
]
```

### DNS: a custom zone, a wildcard and per-app records

`POST /api/dns/zones` -> **200**
request: `{"name":"household","domain":"home.test","enabled":true,"enable_search_domain":false,"distribution_groups":["dbp1dlvi9k7g00a1mfsg","dbp1dlvi9k7g00a1mfu0"]}`
```json
{
  "distribution_groups": [
    "dbp1dlvi9k7g00a1mfsg",
    "dbp1dlvi9k7g00a1mfu0"
  ],
  "domain": "home.test",
  "enable_search_domain": false,
  "enabled": true,
  "id": "dbp1ejfi9k7g00a1mgp0",
  "name": "household",
  "records": []
}
```

`POST /api/dns/zones` -> **422**
request: `{"name":"bad","domain":"*.home.test","enabled":true,"enable_search_domain":false,"distribution_groups":["dbp1dlvi9k7g00a1mfsg"]}`
```json
{
  "message": "invalid zone domain format",
  "code": 422
}
```

`POST /api/dns/zones/dbp1ejfi9k7g00a1mgp0/records` -> **200**
request: `{"name":"*.home.test","type":"A","content":"100.88.7.186","ttl":60}`
```json
{
  "content": "100.88.7.186",
  "id": "dbp1ejni9k7g00a1mgr0",
  "name": "*.home.test",
  "ttl": 60,
  "type": "A"
}
```

`POST /api/dns/zones/dbp1ejfi9k7g00a1mgp0/records` -> **200**
request: `{"name":"app.home.test","type":"A","content":"100.88.7.186","ttl":60}`
```json
{
  "content": "100.88.7.186",
  "id": "dbp1ejni9k7g00a1mgsg",
  "name": "app.home.test",
  "ttl": 60,
  "type": "A"
}
```

`POST /api/dns/zones/dbp1ejfi9k7g00a1mgp0/records` -> **200**
request: `{"name":"node2.home.test","type":"A","content":"100.88.26.210","ttl":60}`
```json
{
  "content": "100.88.26.210",
  "id": "dbp1ejni9k7g00a1mgu0",
  "name": "node2.home.test",
  "ttl": 60,
  "type": "A"
}
```

`POST /api/dns/zones/dbp1ejfi9k7g00a1mgp0/records` -> **200**
request: `{"name":"www.home.test","type":"CNAME","content":"app.home.test","ttl":60}`
```json
{
  "content": "app.home.test",
  "id": "dbp1ejvi9k7g00a1mgvg",
  "name": "www.home.test",
  "ttl": 60,
  "type": "CNAME"
}
```

`POST /api/dns/zones/dbp1ejfi9k7g00a1mgp0/records` -> **422**
request: `{"name":"x.elsewhere.test","type":"A","content":"100.88.7.186","ttl":60}`
```json
{
  "message": "record name does not belong to zone",
  "code": 422
}
```

`POST /api/dns/zones/dbp1ejfi9k7g00a1mgp0/records` -> **422**
request: `{"name":"*.home.test","type":"CNAME","content":"app.home.test","ttl":60}`
```json
{
  "message": "an a, aaaa, or cname record with name *.home.test already exists",
  "code": 422
}
```

`GET /api/dns/zones/dbp1ejfi9k7g00a1mgp0` -> **200**
```json
{
  "distribution_groups": [
    "dbp1dlvi9k7g00a1mfsg",
    "dbp1dlvi9k7g00a1mfu0"
  ],
  "domain": "home.test",
  "enable_search_domain": false,
  "enabled": true,
  "id": "dbp1ejfi9k7g00a1mgp0",
  "name": "household",
  "records": [
    {
      "content": "100.88.7.186",
      "id": "dbp1ejni9k7g00a1mgr0",
      "name": "*.home.test",
      "ttl": 60,
      "type": "A"
    },
    {
      "content": "100.88.7.186",
      "id": "dbp1ejni9k7g00a1mgsg",
      "name": "app.home.test",
      "ttl": 60,
      "type": "A"
    },
    {
      "content": "100.88.26.210",
      "id": "dbp1ejni9k7g00a1mgu0",
      "name": "node2.home.test",
      "ttl": 60,
      "type": "A"
    },
    {
      "content": "app.home.test",
      "id": "dbp1ejvi9k7g00a1mgvg",
      "name": "www.home.test",
      "ttl": 60,
      "type": "CNAME"
    }
  ]
}
```

### Reverse proxy, before any proxy has connected

`GET /api/reverse-proxies/clusters` -> **200**
```json
[]
```

`GET /api/reverse-proxies/domains` -> **200**
```json
[]
```

`POST /api/reverse-proxies/domains` -> **500**
request: `{"domain":"home.test","target_cluster":"proxy.hlspike.test"}`
```json
{
  "message": "internal server error",
  "code": 500
}
```

`POST /api/reverse-proxies/services` -> **412**
request: `{"name":"app","domain":"app.home.test","mode":"tls","listen_port":9443,"enabled":true,"targets":[{"target_id":"dbp1dsfi9k7g00a1mg9g","target_type":"peer","protocol":"tcp","port":8444,"enabled":true,"options":{"proxy_protocol":true}}]}`
```json
{
  "message": "could not derive cluster from domain app.home.test: no proxy clusters available",
  "code": 412
}
```

### Proxy token (account-scoped, by API) and the proxy's registration

`POST /api/reverse-proxies/proxy-tokens` -> **200**
request: `{"name":"homerslab-proxy","expires_in":0}`
```json
{
  "created_at": "2026-11-09T18:57:19.018047036Z",
  "id": "dbp1f3vi9k7g00a1mhag",
  "name": "homerslab-proxy",
  "plain_token": "<redacted>",
  "revoked": false
}
```

`POST /api/reverse-proxies/proxy-tokens` -> **200**
request: `{"name":"short","expires_in":3600}`
```json
{
  "created_at": "2026-11-09T18:57:19.205436118Z",
  "expires_at": "2026-11-09T19:57:19.205436118Z",
  "id": "dbp1f3vi9k7g00a1mhbg",
  "name": "short",
  "plain_token": "<redacted>",
  "revoked": false
}
```

`DELETE /api/reverse-proxies/proxy-tokens/dbp1f3vi9k7g00a1mhbg` -> **200**
```json
{}
```

`GET /api/reverse-proxies/proxy-tokens` -> **200**
```json
[
  {
    "created_at": "2026-11-09T18:57:19.018047036Z",
    "id": "dbp1f3vi9k7g00a1mhag",
    "name": "homerslab-proxy",
    "revoked": false
  },
  {
    "created_at": "2026-11-09T18:57:19.205436118Z",
    "expires_at": "2026-11-09T19:57:19.205436118Z",
    "id": "dbp1f3vi9k7g00a1mhbg",
    "name": "short",
    "revoked": true
  }
]
```

### Reverse proxy, with the proxy connected

`GET /api/reverse-proxies/clusters` -> **200**
```json
[
  {
    "address": "proxy.hlspike.test",
    "connected_proxies": 1,
    "id": "netbird-proxy-75fd8fff-687e-47ad-9bdf-98c0b5a42fa4",
    "online": true,
    "private": false,
    "require_subdomain": false,
    "supports_crowdsec": false,
    "supports_custom_ports": true,
    "type": "account"
  }
]
```

`GET /api/reverse-proxies/domains` -> **200**
```json
[
  {
    "domain": "proxy.hlspike.test",
    "id": "",
    "require_subdomain": false,
    "supports_crowdsec": false,
    "supports_custom_ports": true,
    "supports_private": false,
    "type": "free",
    "validated": true
  }
]
```

`POST /api/reverse-proxies/domains` -> **200**
request: `{"domain":"home.test","target_cluster":"proxy.hlspike.test"}`
```json
{
  "domain": "home.test",
  "id": "dbp1fbni9k7g00a1mhg0",
  "target_cluster": "proxy.hlspike.test",
  "type": "custom",
  "validated": true
}
```

`POST /api/reverse-proxies/domains` -> **200**
request: `{"domain":"nocname.test","target_cluster":"proxy.hlspike.test"}`
```json
{
  "domain": "nocname.test",
  "id": "dbp1fbvi9k7g00a1mhh0",
  "target_cluster": "proxy.hlspike.test",
  "type": "custom",
  "validated": false
}
```

`GET /api/reverse-proxies/domains/dbp1fbvi9k7g00a1mhh0/validate` -> **412**
```json
{
  "message": "dns lookup for validation.nocname.test failed; retry the validation",
  "code": 412
}
```

`POST /api/reverse-proxies/domains` -> **422**
request: `{"domain":"*.wild.test","target_cluster":"proxy.hlspike.test"}`
```json
{
  "message": "invalid domain format",
  "code": 422
}
```

`POST /api/reverse-proxies/domains` -> **409**
request: `{"domain":"home.test","target_cluster":"proxy.hlspike.test"}`
```json
{
  "message": "domain home.test is already registered",
  "code": 409
}
```

`POST /api/reverse-proxies/domains` -> **500**
request: `{"domain":"other.test","target_cluster":"eu.proxy.netbird.io"}`
```json
{
  "message": "internal server error",
  "code": 500
}
```

### Services: what the API refuses

`POST /api/reverse-proxies/services` -> **422**
request: `{"name":"app","domain":"app.home.test","mode":"tls","enabled":true,"targets":[{"target_id":"dbp1dsfi9k7g00a1mg9g","target_type":"peer","protocol":"tcp","port":8444,"enabled":true,"options":{"proxy_protocol":true}}]}`
```json
{
  "message": "listen_port is required for tls services",
  "code": 422
}
```

`POST /api/reverse-proxies/services` -> **422**
request: `{"name":"app","domain":"app.home.test","mode":"tls","listen_port":9443,"enabled":true,"targets":[{"target_id":"dbp1dsfi9k7g00a1mg9g","target_type":"peer","protocol":"tcp","port":8444,"enabled":true,"options":{"proxy_protocol":true}},{"target_id":"dbp1dsfi9k7g00a1mg9g","target_type":"peer","protocol":"tcp","port":8444,"enabled":true,"options":{"proxy_protocol":true}}]}`
```json
{
  "message": "tls services must have exactly one target",
  "code": 422
}
```

`POST /api/reverse-proxies/services` -> **422**
request: `{"name":"app","domain":"app.home.test","mode":"tls","listen_port":9443,"enabled":true,"auth":{"bearer_auth":{"enabled":true}},"targets":[{"target_id":"dbp1dsfi9k7g00a1mg9g","target_type":"peer","protocol":"tcp","port":8444,"enabled":true,"options":{"proxy_protocol":true}}]}`
```json
{
  "message": "auth is not supported for tls services",
  "code": 422
}
```

`POST /api/reverse-proxies/services` -> **422**
request: `{"name":"app","domain":"app.home.test","mode":"http","enabled":true,"targets":[{"target_id":"dbp1dsfi9k7g00a1mg9g","target_type":"peer","protocol":"https","port":8444,"enabled":true,"options":{"proxy_protocol":true}}]}`
```json
{
  "message": "target 0: proxy_protocol is not supported for http services",
  "code": 422
}
```

`POST /api/reverse-proxies/services` -> **412**
request: `{"name":"x","domain":"x.nocname.test","mode":"tls","listen_port":9443,"enabled":true,"targets":[{"target_id":"dbp1dsfi9k7g00a1mg9g","target_type":"peer","protocol":"tcp","port":8444,"enabled":true,"options":{"proxy_protocol":true}}]}`
```json
{
  "message": "could not derive cluster from domain x.nocname.test: domain x.nocname.test is not validated",
  "code": 412
}
```

`POST /api/reverse-proxies/services` -> **412**
request: `{"name":"y","domain":"y.example.org","mode":"tls","listen_port":9443,"enabled":true,"targets":[{"target_id":"dbp1dsfi9k7g00a1mg9g","target_type":"peer","protocol":"tcp","port":8444,"enabled":true,"options":{"proxy_protocol":true}}]}`
```json
{
  "message": "could not derive cluster from domain y.example.org: domain y.example.org does not match any available proxy cluster",
  "code": 412
}
```

### The TLS passthrough service

`POST /api/reverse-proxies/services` -> **200**
request: `{"name":"app","domain":"app.home.test","mode":"tls","listen_port":9443,"enabled":true,"targets":[{"target_id":"dbp1dsfi9k7g00a1mg9g","target_type":"peer","protocol":"tcp","port":8444,"enabled":true,"options":{"proxy_protocol":true}}]}`
```json
{
  "auth": {},
  "domain": "app.home.test",
  "enabled": true,
  "id": "dbp1fgvi9k7g00a1mhn0",
  "listen_port": 9443,
  "meta": {
    "created_at": "2026-11-09T18:58:11.603950243Z",
    "status": "pending"
  },
  "mode": "tls",
  "name": "app",
  "pass_host_header": false,
  "port_auto_assigned": false,
  "private": false,
  "proxy_cluster": "proxy.hlspike.test",
  "rewrite_redirects": false,
  "targets": [
    {
      "access_action": "inherit",
      "enabled": true,
      "host": "100.88.7.186",
      "options": {
        "proxy_protocol": true
      },
      "port": 8444,
      "protocol": "tcp",
      "target_id": "dbp1dsfi9k7g00a1mg9g",
      "target_type": "peer"
    }
  ],
  "terminated": false
}
```

`GET /api/reverse-proxies/services/dbp1fgvi9k7g00a1mhn0` -> **200**
```json
{
  "auth": {},
  "domain": "app.home.test",
  "enabled": true,
  "id": "dbp1fgvi9k7g00a1mhn0",
  "listen_port": 9443,
  "meta": {
    "created_at": "2026-11-09T18:58:11.603950243Z",
    "status": "active"
  },
  "mode": "tls",
  "name": "app",
  "pass_host_header": false,
  "port_auto_assigned": false,
  "private": false,
  "proxy_cluster": "proxy.hlspike.test",
  "rewrite_redirects": false,
  "targets": [
    {
      "access_action": "inherit",
      "enabled": true,
      "host": "100.88.7.186",
      "options": {
        "proxy_protocol": true
      },
      "port": 8444,
      "protocol": "tcp",
      "target_id": "dbp1dsfi9k7g00a1mg9g",
      "target_type": "peer"
    }
  ],
  "terminated": false
}
```

### After the service: peers, policies, a second app on the same port

`GET /api/peers` -> **200**
```json
[
  {
    "accessible_peers_count": 0,
    "approval_required": false,
    "city_name": "",
    "connected": true,
    "connection_ip": "10.231.0.31",
    "country_code": "",
    "created_at": "2026-11-09T18:54:41.849350727Z",
    "dns_label": "device.netbird.selfhosted",
    "ephemeral": false,
    "extra_dns_labels": [],
    "geoname_id": 0,
    "groups": [
      {
        "id": "dbp1d5vi9k7g00a1mffg",
        "name": "All",
        "peers_count": 2,
        "resources_count": 0
      },
      {
        "id": "dbp1dlvi9k7g00a1mfsg",
        "name": "household",
        "peers_count": 1,
        "resources_count": 0
      }
    ],
    "hostname": "device",
    "id": "dbp1dsfi9k7g00a1mge0",
    "inactivity_expiration_enabled": false,
    "ip": "100.88.26.210",
    "ipv6": "fd60:f740:8b9c:b6bd:ad5e:e085:938f:18f3",
    "kernel_version": "7.2.11-200.fc44.x86_64",
    "last_login": "2026-11-09T18:54:41.849350727Z",
    "last_seen": "2026-11-09T18:54:42Z",
    "local_flags": {
      "block_inbound": false,
      "block_lan_access": false,
      "disable_client_routes": false,
      "disable_dns": false,
      "disable_firewall": false,
      "disable_server_routes": false,
      "lazy_connection_enabled": false,
      "remote_jobs_allowed": false,
      "rosenpass_enabled": false,
      "rosenpass_permissive": false,
      "server_ssh_allowed": false
    },
    "login_expiration_enabled": false,
    "login_expired": false,
    "name": "device",
    "network_addresses": [
      {
        "mac": "1a:a1:60:8b:35:2e",
        "net_ip": "10.231.0.31/24"
      }
    ],
    "os": "Alpine Linux 3.24.3",
    "serial_number": "Default string",
    "ssh_enabled": false,
    "ui_version": "",
    "user_id": "",
    "version": "0.82.0"
  },
  {
    "accessible_peers_count": 0,
    "approval_required": false,
    "city_name": "",
    "connected": true,
    "connection_ip": "10.231.0.30",
    "country_code": "",
    "created_at": "2026-11-09T18:54:41.277488458Z",
    "dns_label": "edge.netbird.selfhosted",
    "ephemeral": false,
    "extra_dns_labels": [],
    "geoname_id": 0,
    "groups": [
      {
        "id": "dbp1d5vi9k7g00a1mffg",
        "name": "All",
        "peers_count": 2,
        "resources_count": 0
      },
      {
        "id": "dbp1dlvi9k7g00a1mfu0",
        "name": "edges",
        "peers_count": 1,
        "resources_count": 0
      }
    ],
    "hostname": "edge",
    "id": "dbp1dsfi9k7g00a1mg9g",
    "inactivity_expiration_enabled": false,
    "ip": "100.88.7.186",
    "ipv6": "fd60:f740:8b9c:b6bd:30d7:c880:e67d:80ad",
    "kernel_version": "7.2.11-200.fc44.x86_64",
    "last_login": "2026-11-09T18:54:41.277488458Z",
    "last_seen": "2026-11-09T18:54:41Z",
    "local_flags": {
      "block_inbound": false,
      "block_lan_access": false,
      "disable_client_routes": false,
      "disable_dns": false,
      "disable_firewall": false,
      "disable_server_routes": false,
      "lazy_connection_enabled": false,
      "remote_jobs_allowed": false,
      "rosenpass_enabled": false,
      "rosenpass_permissive": false,
      "server_ssh_allowed": false
    },
    "login_expiration_enabled": false,
    "login_expired": false,
    "name": "edge",
    "network_addresses": [
      {
        "mac": "b6:23:aa:96:f9:c9",
        "net_ip": "10.231.0.30/24"
      }
    ],
    "os": "Alpine Linux 3.24.3",
    "serial_number": "Default string",
    "ssh_enabled": false,
    "ui_version": "",
    "user_id": "",
    "version": "0.82.0"
  }
]
```

`GET /api/policies` -> **200**
```json
[
  {
    "description": "household devices reach the edges on TCP 443",
    "enabled": true,
    "id": "dbp1dm7i9k7g00a1mg10",
    "name": "household-to-edges-https",
    "rules": [
      {
        "action": "accept",
        "bidirectional": false,
        "description": "",
        "destinations": [
          {
            "id": "dbp1dlvi9k7g00a1mfu0",
            "name": "edges",
            "peers_count": 1,
            "resources_count": 0
          }
        ],
        "enabled": true,
        "id": "dbp1dm7i9k7g00a1mg10",
        "name": "https",
        "ports": [
          "443"
        ],
        "protocol": "tcp",
        "sources": [
          {
            "id": "dbp1dlvi9k7g00a1mfsg",
            "name": "household",
            "peers_count": 1,
            "resources_count": 0
          }
        ]
      }
    ],
    "source_posture_checks": null
  }
]
```

`GET /api/groups` -> **200**
```json
[
  {
    "id": "dbp1d5vi9k7g00a1mffg",
    "issued": "api",
    "name": "All",
    "peers": [
      {
        "id": "dbp1dsfi9k7g00a1mg9g",
        "name": "edge"
      },
      {
        "id": "dbp1dsfi9k7g00a1mge0",
        "name": "device"
      }
    ],
    "peers_count": 2,
    "resources": null,
    "resources_count": 0
  },
  {
    "id": "dbp1dlvi9k7g00a1mfsg",
    "issued": "api",
    "name": "household",
    "peers": [
      {
        "id": "dbp1dsfi9k7g00a1mge0",
        "name": "device"
      }
    ],
    "peers_count": 1,
    "resources": null,
    "resources_count": 0
  },
  {
    "id": "dbp1dlvi9k7g00a1mfu0",
    "issued": "api",
    "name": "edges",
    "peers": [
      {
        "id": "dbp1dsfi9k7g00a1mg9g",
        "name": "edge"
      }
    ],
    "peers_count": 1,
    "resources": null,
    "resources_count": 0
  }
]
```

`POST /api/reverse-proxies/services` -> **200**
request: `{"name":"photos","domain":"photos.home.test","mode":"tls","listen_port":9443,"enabled":true,"targets":[{"target_id":"dbp1dsfi9k7g00a1mg9g","target_type":"peer","protocol":"tcp","port":8444,"enabled":true,"options":{"proxy_protocol":true}}]}`
```json
{
  "auth": {},
  "domain": "photos.home.test",
  "enabled": true,
  "id": "dbp1fsfi9k7g00a1mi9g",
  "listen_port": 9443,
  "meta": {
    "created_at": "2026-11-09T18:58:57.792536056Z",
    "status": "pending"
  },
  "mode": "tls",
  "name": "photos",
  "pass_host_header": false,
  "port_auto_assigned": false,
  "private": false,
  "proxy_cluster": "proxy.hlspike.test",
  "rewrite_redirects": false,
  "targets": [
    {
      "access_action": "inherit",
      "enabled": true,
      "host": "100.88.7.186",
      "options": {
        "proxy_protocol": true
      },
      "port": 8444,
      "protocol": "tcp",
      "target_id": "dbp1dsfi9k7g00a1mg9g",
      "target_type": "peer"
    }
  ],
  "terminated": false
}
```

`POST /api/reverse-proxies/services` -> **409**
request: `{"name":"app2","domain":"app.home.test","mode":"tls","listen_port":9443,"enabled":true,"targets":[{"target_id":"dbp1dsfi9k7g00a1mg9g","target_type":"peer","protocol":"tcp","port":8444,"enabled":true}]}`
```json
{
  "message": "domain already taken",
  "code": 409
}
```

`POST /api/reverse-proxies/services` -> **200**
request: `{"name":"apex","domain":"home.test","mode":"tls","listen_port":443,"enabled":true,"targets":[{"target_id":"dbp1dsfi9k7g00a1mg9g","target_type":"peer","protocol":"tcp","port":8444,"enabled":true,"options":{"proxy_protocol":true}}]}`
```json
{
  "auth": {},
  "domain": "home.test",
  "enabled": true,
  "id": "dbp1fsni9k7g00a1mid0",
  "listen_port": 443,
  "meta": {
    "created_at": "2026-11-09T18:58:58.524051269Z",
    "status": "pending"
  },
  "mode": "tls",
  "name": "apex",
  "pass_host_header": false,
  "port_auto_assigned": false,
  "private": false,
  "proxy_cluster": "proxy.hlspike.test",
  "rewrite_redirects": false,
  "targets": [
    {
      "access_action": "inherit",
      "enabled": true,
      "host": "100.88.7.186",
      "options": {
        "proxy_protocol": true
      },
      "port": 8444,
      "protocol": "tcp",
      "target_id": "dbp1dsfi9k7g00a1mg9g",
      "target_type": "peer"
    }
  ],
  "terminated": false
}
```

`GET /api/reverse-proxies/services` -> **200**
```json
[
  {
    "auth": {},
    "domain": "app.home.test",
    "enabled": true,
    "id": "dbp1fgvi9k7g00a1mhn0",
    "listen_port": 9443,
    "meta": {
      "created_at": "2026-11-09T18:58:11.603950243Z",
      "status": "active"
    },
    "mode": "tls",
    "name": "app",
    "pass_host_header": false,
    "port_auto_assigned": false,
    "private": false,
    "proxy_cluster": "proxy.hlspike.test",
    "rewrite_redirects": false,
    "targets": [
      {
        "access_action": "inherit",
        "enabled": true,
        "host": "100.88.7.186",
        "options": {
          "proxy_protocol": true
        },
        "port": 8444,
        "protocol": "tcp",
        "target_id": "dbp1dsfi9k7g00a1mg9g",
        "target_type": "peer"
      }
    ],
    "terminated": false
  },
  {
    "auth": {},
    "domain": "photos.home.test",
    "enabled": true,
    "id": "dbp1fsfi9k7g00a1mi9g",
    "listen_port": 9443,
    "meta": {
      "created_at": "2026-11-09T18:58:57.792536056Z",
      "status": "active"
    },
    "mode": "tls",
    "name": "photos",
    "pass_host_header": false,
    "port_auto_assigned": false,
    "private": false,
    "proxy_cluster": "proxy.hlspike.test",
    "rewrite_redirects": false,
    "targets": [
      {
        "access_action": "inherit",
        "enabled": true,
        "host": "100.88.7.186",
        "options": {
          "proxy_protocol": true
        },
        "port": 8444,
        "protocol": "tcp",
        "target_id": "dbp1dsfi9k7g00a1mg9g",
        "target_type": "peer"
      }
    ],
    "terminated": false
  },
  {
    "auth": {},
    "domain": "home.test",
    "enabled": true,
    "id": "dbp1fsni9k7g00a1mid0",
    "listen_port": 443,
    "meta": {
      "created_at": "2026-11-09T18:58:58.524051269Z",
      "status": "active"
    },
    "mode": "tls",
    "name": "apex",
    "pass_host_header": false,
    "port_auto_assigned": false,
    "private": false,
    "proxy_cluster": "proxy.hlspike.test",
    "rewrite_redirects": false,
    "targets": [
      {
        "access_action": "inherit",
        "enabled": true,
        "host": "100.88.7.186",
        "options": {
          "proxy_protocol": true
        },
        "port": 8444,
        "protocol": "tcp",
        "target_id": "dbp1dsfi9k7g00a1mg9g",
        "target_type": "peer"
      }
    ],
    "terminated": false
  }
]
```

### Changing a service

`PUT /api/reverse-proxies/services/dbp1fsfi9k7g00a1mi9g` -> **200**
request: `{"name":"photos","domain":"photos.home.test","mode":"tls","listen_port":9443,"enabled":false,"targets":[{"target_id":"dbp1dsfi9k7g00a1mg9g","target_type":"peer","protocol":"tcp","port":8444,"enabled":true,"options":{"proxy_protocol":true}}]}`
```json
{
  "auth": {},
  "domain": "photos.home.test",
  "enabled": false,
  "id": "dbp1fsfi9k7g00a1mi9g",
  "listen_port": 9443,
  "meta": {
    "created_at": "2026-11-09T18:58:57.792536056Z",
    "status": "active"
  },
  "mode": "tls",
  "name": "photos",
  "pass_host_header": false,
  "port_auto_assigned": false,
  "private": false,
  "proxy_cluster": "proxy.hlspike.test",
  "rewrite_redirects": false,
  "targets": [
    {
      "access_action": "inherit",
      "enabled": true,
      "host": "100.88.7.186",
      "options": {
        "proxy_protocol": true
      },
      "port": 8444,
      "protocol": "tcp",
      "target_id": "dbp1dsfi9k7g00a1mg9g",
      "target_type": "peer"
    }
  ],
  "terminated": false
}
```

`GET /api/instance/version` -> **200**
```json
{
  "management_current_version": "0.82.0",
  "management_update_available": false
}
```

### People: an invite, accepted without a browser; blocking

`POST /api/users/invites` -> **200**
request: `{"email":"anna@home.test","name":"Anna","role":"user","auto_groups":["dbp1dlvi9k7g00a1mfsg"],"expires_in":3600}`
```json
{
  "auto_groups": [
    "dbp1dlvi9k7g00a1mfsg"
  ],
  "created_at": "2026-11-09T19:00:36.164333237Z",
  "email": "anna@home.test",
  "expired": false,
  "expires_at": "2026-11-09T20:00:36.156797668Z",
  "id": "dbp1gl7i9k7g00a1mipg",
  "invite_token": "<redacted>",
  "name": "Anna",
  "role": "user"
}
```

`GET /api/users/invites/nb?_<redacted>` -> **200** (no token)
```json
{
  "email": "anna@home.test",
  "expires_at": "2026-11-09T20:00:36.156797668Z",
  "invited_by": "",
  "name": "Anna",
  "valid": true
}
```

`POST /api/users/invites/nb?_<redacted>/accept` -> **200** (no token)
request: `{"password":"<redacted>"}`
```json
{
  "success": true
}
```

`POST /api/users/invites/nb?_<redacted>/accept` -> **404** (no token)
request: `{"password":"<redacted>"}`
```json
{
  "message": "user invite not found",
  "code": 404
}
```

`GET /api/users` -> **200**
```json
[
  {
    "auto_groups": [],
    "email": "owner@home.test",
    "id": "CiQxNzcxMTE1Yi0wM2Q5LTQ4MmMtODRmZC1iODY1MWRiMWEzYjcSBWxvY2Fs",
    "idp_id": "local",
    "is_blocked": false,
    "is_current": false,
    "is_service_user": false,
    "issued": "api",
    "last_login": "0001-01-01T00:00:00Z",
    "name": "Spike Owner",
    "pending_approval": false,
    "role": "owner",
    "status": "active"
  },
  {
    "auto_groups": [],
    "email": "",
    "id": "b55e57a1-059a-4ef6-a817-2e41aa3f3148",
    "is_blocked": false,
    "is_current": true,
    "is_service_user": true,
    "issued": "api",
    "last_login": "0001-01-01T00:00:00Z",
    "name": "homerslab",
    "pending_approval": false,
    "role": "admin",
    "status": "active"
  },
  {
    "auto_groups": [
      "dbp1dlvi9k7g00a1mfsg"
    ],
    "email": "anna@home.test",
    "id": "CiQxMTkyYmQ1MC04MDE4LTQxMTUtYTMzMy0yNzRkZDdkNzdiZGYSBWxvY2Fs",
    "idp_id": "local",
    "is_blocked": false,
    "is_current": false,
    "is_service_user": false,
    "issued": "api",
    "last_login": "0001-01-01T00:00:00Z",
    "name": "Anna",
    "pending_approval": false,
    "role": "user",
    "status": "active"
  }
]
```

`PUT /api/users/CiQxMTkyYmQ1MC04MDE4LTQxMTUtYTMzMy0yNzRkZDdkNzdiZGYSBWxvY2Fs` -> **200**
request: `{"role":"user","auto_groups":["dbp1dlvi9k7g00a1mfsg"],"is_blocked":true}`
```json
{
  "auto_groups": [
    "dbp1dlvi9k7g00a1mfsg"
  ],
  "email": "anna@home.test",
  "id": "CiQxMTkyYmQ1MC04MDE4LTQxMTUtYTMzMy0yNzRkZDdkNzdiZGYSBWxvY2Fs",
  "idp_id": "local",
  "is_blocked": true,
  "is_current": false,
  "is_service_user": false,
  "issued": "api",
  "last_login": "0001-01-01T00:00:00Z",
  "name": "Anna",
  "pending_approval": false,
  "role": "user",
  "status": "blocked"
}
```

### Revoking a device

`DELETE /api/peers/dbp1dsfi9k7g00a1mge0` -> **200**
```json
{}
```

`GET /api/peers` -> **200**
```json
[
  {
    "accessible_peers_count": 0,
    "approval_required": false,
    "city_name": "",
    "connected": true,
    "connection_ip": "10.231.0.30",
    "country_code": "",
    "created_at": "2026-11-09T18:54:41.277488458Z",
    "dns_label": "edge.netbird.selfhosted",
    "ephemeral": false,
    "extra_dns_labels": [],
    "geoname_id": 0,
    "groups": [
      {
        "id": "dbp1d5vi9k7g00a1mffg",
        "name": "All",
        "peers_count": 1,
        "resources_count": 0
      },
      {
        "id": "dbp1dlvi9k7g00a1mfu0",
        "name": "edges",
        "peers_count": 1,
        "resources_count": 0
      }
    ],
    "hostname": "edge",
    "id": "dbp1dsfi9k7g00a1mg9g",
    "inactivity_expiration_enabled": false,
    "ip": "100.88.7.186",
    "ipv6": "fd60:f740:8b9c:b6bd:30d7:c880:e67d:80ad",
    "kernel_version": "7.2.11-200.fc44.x86_64",
    "last_login": "2026-11-09T18:54:41.277488458Z",
    "last_seen": "2026-11-09T18:54:41Z",
    "local_flags": {
      "block_inbound": false,
      "block_lan_access": false,
      "disable_client_routes": false,
      "disable_dns": false,
      "disable_firewall": false,
      "disable_server_routes": false,
      "lazy_connection_enabled": false,
      "remote_jobs_allowed": false,
      "rosenpass_enabled": false,
      "rosenpass_permissive": false,
      "server_ssh_allowed": false
    },
    "login_expiration_enabled": false,
    "login_expired": false,
    "name": "edge",
    "network_addresses": [
      {
        "mac": "b6:23:aa:96:f9:c9",
        "net_ip": "10.231.0.30/24"
      }
    ],
    "os": "Alpine Linux 3.24.3",
    "serial_number": "Default string",
    "ssh_enabled": false,
    "ui_version": "",
    "user_id": "",
    "version": "0.82.0"
  }
]
```

`PUT /api/setup-keys/dbp1dovi9k7g00a1mg3g` -> **200**
request: `{"revoked":true,"auto_groups":["dbp1dlvi9k7g00a1mfu0"]}`
```json
{
  "allow_extra_dns_labels": false,
  "auto_groups": [
    "dbp1dlvi9k7g00a1mfu0"
  ],
  "ephemeral": false,
  "expires": "2026-11-10T18:54:27.904665224Z",
  "id": "dbp1dovi9k7g00a1mg3g",
  "key": "<redacted>",
  "last_used": "2026-11-09T18:54:41.279290111Z",
  "name": "homerslab-nodes",
  "revoked": true,
  "state": "revoked",
  "type": "reusable",
  "updated_at": "2026-11-09T19:00:57.782407286Z",
  "usage_limit": 0,
  "used_times": 1,
  "valid": false
}
```

### Teardown: what has to go first

`DELETE /api/groups/dbp1dlvi9k7g00a1mfu0` -> **400**
```json
{
  "message": "group has been linked to policy: household-to-edges-https",
  "code": 400
}
```

`DELETE /api/reverse-proxies/domains/dbp1fbni9k7g00a1mhg0` -> **412**
```json
{
  "message": "delete domain from store: custom domain has dependent services; delete or move them before deleting the domain",
  "code": 412
}
```

### Teardown: services, domains

`DELETE /api/reverse-proxies/services/dbp1fgvi9k7g00a1mhn0` -> **200**
```json
{}
```

`DELETE /api/reverse-proxies/services/dbp1fsfi9k7g00a1mi9g` -> **200**
```json
{}
```

`DELETE /api/reverse-proxies/services/dbp1fsni9k7g00a1mid0` -> **200**
```json
{}
```

`GET /api/reverse-proxies/services` -> **200**
```json
[]
```

`DELETE /api/reverse-proxies/domains/dbp1fbni9k7g00a1mhg0` -> **204**
```json

```

`DELETE /api/reverse-proxies/domains/dbp1fbvi9k7g00a1mhh0` -> **204**
```json

```

`GET /api/reverse-proxies/domains` -> **200**
```json
[
  {
    "domain": "proxy.hlspike.test",
    "id": "",
    "require_subdomain": false,
    "supports_crowdsec": false,
    "supports_custom_ports": true,
    "supports_private": false,
    "type": "free",
    "validated": true
  }
]
```

### Teardown: the proxy's token and cluster

`DELETE /api/reverse-proxies/proxy-tokens/dbp1f3vi9k7g00a1mhag` -> **200**
```json
{}
```

`GET /api/reverse-proxies/clusters` -> **200**
```json
[
  {
    "address": "proxy.hlspike.test",
    "connected_proxies": 1,
    "id": "netbird-proxy-75fd8fff-687e-47ad-9bdf-98c0b5a42fa4",
    "online": true,
    "private": false,
    "require_subdomain": false,
    "supports_crowdsec": false,
    "supports_custom_ports": true,
    "type": "account"
  }
]
```

`DELETE /api/reverse-proxies/clusters/proxy.hlspike.test` -> **200**
```json
{}
```

`GET /api/reverse-proxies/clusters` -> **200**
```json
[]
```

`GET /api/reverse-proxies/clusters` -> **200**
```json
[
  {
    "address": "proxy.hlspike.test",
    "connected_proxies": 0,
    "id": "netbird-proxy-75fd8fff-687e-47ad-9bdf-98c0b5a42fa4",
    "online": false,
    "type": "account"
  }
]
```

`GET /api/reverse-proxies/clusters` -> **200**
```json
[
  {
    "address": "proxy.hlspike.test",
    "connected_proxies": 0,
    "id": "netbird-proxy-75fd8fff-687e-47ad-9bdf-98c0b5a42fa4",
    "online": false,
    "type": "account"
  }
]
```

`DELETE /api/reverse-proxies/clusters/proxy.hlspike.test` -> **200**
```json
{}
```

`GET /api/reverse-proxies/clusters` -> **200**
```json
[]
```

`GET /api/reverse-proxies/proxy-tokens` -> **200**
```json
[
  {
    "created_at": "2026-11-09T18:57:19.018047036Z",
    "id": "dbp1f3vi9k7g00a1mhag",
    "last_used": "2026-11-09T19:05:29.968890089Z",
    "name": "homerslab-proxy",
    "revoked": true
  },
  {
    "created_at": "2026-11-09T18:57:19.205436118Z",
    "expires_at": "2026-11-09T19:57:19.205436118Z",
    "id": "dbp1f3vi9k7g00a1mhbg",
    "name": "short",
    "revoked": true
  }
]
```

### Teardown: DNS, peers, keys, policy, groups, people

`DELETE /api/dns/zones/dbp1ejfi9k7g00a1mgp0` -> **200**
```json
{}
```

`GET /api/dns/zones` -> **200**
```json
[]
```

`DELETE /api/peers/dbp1dsfi9k7g00a1mg9g` -> **200**
```json
{}
```

`GET /api/peers` -> **200**
```json
[]
```

`DELETE /api/setup-keys/dbp1dovi9k7g00a1mg3g` -> **200**
```json
{}
```

`DELETE /api/setup-keys/dbp1dp7i9k7g00a1mg4g` -> **200**
```json
{}
```

`GET /api/setup-keys` -> **200**
```json
[]
```

`DELETE /api/policies/dbp1dm7i9k7g00a1mg10` -> **200**
```json
{}
```

`DELETE /api/groups/dbp1dlvi9k7g00a1mfsg` -> **400**
```json
{
  "message": "group has been linked to user: CiQxMTkyYmQ1MC04MDE4LTQxMTUtYTMzMy0yNzRkZDdkNzdiZGYSBWxvY2Fs",
  "code": 400
}
```

`DELETE /api/groups/dbp1dlvi9k7g00a1mfu0` -> **200**
```json
{}
```

`GET /api/groups` -> **200**
```json
[
  {
    "id": "dbp1d5vi9k7g00a1mffg",
    "issued": "api",
    "name": "All",
    "peers": null,
    "peers_count": 0,
    "resources": null,
    "resources_count": 0
  },
  {
    "id": "dbp1dlvi9k7g00a1mfsg",
    "issued": "api",
    "name": "household",
    "peers": null,
    "peers_count": 0,
    "resources": null,
    "resources_count": 0
  }
]
```

`GET /api/policies` -> **200**
```json
[]
```

`DELETE /api/users/CiQxMTkyYmQ1MC04MDE4LTQxMTUtYTMzMy0yNzRkZDdkNzdiZGYSBWxvY2Fs` -> **200**
```json
{}
```

`GET /api/users/invites` -> **200**
```json
[]
```

`DELETE /api/groups/dbp1dlvi9k7g00a1mfsg` -> **200**
```json
{}
```

`GET /api/groups` -> **200**
```json
[
  {
    "id": "dbp1d5vi9k7g00a1mffg",
    "issued": "api",
    "name": "All",
    "peers": null,
    "peers_count": 0,
    "resources": null,
    "resources_count": 0
  }
]
```

### Teardown: the service user and the setup token

`DELETE /api/users/b55e57a1-059a-4ef6-a817-2e41aa3f3148` -> **200**
```json
{}
```

`GET /api/groups` -> **404**
```json
{
  "message": "invalid token: pat: <hash redacted> redacted> not found",
  "code": 404
}
```

`GET /api/users/CiQxNzcxMTE1Yi0wM2Q5LTQ4MmMtODRmZC1iODY1MWRiMWEzYjcSBWxvY2Fs/tokens` -> **200**
```json
[
  {
    "created_at": "2026-11-09T18:53:12.022524991Z",
    "created_by": "CiQxNzcxMTE1Yi0wM2Q5LTQ4MmMtODRmZC1iODY1MWRiMWEzYjcSBWxvY2Fs",
    "expiration_date": "2026-11-10T18:53:12.022524991Z",
    "id": "dbp1d67i9k7g00a1mfgg",
    "last_used": "2026-11-09T19:07:39.758585695Z",
    "name": "setup-token"
  }
]
```

`DELETE /api/users/CiQxNzcxMTE1Yi0wM2Q5LTQ4MmMtODRmZC1iODY1MWRiMWEzYjcSBWxvY2Fs/tokens/dbp1d67i9k7g00a1mfgg` -> **200**
```json
{}
```

`GET /api/groups` -> **404**
```json
{
  "message": "invalid token: pat: <hash redacted> redacted> not found",
  "code": 404
}
```

`GET /api/instance` -> **200** (no token)
```json
{
  "setup_required": false
}
```
