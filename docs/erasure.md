# Erasure: what deleting a member or an operator removes

Migration 039 and `database/deletion.go`. This is the operational reference.
The reasoning lives in the code comments.

## A member

Everything below happens **in the delete's own transaction**:

| Data | What happens |
|---|---|
| Name, email, phone, category, validity, locator | Removed from the person row. The member number stays until finalisation. |
| Sealed template material (026) | Destroyed (`destroySealedMaterialTx`). |
| Permissions, enrolment requests | Deleted. |
| Person sync jobs | **Kept**, every row, with the payload stripped to the member number. They are the only record of which terminals were sent the person: the roster reconciler sends a DELETE from them when a paused or disabled terminal rejoins. Undelivered upserts are cancelled; an undelivered DELETE is kept. |
| DELETE sync job | Queued to every terminal. Payload is the member number only. |
| Door history (`events`, `access_logs`) | Kept, but anonymised: person, credential and member number are removed (`anonymize_person_history`). |
| Audit trail | Kept. The member's label becomes `deleted-person:<public id>` and their personal fields leave `changes` (`redact_person_audit`). |
| Assistant conversations and tool records | Conversations naming the member are deleted, and tool and confirmation arguments naming them are redacted. Matching is by whole token. Best effort: paraphrases are not found. |
| Ledger | An HMAC of (company, member number) is recorded in `deleted_subjects`, dated by the **actual** deletion (the original date for people deleted before 039). |

**A reused member number never reaches a live member.** A number can pass to
somebody new once its holder is deleted. Rows linked to the person, by id,
credential or public id, are theirs. Rows that only name the number are theirs
only if written inside the person's own lifetime, `created_at` to `deleted_at`.
That window is widened by a minute and clamped so it never reaches into another
holder's lifetime (`personLifetimeTx`). The same rule applies to an operator's
address (`operatorLifetimeTx`).

**Finalisation** (maintenance task `erasure`, hourly) deletes the row once
every live terminal has let go. A terminal has let go once it has acknowledged
a DELETE newer than the last thing it was sent about the person. Until then
the person is held by any of:

* a placement that is not REMOVED or FAILED;
* an unacknowledged person job;
* a FAILED DELETE;
* job history with no acknowledged DELETE after it.

A deleted or released terminal holds nobody. A terminal that never comes back
holds the person until it is released or deleted.

**Ordering at the terminal.** No job for a member number is handed out while an
older DELETE for the same number is pending for that terminal. So a DELETE for
an old holder can never arrive after the CREATE for the new one. The reconciler
does not re-queue a DELETE the terminal has acknowledged.

**Late uploads.** A door event naming a deleted member's number is stored with
no member. This applies when the event happened before anybody new was given
that number. A REMOVED report about a number the ledger knows is resolved to
the row the terminal was asked to remove. If that resolves to a live member it
was not asked to remove, the report is accepted (200) without touching them.

## An operator

The account row is deleted. Sessions, reset and invitation tokens, site grants
and assistant records go with it by cascade. Their audit rows are kept with the
actor replaced by `deleted-operator:<public id>`, and with no IP address or
browser. This covers rows that recorded only their address (terminal claimed
or collected), within their account's lifetime. Their address also leaves
other operators' audit diffs. Every other table that copied their email gets
the pseudonym, matched by their user id or, where none is recorded, by address
within their lifetime. A new account later given the same address keeps its
own attribution.

The history tables' trigger admits only what erasure does: an actor or label
can change only to a `deleted-…` pseudonym, `changes` can only lose keys, and
the actor id, IP address and browser can only become NULL.

## Rows deleted before 039

People and operators soft-deleted before this migration are erased by the
`erasure` task on its first run after deploy. **This changes production data
when deployed.** It is the point of the change, and it is irreversible.

## Restoring a backup

A restored backup brings back people deleted after it was taken. The
`erasure_replay` task (at startup, then daily) deletes again any live person
whose number matches a ledger entry and who was created before that entry.

