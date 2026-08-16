# What the console still needs from the API

Recorded by the console (AI #3) during the F1–F10 safety pass on 2026-08-16.

This file exists for the same reason `docs/frontend-backend-requirements.md`
does, and follows the same rule: **no screen in this console calls an endpoint
that is not in `router.go`, derives a value from an unrelated one to stand in for
a missing field, or presents a default as though it had been read back.** Where a
screen has a hole, it says so in words an operator can read, and the hole is
recorded here.

It is kept in `web/` rather than merged into `docs/frontend-backend-requirements.md`
because that file had uncommitted changes from another agent while this pass was
running. **It should be folded into that document** — the two overlap by design
and only one of them should survive.

---

## Contracts this pass consumed

Everything below already exists and is now used. Listed so that a change to any
of them is known to break a screen.

| Contract | Used by | Note |
|---|---|---|
| `PUT /console/firmware/{id}/current` + the heartbeat's `firmware_update` offer | Firmware catalogue | The console mirrors `database/firmware_offer.go`'s deliverability rules in `pages/firmware/offerability.ts` so an operator learns a build cannot be sent *before* promoting it. **If those rules change, that file has to change with them.** |
| `POST /console/sites/{site_id}/claim-codes` | Site detail → "Provision a terminal" | Consumed exactly as returned, including `shown_once` and `superseded_codes`. The code is never cached, stored or logged. |
| `offline_policy` / `offline_grace_minutes` on `ConsoleSite` and on `ConsoleSiteUpdateRequest` | Site list, site detail, terminal detail | Landed mid-pass. Before it, the panel could not show what was in force and said so; it now shows the real value. |
| `RejectReservedSettingsKeys` (400, `RESERVED_SETTINGS_KEY`) | Site settings panel | The console strips a stale `offline_policy` / `offline_grace_minutes` from a guided save and refuses one typed into the raw editor. Without the strip, every save at a site carrying a legacy settings object would 400. |
| `models.MaxOfflineGraceMinutes` = 43,200 | Offline policy panel | The console previously enforced 10,080, which was its own invention and refused configurations the platform accepts. |
| `recovery` on the revoke response | Revoke dialog | Appended verbatim rather than paraphrased. |

---

## Still open, in the order they cost the most

### 1. `api_key_prefix` is absent from every site read — CON-03 is marked FIXED and is not

**Endpoints** `GET /console/sites`, `GET /console/sites/{id}`

`sites.api_key_prefix` exists, `database.SiteKeyPrefix` reads it, and the create
and rotate responses carry it. But `models.ConsoleSite` has no field for it and
`consoleSiteColumns` (`database/console.go`) does not select it — so **no read
returns one**, and the console can only display a prefix during the session that
minted the key.

`console_sites_test.go` asserts the prefix on the CREATE response and in a raw
SQL read, which is not what the finding describes. `docs/market-readiness.md`
records CON-03 as `FIXED`; against the projection it is not. **The register is
AI #1's and has not been edited here.**

The console types the field optional and populates it from create/rotate only, so
this is a one-line backend change with nothing to alter in the frontend. The site
detail card now distinguishes the two facts it used to run together as "not
shown": the *key* is unrecoverable by design, and the *prefix* is simply not
returned.

### 2. No console route serves terminal health

**Missing** a route mounting `database.GetTerminalHealth` → `models.ConsoleTerminalHealth`

Both the store function and the response model exist and are fully written —
`pending_jobs`, `failed_jobs`, `last_apply_error`, `last_apply_error_at`,
`credential_active`, `offline_policy`, `offline_grace_minutes` — and **nothing in
`router.go` mounts them.** They are dead code today.

Consequences in the console, each of them a screen that cannot say something
useful:

- **Backlog and apply failures are invisible.** SYN-04 is recorded as `PARTIAL`
  on the strength of these counters existing, but an operator cannot see them. A
  terminal quietly failing to apply a roster looks identical to a healthy one.
- **`credential_active` cannot be shown.** It is derived from the key hash being
  present, which is what authentication actually checks — the one value that
  would let the console agree with the door about whether a terminal can still
  authenticate.
- The terminal page reads the **site** for the offline policy instead. That is
  correct and not a workaround (the site is the authority), so this is not a
  blocker for F2.

### 3. `TerminalDetail.roster_size` / `over_capacity` are returned and unused

Landed mid-pass and not consumed here — it is TM-01's work, not F1–F10's. Worth
recording so it is not mistaken for a gap: the API is ahead of the console on
this one, which is the right direction.

### 4. `external_id` validation is enforced server-side and not mirrored

**Endpoint** `POST/PUT /console/people`

At most 31 characters, printable ASCII, no spaces, with `field` on the 400. The
person form does not validate it yet, so an integrator pasting a UUID — the first
thing anyone reaches for — gets a server rejection instead of a hint while the
value can still be changed. **Out of scope for this pass and not fixed.**

---

## Things that would be wrong to add

Recorded because each has been suggested and each would break the model.

- **An `is_operational` field on the applications catalogue.** Implementation
  status is a property of the BUILD, not of a company's configuration.
  `applications/readiness.ts` hard-codes it deliberately and says so.
- **A GET that returns a claim code.** The server stores only its SHA-256. A read
  endpoint would mean storing the plaintext, which is the whole thing the design
  avoids.
- **`offline_policy` accepted in the free-form settings object.** It is refused
  now, and that refusal is what stops an operator bypassing a validated safety
  control by writing raw JSON.
