# Tender

Move physical naira to anyone, instantly, without going to a bank.

## What it actually does

Someone holding cash needs to send value to someone far away. Today that means a
bank queue or a POS agent. Tender does it by **not moving the cash into the banking
system at all** — it moves it sideways to somebody nearby who wanted physical notes.

```
Ada holds ₦20,000 cash in Lagos and wants it in Bola's account in Abuja.
Chidi, 400m away, holds ₦20,000 digitally and needs notes for his market stall.

Ada photographs her cash        →  Chidi's ₦20,000 is locked in escrow
Ada hands the notes to Chidi    →  both confirm with a six-digit code
                                →  Chidi's ₦20,000 pays out to Bola's bank account
```

The platform holds no cash, runs no vault, and makes no bank deposit. It is a
**matching engine plus escrow**.

## The photograph is not collateral

A photo proves nothing — anyone can photograph a stranger's cash. So the design
never relies on it:

| Attack | What happens |
|---|---|
| Photograph somebody else's cash | Nothing. No notes at handover means no handover, so no value moves. |
| Photograph a screen or a photocopy | Refused at pledge time by the recognition step. |
| Pledge the same notes twice | Refused: serial numbers and a perceptual hash are registered while a transfer is open. |
| Never turn up to the handover | Escrow is released, the recipient sees "expired", nobody is out of pocket. |
| Advertise a cash request as a lure | Requests can only be posted against premises you operate, and the book is never published. |
| Take the cash and never confirm | The sender reports it; the funds are **held**, not released, and the operator is suspended. |

Recognition is a **pre-filter and a fraud signal**, never a guarantee.
Authenticity is established by the counterparty who physically receives the
notes — they are the one who loses if the notes are fake.

## Physical safety is a separate problem from escrow

Escrow guarantees nobody loses money. It does nothing to stop somebody being
robbed, and left alone it made robbery *free*: take the cash, never confirm, and
the sweeper hands your escrow back on a timer. Three things address that.

**Handovers only happen at registered venues.** A counterparty must be an
operator with fixed, public, verified premises — an agent shop, a filling
station, a bank branch — and can only post cash requests against a venue they
run. Nobody nominates a meeting place per transaction, because the person
choosing the spot is the one who benefits from choosing a bad one. Matching is
also restricted to a venue's opening hours.

**The demand book is never published.** There is no endpoint listing everyone's
open requests. An open request means "this person will be holding cash, here,
shortly", and publishing it was a target list that did not even require using the
app to read. Only the matching engine sees the whole book; you see your own.

**A reported incident holds the money.** Escrow cannot tell theft from an
innocent no-show — in both cases the counterparty simply never confirms. Reporting
moves the transfer to `disputed`, which the expiry sweeper deliberately does not
touch, so the funds stay locked until a person reviews it. The accused is
suspended and their open requests come off the book immediately.

Per-handover amounts are also capped, so a meeting is never worth targeting for a
large sum.

## Where the financial risk lives

**Tier 0 (escrow, the default)** carries no platform risk at all. Value moves only
after cash has moved.

**Tier 1 (instant credit)** is the one place the platform is exposed: the
recipient is paid at snap time out of float, and the sender owes a time-boxed
obligation. Every account starts at a **₦0 credit limit** and earns one by
settling. Underwriting is on identity and history, never on the photograph. On
default the balance is clawed back, sending is frozen, and the remainder goes to
the loss reserve.

## Note recognition

Recognising a ₦500 from a ₦1,000 is easy. **Counting forty overlapping notes is
not** — and it is the count, not the denomination, that decides whether a
transfer goes through, because a declared amount that does not match what is
visible is refused. Three things follow from that.

**The model is asked for counts per denomination, not a list of notes.** Asking
it to enumerate forty objects costs a great deal of output and gives it more
chances to lose its place. `{"counts": {"1000": 20}}` is both cheaper and easier
to get right; Go expands it back into individual notes for the registry.

**The declared amount is never sent to the model.** Asked to check a claim, a
model tends to agree with it — and the case that matters most is the one where
the claim is wrong. It reports what it sees, and the comparison happens in Go.

**The capture screen does the real work.** It shows the sender what to lay out
("20 × ₦1,000") and asks for rows with nothing overlapping, because overlap is
the main cause of a bad count. Serial numbers are treated as a bonus, capped at
twelve per photo — the perceptual hash already carries the replay guard, so
paying for forty serial reads buys almost nothing.