**The ledger must survive the restore.** A whole-database restore replaces it
with the backup's own, older copy, so:

1. Before restoring, export the ledger from the current database:
   `\copy deleted_subjects TO 'deleted_subjects.csv' CSV HEADER` and
   `\copy deletion_ledger_keys TO 'ledger_keys.csv' CSV HEADER`.
   The keys file is a secret; handle it like one.
2. Restore.
3. Import both files. Duplicates are harmless: they match the same hash.
4. Start the API (replay runs at startup), or wait for the next daily replay.

If the current database is lost entirely, deletions made after the backup
cannot be replayed. Nothing in the backup records them.

## Configuration

| Variable | Default | Meaning |
|---|---|---|
| `DELETION_LEDGER_KEY` | **required** | Base64, ≥ 32 bytes. Missing or unusable: the API refuses to start. |
| `DELETION_LEDGER_PREVIOUS_KEYS` | unset | Comma-separated retired keys, matched but never written with. A malformed entry refuses startup. |
| `DELETION_LEDGER_ALLOW_STORED_KEY` | false | **Development only.** Without a key, generate one and store it in the database. |
| `DELETED_SUBJECT_GRACE_DAYS` | 0 (never) | Ledger entry lifetime after finalisation. Finite only once no 1.3.5 terminal is in service, and then at least the age of the oldest restorable backup. |
| `AUDIT_RETENTION_DEFAULT_DAYS` | 365 | Audit window for companies without one. |
| `API_CREDENTIAL_RETENTION_DAYS` | 90 | Retired integration keys. |

## The ledger key

**Required.** The API refuses to start without a usable `DELETION_LEDGER_KEY`,
and a deletion attempted without one fails and rolls back whole. Nothing is
generated silently. A key held beside the hashes would protect little, because
member numbers are short and anybody with the database could test candidates.
A generated key could also be replaced as silently as it appeared.

**Development only.** `DELETION_LEDGER_ALLOW_STORED_KEY=true` generates one key
on the first deletion and stores it in `deletion_ledger_keys`. It is stable
from then on.

**Setting a key later.** New entries use it. Entries made under the stored key
still match, because stored keys are always loaded.

**Rotating or losing a key.** An entry can never be re-hashed, because the
member number is not kept. A key that disappears therefore leaves its entries
unable to recognise anybody. That is never silent: the `erasure` task fails
every pass, naming the count (`UnmatchableLedgerEntries`), until the key is
supplied again via `DELETION_LEDGER_PREVIOUS_KEYS`. To rotate, move the old key
there and leave it while any entry made under it is alive.

**Restoring.** Export `deletion_ledger_keys` with the ledger (see above). An
environment key lives outside the database and survives a restore by itself.

## How long a ledger entry must live

An entry is never removed before finalisation. That wait is event-driven,
whatever the terminal's offline period. After finalisation two things can
still present the number:

* **Terminals on firmware 1.3.5.** Queued door events are kept in flash across
  reboots and have no time limit. Network failures never spend an attempt;
  only server-answered failures spend one of the 20. An event leaves only when
  accepted, refused 20 times, or pushed out by 32 newer events. A 1.3.5
  terminal that confirms the removal and then goes offline can therefore
  upload a pre-deletion event at any later time. Firmware with the queue scrub
  blanks the member in its queue before it acknowledges the DELETE, which
  closes this.
* **Backups.** Any backup taken before a deletion brings the person back on
  restore, and only a live ledger entry replays it. This includes manual
  `pg_dump` files, which have no expiry of their own.

So no finite value is demonstrably safe while 1.3.5 terminals are in service,
or while any backup of unbounded age exists. The default is therefore `0`
(never). An entry is only a keyed hash and its timestamps. Set a finite value
only once both conditions are resolved, and then to at least the age of the
oldest restorable backup.

## Not covered

Company termination is not implemented. Server request logs now record the
route template, not the URL, but their retention is the host's. Backups are
the host's.
