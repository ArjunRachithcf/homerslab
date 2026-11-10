# HomersLab prototypes: handoff

For the engineers who build HomersLab from these designs. The prototypes are clickable, throwaway HTML: they decide
how things look, read and move, not how they are built. Everything that is a decision is in `docs/spec.md` and the
ADRs; this file says which screens exist, where each lives, what the design system offers, and what is prototype-only.

Open `prototypes/index.html` (the hub) in a browser. Every page also works straight from the disk (`file://`).

## The folders

| Folder | What it is |
|---|---|
| `design/` | The design system: tokens, components, icons, motion, charts, illustrations, the gallery (`design/index.html`). The source of truth. |
| `prototypes/design/` | A copy of `design/`, so each prototype works from its own folder. Never edit it; edit `design/` and copy. |
| `prototypes/*.html` | Four pages: `index.html` (the hub), `setup.html` (first-time setup), `admin.html` (Admin), `home.html` (Home). |
| `prototypes/assets/` | The prototypes' own code and styles (see below). |
| `prototypes/icons/apps/` | App icons from selfh.st/icons (CC BY 4.0, see its ATTRIBUTION.md). |
| `prototypes/screenshots/final/` | The reviewed screenshots: `NN-name-WIDTH-THEME.png`; `gc-` and `gf-` are the Clear and Frosted glass levels. |
| `prototypes/screenshots/motion/` | Short webm clips of every motion that matters: setup, the Store to an install, a request answered and installed, the glass level in Admin and on Home, sheets and the step-up, an app tile to its page, the disk analyzer (delete and restore), replacing a failed disk. |
| `prototypes/screenshots/inspect/` | The design inspector's last run, one folder per screen. |

## The design system (`design/`)