### Choosing the model

Counting is perception, not reasoning, so a cheap model may match an expensive
one. **Decide by measurement**, using your own photographs:

```bash
go run ./cmd/eval -manifest testdata/naira/manifest.json \
    -models claude-haiku-4-5,claude-sonnet-5,claude-opus-5
```

It reports, per model, the exact-total pass rate, mean notes miscounted, serial
legibility, screen-replay detection with false positives, median latency, and
**measured cost per snap against the fee it has to come out of**. On a ₦20,000
transfer the fee is ₦100, so a model that costs more than that per call loses
money on every send. `apps/api/testdata/naira/README.md` explains what to shoot.

Set the winner as `VISION_MODEL`.

## Architecture

```
apps/web    Next.js 15 PWA — camera capture, handover, live balances
apps/api    Go 1.26 — double-entry ledger, matching, escrow, SSE
db/         SQL migrations
scripts/    migrate.sh, e2e.sh
```

Amounts are **integer kobo** (`int64`) everywhere. Floating point never touches money.

### The ledger

Every movement is a set of signed entries sharing a transaction id, and every set
must sum to exactly zero. This is enforced by a **deferred constraint trigger in
Postgres**, so an unbalanced write cannot be committed even by a bug:

```
ERROR:  unbalanced ledger transaction 1ff2c35d…: entries sum to -1 kobo, must be 0
```

`GET /v1/ledger/audit` exposes the books, including capital currently at risk.

### The float, reconciled three ways

Every bank payout debits a wallet held at Fintava. That wallet is the real
constraint on settlement: a transfer can be valid, escrowed and handed over and
still fail to pay out because the float is empty.

`GET /v1/float` reports it three ways.

| | |
|---|---|
| **GL** | the `float` control account, a system account in the double-entry ledger |
| **SL** | the entries composing it, grouped by the reason each was recorded under |
| **BANK** | what Fintava will actually let Tender pay out |

GL and SL are **not** an independent check, and are not presented as one. The
control balance is a database view over the very entries the detail sums, so
they agree by construction. The detail earns its place by explaining the control
figure — a float of ₦500,000 reads as a funding movement, a credit extended, and
a repayment that restored it — so a difference against the bank can be
attributed to a movement rather than merely noticed.

The check that can fail is **GL against BANK**. A gap means money moved on the
rail that the books never recorded, and seeing it here is better than
discovering it when a payout fails.

A balance that cannot be read is an error, never a zero: answering "no float"
when the truth is "we could not ask" would be the wrong answer to the only
question the endpoint exists to settle. When the rail is unreachable the books
are still returned, and the bank leg says it is absent.

`POST /v1/float/fund` issues a one-time account that tops the float up. Fintava's
virtual wallets are single-use and amount-specific, which is worth keeping
rather than working around: an account that only accepts the amount it was
issued for cannot quietly absorb a payment nobody is expecting.

### Accounts and sessions

Sign-up is an email address and a password, hashed with bcrypt. There is no email
verification step: an unverified address is still a real credential for signing
back in, and a wall between signing up and using the app would buy nothing here.

A session is 32 bytes from a CSPRNG. Only its SHA-256 is stored, so a database
copy does not yield working sessions, and it is returned both as a `Secure;
SameSite=None` cookie and in the response body — the PWA and the API sit on
different origins, so that cookie is third-party and some browsers drop it
outright. The bearer header is what actually keeps a phone signed in. Signing out
revokes one session rather than the account, so one phone does not sign the
others out.

Sign-in is rate limited per address and per source, an unknown address still
costs a bcrypt compare so response time does not reveal who has an account, and
every duplicate-registration collision returns the same wording so the endpoint
cannot be used to enumerate accounts. There is deliberately no route listing
users: nothing needs a directory of who holds an account.

### The recipient is a bank account

A transfer's destination is somebody's ordinary bank account, so the recipient
needs no Tender account at all. The sender picks a bank and types the account
number, and `POST /v1/accounts/resolve` returns the name the bank holds for it.
That name is what gets stored and shown — never anything the sender typed — and
is the check standing between a mistyped digit and cash handed over for nothing.
Name enquiry is rate limited, because an open one is a way to harvest the name
behind any account number in the country.

Settled and arrived are different claims, and the app shows both: the ledger
balances the moment cash changes hands, while the payout to the bank carries its
own state.

## Running it locally

