# HomersLab

HomersLab installs and looks after a household's self-hosted apps on the household's own machines. These are the
words the product, its docs and its code use.

## Language

### The household

**Household**:
The people and the machines one HomersLab looks after.
_Avoid_: instance, tenant, homelab, family (as a system term)

**Person**:
Someone in the household who has a household account.
_Avoid_: user, member, account

**Role**:
What a person may do: **admin**, **adult** or **kid**. A guest has none.
_Avoid_: group, permission level

**Household admin**:
A person with the admin role, who installs apps and adds nodes and people.
_Avoid_: operator, owner, superuser

**Household account**:
A person's single sign-in for every app and for HomersLab itself.
_Avoid_: SSO account, IdP account, login

**Audience**:
The people an app is shown to and may be opened by.
_Avoid_: visibility, ACL, access list

**Kid view**:
The kid-safe mode of an app, which kids get without anyone switching it on.
_Avoid_: parental mode, child profile

**Invite**:
How a new person joins: an account, a role, and guidance for getting their devices onto the mesh.
_Avoid_: registration, onboarding link

**Guest**:
A person invited until an end date, with no role, who sees only the apps named for them and leaves the household on
that date.
_Avoid_: visitor, temporary account, guest role

### Apps and catalogs

**App**:
Something people open and use, such as photos, documents or films, installed from a catalog. A household may have
several apps of one kind.
_Avoid_: component, service, package, stack

**Host app**:
An app installed from its maker's own package for the machine's operating system, instead of in containers.
_Avoid_: native app, system app

**App definition**:
What a catalog holds for one app: what it is, what it needs, and how it is wired into the household.
_Avoid_: template, recipe, package, manifest (that is one part of it)

**Catalog**:
A signed collection of app definitions that a household trusts.
_Avoid_: market, marketplace (the Store is where catalogs are browsed)

**Store**:
The place where household admins browse the catalogs they trust and get apps.
_Avoid_: App Store, marketplace, shop

**App request**:
A person's ask for an app the household doesn't have, which an admin approves or turns down.
_Avoid_: wish, suggestion, ticket

**Feedstock**:
HomersLab's recipe for building one app from its upstream source, with the few patches it needs to fit the household's
sign-in.
_Avoid_: fork, mirror, build repo

**Official catalog**:
The catalog HomersLab itself publishes.

**Third-party catalog**:
A catalog someone else publishes, which a household admin trusts by its publisher's key.
_Avoid_: community store

**Floor**:
The security requirements every app meets before it can be installed, checked by HomersLab itself.
_Avoid_: baseline, minimum, policy

**Tier**:
How deeply an app is integrated. **Verified** apps have native sign-in, tested restores and updates, and
hardening; **Community** apps meet the floor.
_Avoid_: level, grade, badge

**Extra**:
A permission beyond the floor, such as a device or the machine's own network. An app declares it and a household
admin approves it at install.
_Avoid_: privilege, capability, exception

### Lifecycle

**Install plan**:
What an install, update or removal will do, in plain words, shown before anything happens.
_Avoid_: preview, diff, dry run

**Install**:
Bringing an app into the household, wired: sign-in, name, audience, backups and health.
_Avoid_: enable, switch on, deploy

**Update**:
Moving an app to a newer tested version, with a snapshot taken first.
_Avoid_: upgrade, bump

**Snapshot**:
The copy of an app taken right before an update, kept for a rollback.
_Avoid_: checkpoint

**Rollback**:
Returning an app to its snapshot when it fails its health check after an update.
_Avoid_: revert, downgrade

**Remove**:
Stopping an app and taking away its sign-in, names and place on Home, while keeping its data and backups.
_Avoid_: uninstall, disable, switch off

**Delete data**:
Destroying a removed app's data, a separate action confirmed by typing the app's name.
_Avoid_: purge, wipe

**Eject**:
Writing an app out as a standalone setup with its data, settings and secrets, so it keeps running without
HomersLab.
_Avoid_: export, detach, leave