| File | Offers |
|---|---|
| `tokens.css` | Colour (light and dark, Apple's neutrals, the teal accent, status, pills), type (system font, Inter as fallback), space and radii, materials (glass in four weights, solids, rim, sheen, depth), the wallpaper, springs and durations, the **glass level** (`--glass`, `data-glass="clear|balanced|frosted|solid"`), data tones (`--viz-1…6` with `--viz-ink`, `--day-*`). |
| `components.css` | Buttons, inset grouped lists and rows, widgets and cards, bars, the floating sidebar and tab bar, sheets and the step-up, the install plan's pieces, status and pills, app tiles and rows, forms, toasts, progress and steps, people (avatars), the setup assistant, the Store (hero, shelves, items, Get buttons, app page, requests), disks (usage bar, disk members, bays), the treemap, uptime days and sparklines, the analyzer's session ring, notifications, DNS ownership marks. |
| `icons.js` | One 16px outline set, `I(name, cls)`, plus the mark and lockup. |
| `motion.js` | `HL.run` (screen changes as View Transitions: push, pop, fade), `HL.list` (rows glide when a list changes), `HL.morph` (an icon flies to its page), `HL.lens` (the tab bar's lens), `HL.glass` / `HL.glassControl` / `HL.glassSync` (the glass level), `HL.still()` (reduced motion). |
| `charts.js` | `HL.treemap`, `HL.usage`, `HL.days`, `HL.spark`, `HL.size`: plain HTML/SVG strings, no library. |
| `illos.js` | The setup assistant's illustrations. |
| `index.html`, `gallery.*` | The gallery: every component with "when to use", in both themes, at every glass level. |

Rules the system already encodes, and the checks hold:

- **Contrast.** Every word meets WCAG AA against the pixels behind it, glass and all, in both themes and at every
  glass level. Below Balanced, text on glass gets stronger automatically (`--ink-lift`). Reduced transparency and
  Solid swap every material for its solid.
- **Motion.** Springs only in answer to a click; reduced motion leaves cross-fades. Admin never moves by itself.
- **Admin is dark by default**; Home follows the device. Each person sets the glass level for themselves.

## The screens

### Home (`home.html`; logic in `assets/home.js`, styles in `assets/home.css`)

| Screen | How to reach it |
|---|---|
| An adult's apps in panels; a kid's tiles; away from the mesh; an app not working; updating in the maintenance window | `home.html`, the prototype bar's State: who is looking, where, app states, window |
| An admin's Home: "needs a look", new requests, a failed disk | `home.html?as=ines` (add `&disk=failed`) |
| The person's menu: appearance, glass, hidden apps, ask for an app | the name button, top right |
| **Hide one of two same-purpose apps** (Jellyfin/Plex), on this person's Home only; "Show it" on the panel and in the menu | "Hide one" under the panel |
| Ask for an app: the member Store, an app page, Can't find it?, Your requests, answers | `home.html#/ask`, `#/ask/app/<id>`, `#/ask/requests` |

### Admin (`admin.html`; routes in `assets/admin-*.js`, styles in `assets/admin.css`)

| Area | Routes | File |
|---|---|---|
| Apps, an app's page (audience, exposure, backups, updates, health, **how it's built** for a feedstock app, danger zone), a removed app | `#/apps`, `#/apps/<id>`, `#/apps/mealie` | `admin-apps.js` |
| **Move an app's data** (plan, progress, 7 days to roll back) | `#/apps/<id>/move` | `admin-disks.js` |
| The Store: Discover, Updates, Requests, browse, an app's page, Get → the install plan, the install | `#/store…`, `#/plans/<id>`, `#/install/<id>` | `admin-catalog.js`, shared pieces in `store.js` |
| People, a person, invite | `#/people…` | `admin-people.js` |
| Nodes (Machines, **Disks**, **Uptime** as segments), a node, add a node | `#/nodes`, `#/nodes/disks`, `#/nodes/uptime`, `#/nodes/<id>`, `#/nodes/add` | `admin-nodes.js`, `admin-disks.js`, `admin-uptime.js` |
| **A disk**: health, use, growth, scrub, encryption, mirror, apps on it; **adopt** | `#/disks/<id>` | `admin-disks.js` |
| **Prepare a disk** (plan, passkey, progress); **erase** (serial typed + passkey); **replace a failed disk** (which to pull, rebuild) | `#/disks/sp-new/prepare`, `#/disks/sp-usb/erase`, `#/disks/an-apps/replace` | `admin-disks.js` |
| **Encrypt the system disk** (preconditions, legacy BIOS) | `#/nodes/study-pc/encrypt` | `admin-disks.js` |
| **The disk analyzer** (passkey, 10-minute session, space map, sorted lists, biggest, oldest, duplicates, per person, recently deleted) | `#/analyzer?disk=<id>&view=…` | `admin-analyzer.js` |
| Backups, restore | `#/backups…` | `admin-backups.js` |
| Settings: appearance and glass, the household, **alerts and notifications**, **domains** (add, rename), sign-in, meshes, recovery kit, assistants | `#/settings`, `#/settings/alerts`, `#/settings/domains`, `#/settings/domains/add`, `#/settings/domains/rename` | `admin-settings.js`, `admin-domains.js` |

Prototype states for review are in the prototype bar ("State"): the meshes, how the next install ends, Bay 1's disk
(SMART warnings or failed), the media pool (91% or 96%), the maintenance window, Memos (patched or guarded fallback),
the glass level, motion and transparency.

### First-time setup (`setup.html`; `assets/setup.js`, `assets/setup.css`)

Eleven steps as a setup assistant (welcome to recovery kit, then join, passkey, done). `?resume=yes|end` jumps ahead.

## What is prototype-only

- **The data is invented**: the Ferreira household, its people, devices, nodes, disks, incidents, domains and
  requests (`assets/data.js`, `assets/data-ops.js`). Asserts at the bottom of each keep the figures consistent; read
  the console.
- **State lives in memory**, per page load. Two things cross pages, in the tab's **sessionStorage**: app requests and
  what was installed for them (`store.js`), so an answer given in Admin reaches the requester's Home. Per-browser
  conveniences use **localStorage**: Admin's appearance and glass, each person's glass and hidden apps on Home.
  In the product all of these are server-side, per person.
- **Passkeys are simulated**: `UI.stepUp` shows what is approved and a fake system prompt. Plan ids are hashes of the
  plan's content in the browser.
- **Nothing is fetched or sent**: no network calls, no real DNS, SMART, btrfs or TPM. Progress is timed.
- **The prototype bar** (`assets/proto.js`) exists only for review.
- Not built: the desktop family app, the importer, the MCP assistant's screens beyond Settings.

## Checks the prototypes pass

axe (WCAG 2.2 AA) at 1440, 768 and 390 in both themes, at the glass levels and with reduced transparency and motion;
a pixel-contrast check of every word against the rendered pixels; the design inspector (0 FAIL); the lint; every page
opened from disk with no console errors; flow tests for the Store, requests, the glass level, preparing a disk, the
analyzer's delete and restore, replacing a failed disk, adding a domain and hiding an app on Home.