```bash
# 1. Postgres
docker compose up -d db          # publishes 5433, so it cannot collide with a local 5432
pnpm db:migrate

# 2. Accounts and settlement float (idempotent)
pnpm bootstrap

# 3. API
cd apps/api && DATABASE_URL=... ANTHROPIC_API_KEY=... go run ./cmd/api

# 4. Web
pnpm --filter @tender/web dev
```

Copy `.env.example` to `.env` and fill it in first.

> **The camera needs a secure context.** `localhost` counts, but a phone pointed at
> your laptop's LAN IP does not — `getUserMedia` is blocked outright. To test on a
> real phone, use the deployed URL or an HTTPS tunnel.

## Tests

```bash
pnpm test        # unit + ledger property tests (needs Postgres)
pnpm e2e         # full settlement, replay guard, expiry, and credit paths
```

`scripts/e2e.sh` drives the real HTTP API end to end. It runs a second instance
with very short expiry windows so the abandoned-handover path can be observed in
seconds rather than half an hour.

## Deploying

**API + database → Render**, via the blueprint in `render.yaml`.

1. In Render, choose **New → Blueprint** and point it at this repository. It
   creates the web service and a Postgres instance, and wires `DATABASE_URL`
   between them.
2. Fill in the values marked `sync: false`. They are deliberately absent from
   the repository:

   | Variable | What it is |
   |---|---|
   | `ANTHROPIC_API_KEY` | Note recognition. The service refuses to start without it. |
   | `FINTAVA_API_KEY` | Bank rail: name enquiry and payouts. |
   | `FINTAVA_SOURCE_ID` | The Fintava wallet payouts debit — Tender's float. |
   | `FINTAVA_WEBHOOK_SECRET` | Verifies inbound webhook signatures. |
   | `CORS_ORIGIN` | The origin the PWA is served from. |

3. Provision the venue network once, from the service shell:
   `/usr/local/bin/bootstrap`

Migrations are embedded in the binary and applied on boot under an advisory
lock, so no pre-deploy hook is needed and several instances may start at once.

Point Fintava's webhook at `POST /v1/webhooks/fintava`. Without the secret,
events are rejected and payouts are never confirmed — they will sit in `sent`
until reconciliation chases them.

**A caveat on Render's free plan:** the service sleeps when idle, and a sleeping
instance runs neither the expiry sweeper nor payout reconciliation. Escrow on an
abandoned handover is released late, and a payout stays unconfirmed until the
next request wakes the service. For a demo that is fine; for anything real the
service needs to stay up.

**Web → Vercel**

Root Directory is `apps/web`, and `NEXT_PUBLIC_API_URL` points at the Render URL.
`CORS_ORIGIN` on the API names the Vercel origin.

Both halves deploy from a push to `main`: Render builds the API, Vercel builds
the PWA. They are separate builds of the same commit, so a change that spans the
two -- a removed route and the screen that called it -- is briefly live on one
side and not the other. Deploy the API first when the change is additive, and the
PWA first when it removes something the old PWA still calls.

## API

| | |
|---|---|
| `POST /v1/auth/signup` · `/v1/auth/signin` | email and password; returns a session token |
| `POST /v1/auth/signout` · `GET /v1/auth/me` | end a session, or read the one you hold |
| `POST /v1/users` | register an account without credentials (seeding, tests) |
| `GET /v1/users/{id}` | one account and its balances |
| `GET /v1/users/{id}/transfers` | activity |
| `GET /v1/banks` | the bank directory a recipient account can belong to |
| `GET /v1/float` | settlement capital: GL control, SL detail, and the bank balance |
| `POST /v1/float/fund` | a one-time account to top the float up |
| `POST /v1/accounts/resolve` | name enquiry: account number → the name the bank holds |
| `POST /v1/pledge` | photograph cash (multipart `photo`, or base64 JSON) |
| `POST /v1/transfers/{id}/match` | look for a counterparty again |
| `POST /v1/transfers/{id}/confirm` | confirm the handover (counterparty supplies the code) |
| `POST /v1/transfers/{id}/reject` | refuse the notes in person |
| `GET /v1/venues` | registered premises (all verified, or one operator's) |
| `GET /v1/cashouts?userId=` | **your own** cash requests — the book is never public |
| `POST /v1/cashouts` | ask for cash at premises you operate |
| `POST /v1/transfers/{id}/incident` | report a handover that went wrong; holds the funds |
| `GET /v1/stream?userId=` | server-sent events |
| `GET /v1/ledger/audit` | the books |