### Machines and reach

**Node**:
A machine in the household that runs apps.
_Avoid_: server, host, worker, box

**Primary**:
The node that runs HomersLab's manager and the household's sign-in.
_Avoid_: master, controller, main node

**Placement**:
Which node an app runs on.
_Avoid_: scheduling, assignment

**Join code**:
The one-time code that makes a machine a node.
_Avoid_: token, enrolment key

**Storage location**:
A shared folder that apps may use, on a node or on a network share.
_Avoid_: volume, mount, share

**Mirror**:
Two or more disks that each hold a full copy, so one can fail without a restore. A mirror is not a backup.
_Avoid_: RAID, redundancy (as a noun)

**Mesh**:
The private network that joins the household's nodes and devices, at home or away.
_Avoid_: VPN, tailnet, overlay

**Device**:
A person's phone, tablet or computer on the mesh.
_Avoid_: peer, client, endpoint

**Edge**:
A node's front door: it ends encryption and asks for the household account before an app sees a request.
_Avoid_: proxy, reverse proxy, ingress, gateway

**Exposure**:
Whether an app is reachable only over the mesh (the default) or is also **public**.
_Avoid_: visibility, publishing

**Main domain**:
The household's domain where people sign in; other domains of the household serve the same apps.
_Avoid_: primary domain (the primary is a node), root domain

**Public path**:
The way a public app is reached from the internet without opening the household's router.
_Avoid_: tunnel, port forward

### Sign-in

**Native sign-in**:
An app that takes the household account itself, so people never see a second login.
_Avoid_: SSO app, integrated login

**Guarded app**:
An app the edge signs people in for, because the app can't take the household account itself.
_Avoid_: proxied app, protected app

**Step-up**:
Asking for a person's passkey again, right before a risky change.
_Avoid_: re-authentication, sudo mode, elevation

**Risky change**:
A change that needs step-up: deleting data, nodes, roles, sign-in settings, anything public, extras, restoring an app,
eject, trusting a catalog, making a join code, replacing a mesh token, making a new recovery kit, the archive's key,
adding, removing or changing a backup target, adding or removing a domain, replacing a DNS token, changing the mail
account, and making an API or MCP token. Sign-in settings are the identity provider, self sign-up and its domains,
invite lifetimes, and the passkey rules.
_Avoid_: dangerous action, admin action

### Keeping watch

**Heartbeat**:
The regular sign of life the primary sends to a check outside the household, which alerts when it stops.
_Avoid_: ping (the act), monitor

**Incident**:
One stretch of something not working, from the first failed check to "resolved", with what fixed it.
_Avoid_: outage (for small ones), event, alert (one incident may send several)

**Maintenance window**:
The nightly hours when planned work (updates, scrubs, restore tests, moves) runs.
_Avoid_: downtime window, schedule

### Keeping data

**Backup**:
The frequent, deduplicated, encrypted copy of every app's data.
_Avoid_: snapshot (that is for updates), sync

**Archive**:
The copy of the small, sensitive things, made after every change to them and at least weekly, which nobody on the
household's machines can read.
_Avoid_: cold backup, export

**Restore test**:
The weekly proof that an app's latest backup restores and works.
_Avoid_: backup check, verification

**Recovery kit**:
What the household admin keeps away from the machines, enough to restore everything even without HomersLab.
_Avoid_: emergency kit, recovery codes

### Surfaces and history

**Home**:
Where people open their apps.
_Avoid_: dashboard, launcher, portal, start page

**Admin**:
Where household admins manage apps, nodes, people and backups.
_Avoid_: console, control panel, dashboard

**First-time setup**:
The one guided sitting that turns a fresh machine into a working household.
_Avoid_: onboarding, wizard, bootstrap

**Classic**:
HomersLab's predecessor, one household's homelab.
_Avoid_: v1, legacy, the old homelab

**Importer**:
What moves a classic household into HomersLab.
_Avoid_: migrator
