# Classy Cash — integration contract

Audience: the people (and agents) who build everything *around* the Go application — the Cloudflare
Email Worker, the mail relay, FROG provisioning, the `classy-cash/demo` wrapper. This document is the
interface; the application never needs to know how those pieces are deployed.

Status: written against the Go rewrite on `main`. Anything marked **planned** is *not* implemented and
will be documented here before the app starts requiring it.

Contents: [1 Configuration](#1-configuration) · [2 Runtime](#2-runtime-health-and-resources) ·
[3 Bank-mail webhook](#3-bank-mail-webhook) · [4 Outgoing mail relay](#4-outgoing-mail-relay) ·
[5 Demo mode](#5-demo-mode-contract-for-classy-clashdemo) · [6 Backups](#6-backups-and-restore) ·
[7 Release artifacts](#7-release-artifacts-and-upgrades) · [8 Security notes](#8-security-notes)

## 1. Configuration

Every setting is an environment variable prefixed `CLASSYCASH_`. The contract is **versioned**:
`CLASSYCASH_CONFIG_VERSION` (currently `1`) may be set; if it differs from what the binary understands
the process refuses to start. The version is bumped only when a variable is removed or changes meaning.
`classycash config-check` validates the environment and prints the effective, secret-free values.
A commented template is in [`deploy/env.example`](../deploy/env.example).

| Variable | Default | Meaning |
| --- | --- | --- |
| `CONFIG_VERSION` | – | Must equal `1` when present. |
| `ENV` | `prod` | `prod` or `dev`. `prod` requires `SESSION_SECRET`, https relay URL and a claim token for onboarding. |
| `ADDR` | `127.0.0.1:8080` | Listen address. |
| `BASE_URL` | `http://localhost:8080` | Public URL; used in e-mail links and for Origin checks. `https://` turns on `Secure` cookies and HSTS. |
| `DB_PATH` | `data/classycash.db` | SQLite file. Its directory must be persistent and writable (WAL files live beside it). |
| `INSTANCE_ID` | `local` | Stable identifier shown in `/ready`-adjacent diagnostics and settings. Informational. |
| `TIMEZONE` | `Europe/Warsaw` | Display zone (timestamps are stored in UTC). tzdata is embedded. |
| `MEMORY_LIMIT_MB` | `160` | Soft Go memory limit (`GOMEMLIMIT`). Keep below host RAM. |
| `LOG_LEVEL` | `info` | `debug`/`info`/`warn`/`error`. JSON logs on stdout. Bodies of bank mail are never logged. |
| `TRUST_PROXY` | off | Honour the rightmost `X-Forwarded-For` hop (set behind the FROG HTTPS proxy) for rate limiting. |
| `SESSION_SECRET` | dev only | ≥ 32 chars; keys CSRF tokens. Required in `prod`. Rotating it invalidates open forms, not sessions. |
| `CLAIM_TOKEN` | – | One-time owner claim code for first-run onboarding (see below). |
| `SUPPORT_URL` | `https://suppi.pl/mleczakm` | Voluntary "buy the author a coffee" page. Works by default in every instance, demo included; the link is shown only to a signed-in user who has no unpaid or partly paid obligation in any class. `off` hides it; any other value must be an `https` URL. |
| `MAIL_DRIVER` | `relay` if a relay URL is set, `log` in dev, else `capture` | `relay`, `log` or `capture`. `log`/`capture` never leave the host. |
| `MAIL_RELAY_URL` / `MAIL_RELAY_KEY` | – | Relay endpoint (https in prod) and its per-instance bearer key. |
| `MAIL_FROM_NAME` | `Classy Cash` | Display name sent to the relay. The visible From *address* is owned by the relay. |
| `BANK_MAIL_ADDRESS` | – | The random inbound address (lower-cased). The webhook accepts only this recipient. |
| `BANK_MAIL_SECRET` | – | Shared HMAC secret with the Worker, **≥ 32 chars**. Empty ⇒ webhook returns 503. |
| `BANK_MAIL_FROM` | – | Optional extra check: exact expected `mail_from`; mismatches are quarantined. |
| `DEMO_MODE` | off | See [§5](#5-demo-mode-contract-for-classy-clashdemo). Forces capture mail and disables the webhook. |

**First-run claim.** A fresh database has no users, so every visit routes to `/onboarding`. In `prod`
onboarding is refused (HTTP 503) unless `CLAIM_TOKEN` is set, and the form requires that code — a public
"first visitor wins" page is not possible. Provisioning should generate a random token, hand it to the
owner out of band (e.g. as `/onboarding?claim=…`), and remove it from the environment afterwards. Once any
user exists onboarding is closed for good. Onboarding creates the administrator, the class, the treasurer
membership and the default settings in one transaction.

## 2. Runtime, health and resources

* One static, CGO-free binary (`classycash`) with templates, assets, fonts and migrations embedded.
  SQLite is embedded (pure Go). No other process or service is required.
* Commands: `serve` (default; applies pending migrations itself at start-up), `migrate`, `backup FILE`,
  `demo-load [FILE]`, `ledger-rebuild`, `export-user EMAIL`, `erase-user EMAIL`, `config-check`, `version`.
* **Personal data (GDPR).** Every signed-in person can download their data (`GET /profile/export`, JSON) and
  erase their account on the profile page (type `USUŃ` + password). The operator serves requests received by
  e-mail with `classycash export-user EMAIL` (JSON on stdout) and `classycash erase-user EMAIL`. Erasure
  anonymises the account (placeholder address `erased-<id>@erased.invalid`, name/phone/password removed,
  sessions, sign-in links, memberships, parent links and mail addressed to the person deleted); payments,
  obligations and the ledger stay because they are the class's accounting records, but no longer identify the
  person. Children stay on the class roster (the treasurer controls that data). A treasurer or the last
  administrator must be replaced first (the command refuses). Bank-transfer sender names, which come from the
  bank's notification, are accounting data and are not touched. Bank-pipeline test references the person created
  (§3) appear in their export as `bank_test_references`; erasure clears the link to the person but keeps the
  reference itself (marker, status, counters), so a notification that still arrives is swallowed as before.
  Disabled in demo mode.
* Graceful shutdown on `SIGTERM`/`SIGINT` (20 s drain, WAL checkpoint).
* **Endpoints for supervisors**
  * `GET /health` (alias `/ping`) → `200 ok`: liveness, touches nothing.
  * `GET /ready` → JSON; `200` only when the database answers *and* no migration is pending, otherwise
    `503`. Also reports `mode` (`normal`/`demo`), `mail_driver`, `mail_pending`, `mail_failed`,
    `mail_oldest_pending_seconds`, `bank_webhook` (`enabled`/`disabled`) and `bank_last_delivery`
    (RFC 3339, last authenticated bank delivery). Suggested alerts: `mail_failed > 0`,
    `mail_oldest_pending_seconds > 900`, no `bank_last_delivery` for the expected period.
  * `GET /version` → `{version, commit, built, config_version}`.
* **Measured resource use** (Alpine container with `--memory=256m`, linux/arm64, this build): idle RSS
  ≈ 16 MiB; 64 concurrent login attempts (8 reach argon2, the rest are throttled) peak at 87 MiB RSS and
  settle at 28 MiB. Password hashing is argon2id (19 MiB, t=2, p=1), serialised one at a time. The image is
  36 MB, the binary ≈ 20 MB. These are single-run measurements, not a soak test.
* Background work (mail dispatch every 10 s, re-matching of unmatched transfers from the last 12 h every
  minute, expiry/retention every 5 min) reads its queue from the database, so a restart loses nothing.

## 3. Bank-mail webhook

The Cloudflare Email Worker delivers each bank notification to the app. **The wire contract is exactly
Ceramiza's** (`ceramiza/internal/web/bank_mail.go`) so one Worker works for both.

```
POST {BASE_URL}/api/bank-mail
Content-Type: application/json
X-Ceramiza-Bank-Timestamp: 2026-10-27T10:15:02.123Z        # RFC 3339, fresh on every attempt
X-Ceramiza-Bank-Signature: <hex HMAC-SHA256>               # see below

{"id":"<64 hex chars>","received_at":"2026-10-27T10:15:01.900Z","recipient":"bank-…@classycash.dpdns.org",
 "mail_from":"powiadomienia@alior.pl","subject":"Uznanie rachunku 91...6978 kwotą 50,00 PLN","body":"<html>…"}
```

**Signature**: `hex( HMAC-SHA256( BANK_MAIL_SECRET, timestamp + "\n" + exactRequestBodyBytes ) )` — the
timestamp string exactly as sent in the header, one LF, then the raw JSON bytes (not re-serialised).

```js
// Worker (same as Ceramiza)
const sig = hex(await crypto.subtle.sign("HMAC", key, enc.encode(`${timestamp}\n${body}`)));
```

**Validation and responses** (checked in this order):

| Condition | Response |
| --- | --- |
| Method not `POST` | `405` |
| `BANK_MAIL_SECRET` < 32 chars or no `BANK_MAIL_ADDRESS` (always the case in demo) | `503 bank mail is not configured` |
| Body > 128 KiB | `413` |
| Timestamp unparsable, or more than ±5 min from the server clock | `401 invalid signature` |
| Signature not hex or HMAC mismatch (constant-time compare) | `401 invalid signature` |
| Body not JSON; `id` not exactly 64 hex chars; `recipient` ≠ `BANK_MAIL_ADDRESS` (case-insensitive); `mail_from` empty; `subject` > 2000 or `body` > 100 000 bytes | `400 invalid payload` |
| `received_at` not RFC 3339 | `400 invalid timestamp` |
| Storage failure | `500 storage error` (retry) |
| **Anything else, including redeliveries and quarantined mail** | **`204 No Content`** |

**Worker retry policy.** Treat `2xx` as done. Retry with a *new* timestamp/signature each attempt and the
same `id`. Retrying every non-2xx (as Ceramiza does) is acceptable, but `400`/`413` can never succeed and should
reach the DLQ quickly, and `401` means a secret mismatch or more than 5 minutes of clock skew — alert on it.
Do not follow redirects. Ceramiza's schedule (`30·2^n` s capped at 30 min, 20 attempts, DLQ) works unchanged.

**Idempotency and effects.** In one SQLite transaction the app stores the delivery (keyed by `id`, lower-cased),
the parsed transfer, any matching/settlement and the notification outbox rows. A second delivery with the same
`id` is acknowledged with `204` and changes nothing. `id` is *transport* identity (Ceramiza uses SHA-256 of
the raw MIME); a bank that re-issues the same credit with different headers gets a new `id`, so the app adds a
second line of defence: a transfer with the same account, sender, title and amount as one received in the last
24 h is **held for review** (not auto-matched, treasurer approves or dismisses). Two legitimate transfers
with different titles/codes are never merged.

**What is parsed.** Only Alior credit notifications: subject must start with `Uznanie rachunku`; account and
amount come from the subject, sender and title from the body (`Nadawca:` / `Tytuł zlecenia:` followed by
`<br`). Plain-text bodies work too. A message that is not a recognisable credit is **quarantined** (status
`unrecognized`, visible to administrators only because the bank inbox is instance-wide, `204` returned) and can be replayed after a parser improvement —
replay is idempotent. A `body` that is only the HTML/text alternative is fine; the Worker sends
`parsed.html || parsed.text`.

**Matching** (ported from the PHP matcher): payment code in the title (single words, then adjacent words
glued together to survive "AB 12", then `0`↔`O` substitutions; first candidate that is an open payment wins);
otherwise a unique historical match (same account/sender previously matched to the same student or payer *and*
equal amount). Ambiguity ⇒ unmatched for manual assignment. A full match settles the payment; a part payment
leaves it pending with the transfer attached ("Niedopłata").

**Origin checks.** Address matching alone is not proof of bank origin. Keep the Worker's exact
recipient + envelope-sender + `From` checks. `BANK_MAIL_FROM` adds an app-side check on `mail_from`.
*Planned, not required yet:* an optional `auth` object in the payload carrying the Worker's SPF/DKIM/DMARC
verdicts, which the app would enforce when configured. Nothing in the current contract needs it.

**Pipeline self-test (settings → "Test połączenia z bankiem").** The treasurer (or an administrator) opens a
*diagnostic reference* — a unique marker `CCTEST-XXXX-XXXX` (8 random characters from an alphabet without
`0 O 1 I L`, so it cannot be a payment code, which are 4-6 characters) with an expected amount of at most 10,00 zł —
and makes a small **real transfer herself** with the marker in the title. The app never initiates, requests or
simulates a transfer. Status is `Oczekuje` → `Sukces` once the bank's notification arrives through the Worker (open
references: 3 per class, valid for 48 h; expiry is computed from the clock, not by a job).

* **A reference is not a payment.** `processEvent` hands every delivery to the interceptor *before* parsing results
  are stored or matched: a delivery whose title, subject or body contains a known marker (case, spaces and hyphens
  are ignored) creates **no `transfers` row**, runs no matching (not by code, not by history), enqueues no
  administrator mail, never touches `payments`, `student_payments` or the ledger, and is stored in `bank_events`
  with status `test` (readable notification) or `test_unparsed` (marker present, not a recognised credit), never
  `parsed`, so it does not count as an import.
* **Retries, duplicates, replays.** Same event id ⇒ ordinary transport idempotency (no second count). A new event id
  with the same marker (bank retry, second mail) is swallowed again and only raises the hit counter. Replaying a
  stored event whose body carries a marker is swallowed too. Cancelled and expired references keep swallowing: a late
  notification is recorded ("dotarło po terminie", **not** a success) and still books nothing.
* **Races.** All of it runs in the ingest transaction under SQLite's immediate write lock, so a delivery racing a
  cancellation or the expiry boundary is serialised: the outcome is success or cancelled/expired, never a booking.
  A success cannot be cancelled. The interception is global — any known marker is swallowed whichever class it came from.
* **Unrecognised look-alikes** (`CCTEST-…` that was never issued) are ordinary transfers. A delivery rejected by
  `BANK_MAIL_FROM` is quarantined before any of this; the test then simply stays `Oczekuje`.
* **Limits of what it proves.** Success shows that mail from the bank reached the Worker, passed the signature
  check and was parsed. The sender/amount shown in `bank_events` is the bank's; a differing amount is flagged
  but still counts as a successful round trip. A person who knew an open marker could hide a *real* transfer from
  the books by putting it in the title (it would stay visible only as a `test` event with its hit counter on the
  settings page) — markers are shown only to the class's managers, and a stray one is visible on the settings page as an unexpected hit.
* **Not available in demo mode** (the webhook answers `503` there, so a real notification cannot arrive) or when the
  webhook is not configured: the page says so and offers no form. Schema: migration `0006` (`bank_tests`), additive;
  a seed may ignore it.

**Intentional differences from Ceramiza** (none on the wire): Alior parser only (no generic parser); the
duplicate-review rule above; unrecognised and spoofed-sender deliveries are acknowledged and kept for
review instead of rejected; stored bodies are blanked after 90 days.

## 4. Outgoing mail relay

The app never speaks SMTP. It enqueues every message in a durable `outbox` table and a background job
POSTs it to an HTTPS relay that owns the shared mailbox and the visible From address.

```
POST {MAIL_RELAY_URL}
Authorization: Bearer {MAIL_RELAY_KEY}
Idempotency-Key: <key>                 # identical to idempotency_key below
Content-Type: application/json

{"idempotency_key":"settled-123-payer","from_name":"Classy Cash","reply_to":"skarbnik@example.com",
 "to":"anna@example.com","to_name":"Anna Kowalska","subject":"…","html":"<!DOCTYPE html>…","text":"…"}
```

* **Success**: any `2xx`. The relay must treat a repeated `Idempotency-Key` as already accepted (return `2xx`
  without sending again) — the app retries after timeouts.
* **Retry** (backoff 30 s, 60 s, 2 min … capped at 1 h, 12 attempts then the message is parked as
  `failed` and shows in `/ready` and the admin “Wiadomości” page): network errors, `5xx`, `429`, and
  `401`/`403` (a revoked or misconfigured key is an operator problem; messages wait).
* **Permanent failure** (no retry): any other `4xx` (e.g. `400`, `422` invalid recipient).
* Redirects are **not** followed.
* **The relay must enforce**: fixed From address (`no-reply@classycash.dpdns.org`) with `from_name` as the
  display name only; the app never sends a `from` address and the relay must ignore/forbid one; per-instance
  quotas and rate limits; recipient and size limits; no raw SMTP proxying; no unauthenticated sends; no test
  endpoint that can mail arbitrary third parties. Rotating or revoking a key must not need an app change.
* Message kinds today: login link, password reset, parent invitation, settlement confirmation (parent),
  new-payment notice (treasurer), unmatched/duplicate-transfer alert (administrators), part-payment notice.
  All are rendered by the app (Polish, brand palette); the relay does not template.
* Idempotency keys are deterministic where a business event repeats (`settled-<payment>-payer`,
  `unmatched-<transfer>-<admin>`) and random-per-request for links, so a retry never duplicates a mail.
* In `capture`/`log` mode (default without a relay, always in demo) messages are stored as `captured` and
  can be read by administrators at `/admin/mail`. Nothing leaves the host.

## 5. Demo mode (contract for `classy-cash/demo`)

The shared demo is a separate repository. The app provides only the following; it contains **no seed data,
no scheduler and no deployment logic**.

**`CLASSYCASH_DEMO_MODE=1` (trusted runtime configuration, cannot be changed from the UI) means:**

* `MAIL_DRIVER` is forced to `capture`; relay URL/key are discarded. No mail can leave, including password
  reset, invitations and settlement/transfer notices — they appear only under `/admin/mail`.
* The bank webhook is disabled (`503`) and the bank address/secret are discarded.
* Onboarding is closed; password reset sends nothing; profile edits, password changes and the reply-to
  setting are locked, so demo credentials and integrations cannot be altered.
* A persistent banner explains that data is shared and reset every 15 minutes and shows a countdown to the
  next wall-clock quarter hour (`:00/:15/:30/:45`, computed client-side from the server time). **The reset
  itself is performed by the wrapper**; if the wrapper uses a different cadence the banner text is wrong.
* `/ready` reports `"mode":"demo"`, `"bank_webhook":"disabled"`.
* The login page lists quick-login buttons for accounts named in the `demo_accounts` setting (below); there is
  no other passwordless route.

**Loading data — `classycash demo-load [seed.sql]`** (stdin when no file; refuses unless `DEMO_MODE` is on):

1. Opens the database and applies pending migrations (so a fresh file works).
2. In **one IMMEDIATE transaction**: empties every table except `schema_migrations`, executes the seed SQL,
   checks foreign keys, increments the `generation` setting. Any error rolls everything back and the previous
   dataset stays intact (exit status 1, message on stderr).
3. Prints `{"generation":N,"loaded_at":"…"}`.

Because sessions are rows in the database, a reload signs every browser out; stale forms fail CSRF/session
checks and the visitor is sent to the login page. Running the command while the server is up is supported
(SQLite WAL, 5 s busy timeout); the server keeps no state in memory that a reload could make stale.
The wrapper may equally stop the server, replace the database file and start it again.

**Seed requirements** (schema: [`migrations/`](../migrations) — `0001_init.sql` plus additive later files; they
are the source of truth, pin the wrapper to an app version and re-check on upgrades):

* Ids are integers; timestamps are RFC 3339 **UTC** text; money is integer **grosze**. Generate relative
  dates (deadlines, history) from the reset time in the wrapper.
* Insert users with `password_hash=''` (quick login) and/or an argon2id hash; `is_admin` for the treasurer.
* Needed settings rows: `app_name`, `parent_registration_enabled` (`0`/`1`), `bank_class_id`, and
  `demo_accounts` = JSON array `[{"label":"Skarbnik","email":"…","role":"skarbnik"}, …]` (only these emails can
  quick-login). `generation` is managed by the loader — do not write it.
* **Pending payments must be younger than 24 h at load time.** The app expires `pending` payments older than 24 h
  (at start-up and every 5 minutes), so a seed with a "pending" payment created weeks ago shows as "Wygasła"
  within minutes. Generate `created_at` relative to the load time (e.g. a few hours ago) on every reset.
* **Every `contribution_students` row needs a `student_payments` row** (same contribution and student); a slot
  without an obligation renders as "Anulowano" (cancelled). Paid obligations need `payment_id` and `paid_at`.
* Failed and paid payments should carry `code = NULL` (the app clears codes when a payment fails or is paid).
* After loading ledger-affecting data (paid payments, expenses) run `classycash ledger-rebuild`, or insert
  `ledger` rows yourself. The ledger is derived: one income per `paid` payment (valued at the sum of its
  matched transfers, else its amount, dated `paid_at`) and one outflow per expense.
* Use fictional names, `example.test`/`example.com` addresses and placeholder BLIK/account numbers.
  Seed payments in every state (`pending, paid, failed, refunded, expired`), obligations in `pending, partial,
  paid`, matched/unmatched/review (`review_reason` set) transfers and `bank_events` rows
  (`parsed, unrecognized, duplicate_review`) to demonstrate each screen.
* Migration `0006` adds the diagnostic table `bank_tests` (empty in a seed; the feature is off in demo mode).
* Migration `0005` adds `student_payments.amount_override` (`0`/`1`, default `0`; `1` = individual amount that edits
  of the collection do not overwrite). Optional in a seed.
* Migration `0004` adds `classes.archived_at` (set = archived class: read-only, listed last). Optional in a seed.
* Migration `0003` (corrections) is additive and optional for a seed: `contributions.closed_at` (set = closed,
  shown as "Zakończona"), `expenses.voided_at` (set = voided, left out of the ledger), `payments.source`
  (`online` default; `manual` marks hand-booked cash so it appears in "Ostatnie wpisy ręczne" and can be
  reversed) and `payments.note` (its description, e.g. `Jan Kowalski — Wycieczka`). A seed that ignores them
  still loads; add `source='manual'` rows and a voided expense to demonstrate the new screens.
* Receipts (`expenses.attachment_path`) are files on disk and are disabled in demo mode: leave the column `NULL`.

`scripts/demo_acceptance.py` is a ready-made black-box check for any deployment (see its docstring); it needs only
HTTP access and discovers the quick-login accounts from the login page.

**Hosting note (Docker):** a container attached *only* to a network declared `internal: true` has no outbound
route (good) but Docker also **does not publish its ports**, so `ports: ["127.0.0.1:PORT:8080"]` silently does
nothing. Put a small proxy (nginx/socat) on both the internal network and a normal one, or block egress with
host firewall rules instead.

Verification the wrapper can do after a load: `GET /ready` → `200`, `mode=demo`; `GET /login` → quick-login
buttons present; `generation` increased.

## 6. Backups and restore

* **Receipts live outside the database**, in `DATA_DIR/attachments/` (one directory per class, files `0600`).
  `classycash backup` copies only the SQLite file, so back up that directory with it (`rsync`/tar), and restore
  both together; a database restored without its files just shows the receipt link as "not found". Disabled in
  demo mode (nothing is written there). Uploads are limited to 5 MB per file.
* `classycash backup /path/file.db` writes a transactionally consistent copy with SQLite `VACUUM INTO`
  (safe while the server runs; a plain copy of the `.db` file with WAL is not), sets mode `0600`, and verifies it
  with `PRAGMA integrity_check`. It refuses to overwrite an existing file.
* Restore: stop the service, put the verified file at `DB_PATH` (remove stale `-wal`/`-shm`), start; migrations
  run forward automatically. Encrypt and ship backups off-server in the provisioning layer (the app does not
  encrypt, does not hold backup keys). Suggested: daily, 7 daily + 4 weekly points.
* A restore from the same or an older binary version is only guaranteed when the schema is compatible;
  otherwise restore the pre-upgrade backup taken by the update workflow.

## 7. Release artifacts and upgrades

* **Versioning uses a UTC build-time scheme** (inherited from the original PHP application): the version is the UTC build time `YYYYMMDDHHMM` (e.g.
  `202610271430`), injected with `-ldflags` and shown in the UI as "Wersja z 15:30, 27 paź" (installation time
  zone). `make release` / `scripts/release.sh` default to the current time; pass `VERSION=…` to rebuild an existing
  release byte-for-byte. Local builds report `dev`. Config compatibility is tracked separately by `CONFIG_VERSION`.
* `make release` / `scripts/release.sh` produce, in `dist/`:
  `classycash_<version>_linux_amd64.tar.gz`, `classycash_<version>_linux_arm64.tar.gz` and `SHA256SUMS`
  (`sha256sum -c SHA256SUMS`). Each tarball contains `classycash` (static), `LICENSE`, `env.example`,
  `classycash.openrc`. Archives are byte-reproducible with a given `tar` implementation (fixed mtimes, sorted
  entries, root ownership, `--no-recursion`, no gzip timestamp; GNU tar and bsdtar write different headers, so
  checksums are only comparable between builds made with the same one — CI uses GNU tar). Each tarball holds
  exactly one directory and four regular files, **no hard links**; `scripts/release.sh` runs
  `scripts/check-archive.sh` on every archive (no duplicate entries, no links, expected layout, clean
  extraction). An early artifact (workflow run `40488020394`) listed every file twice — GNU tar recursed into the
  directory that the file list already named and stored the second copy as a hard link to itself, which made
  bsdtar extraction fail; fixed by `--no-recursion`.
* Container: `deploy/Dockerfile` (Alpine 3.22, non-root, `/data` volume, `HEALTHCHECK` on `/health`).
* Alpine/OpenRC: [`deploy/openrc/classycash`](../deploy/openrc/classycash) runs `/opt/classycash/classycash`
  as user `classycash` with `/etc/classycash/env`.
* GitHub: `Go CI` runs on pushes/PRs. `Go release artifacts` is **manual** and, unless asked, uploads the files
  only as workflow artifacts (they expire) and creates no tag or release. With `publish_release` it additionally
  creates the durable GitHub Release described below. With `publish_image` it also pushes a multi-arch image
  `ghcr.io/classy-cash/classycash:<version>` (private package; no `latest` tag, pin the version; the old PHP
  image under `ghcr.io/classy-cash/app` is no longer built). Public GitHub Releases exist for `v202610280850`
  (`b6756c8`), `v202610281446` (`d5fac03`) and `v202610281453` (`22f832b`), each with the two archives and
  `SHA256SUMS`. No container image has been published to GHCR yet; every publish is a deliberate act of the release
  owner.
* **Release publication contract** (consumed by the per-instance FROG installer in `classy-cash/infrastructure`,
  `templates/instance`). Instances never build the app and never read workflow artifacts, which expire. A release
  is published by running `Go release artifacts` manually with `publish_release` on `main`; the `release` job
  (the only one with `contents: write`) then:
  * creates the tag `v<version>` at the built commit and a GitHub Release of the same name in this
    repository, marked "not latest", with exactly three assets: `classycash_<version>_linux_amd64.tar.gz`,
    `classycash_<version>_linux_arm64.tar.gz` and `SHA256SUMS`;
  * refuses to run when the tag or release already exists. **A published version is immutable**: never delete,
    re-upload or move it; fix forward with a new version. Deleting a release breaks every instance still pinned
    to it (they keep running, but cannot install or roll back to it);
  * does not sign anything. Authenticity of an instance's binary rests on the SHA-256 that the control plane
    pins in the instance repository's `release.json`, taken from this `SHA256SUMS`; the instance verifies the
    download against that pin and the archive layout before touching the host, and verifies the checksum again
    on the server;
  * the tag push triggers no workflow.
  Instances download the release anonymously over HTTPS (no token or credential of any kind), only when the
  pinned version differs from the installed one; this requires the release to be publicly downloadable.
  Weekly maintenance without a pending upgrade downloads nothing. Retention: keep every version that any
  instance is still pinned to.
* **The legacy PHP application is gone from the tree**, together with its QA, production-deploy and
  maintenance-page workflows, Ansible roles, Docker/compose files and Symfony configuration. Pushing a tag
  deploys nothing. The last commit that still contains the PHP sources is `22f832b`
  (`git show 22f832b:src/…`; see [PARITY.md](PARITY.md)).
* Upgrade procedure for the deployment layer: `classycash backup` → replace the binary → restart (migrations
  run at start-up; `/ready` is `503` until they finish) → check `/ready` and `/version`. Binary rollback is safe
  only while the schema is unchanged; otherwise restore the pre-upgrade backup. The bank address, secrets,
  database and settings persist across upgrades because none of them live in the binary.
* Artifacts are checksummed but **not signed**; signing/attestation is left to the release pipeline.

## 8. Security notes

* Sessions are random 32-byte tokens stored as SHA-256 in the database (`HttpOnly`, `SameSite=Lax`, `Secure`
  on https, 90 days); changing or resetting a password bumps the user's epoch and deletes all their sessions.
* CSRF: per-browser cookie + HMAC form token on every state-changing request, plus an `Origin` check. The
  webhook is exempt (authenticated by HMAC, not cookies).
* CSP: `default-src 'self'`; no inline script or style; no third-party requests; fonts are bundled
  (Inter, Playfair Display, SIL OFL).
* Login: argon2id; per-IP and per-account throttling (in memory, resets on restart); unknown accounts and wrong
  passwords are indistinguishable; magic-link and reset requests never reveal whether an address exists. Login
  and reset links are single-use, valid 1 h (invitations 72 h), and a `GET` never spends them (mail scanners).
* Every class-scoped query filters by class; treasurers manage only their own class, administrators every
  class, parents only their children's obligations and their own payments.
* The app records and matches payments; it does not move money, hold funds or ask for bank credentials.
