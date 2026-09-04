#!/usr/bin/env bash
# End-to-end test of the settlement layer.
#
# Runs its own API instance on a spare port with the stub recognizer and very
# short expiry windows, so the abandoned-handover path can be exercised without
# waiting half an hour. Every step goes through the real HTTP API.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PORT="${E2E_PORT:-8099}"
API="http://localhost:$PORT"
export DATABASE_URL="${DATABASE_URL:-postgres://tender:tender@localhost:5432/tender?sslmode=disable}"
DB="$DATABASE_URL"
TMP="$(mktemp -d)"
FAILED=0

cleanup() {
  for pid in "${API_PID:-}" "${FAST_PID:-}"; do
    [ -n "$pid" ] || continue
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  done
  rm -rf "$TMP"
}
trap cleanup EXIT

bold() { printf '\n\033[1m%s\033[0m\n' "$*"; }
pass() { printf '  \033[32m✓\033[0m %s\n' "$*"; }
fail() { printf '  \033[31m✗\033[0m %s\n' "$*"; FAILED=1; }
info() { printf '      %s\n' "$*"; }

expect() { # expect <description> <actual> <expected>
  if [ "$2" = "$3" ]; then pass "$1"; else fail "$1 — expected '$3', got '$2'"; fi
}

img() { "$TMP/testimg" -seed "$1" -out "$2"; }

# post_cashout <operator-id> <amount-kobo> -- resolves the operator's venue.
post_cashout() {
  local vid
  vid=$(curl -s "$API/v1/venues?operatorId=$1" | jq -r '.[0].id')
  curl -sX POST "$API/v1/cashouts" -H 'Content-Type: application/json' \
    -d "{\"userId\":\"$1\",\"amountKobo\":$2,\"venueId\":\"$vid\"}" >/dev/null
}
balance() { curl -s "$API/v1/users/$1" | jq -r "$2"; }

# ---------------------------------------------------------------- setup
bold "Starting a test instance on port $PORT"
cd "$ROOT/apps/api"
go build -o "$TMP/api"       ./cmd/api
go build -o "$TMP/bootstrap" ./cmd/bootstrap
go build -o "$TMP/testimg"   ./cmd/testimg
"$TMP/bootstrap" --truncate > "$TMP/ids.txt"
ADA=$(awk '$1=="ada"{print $2}'     "$TMP/ids.txt")
BOLA=$(awk '$1=="bola"{print $2}'   "$TMP/ids.txt")
CHIDI=$(awk '$1=="chidi"{print $2}' "$TMP/ids.txt")
FUNKE=$(awk '$1=="funke"{print $2}' "$TMP/ids.txt")
MUSA=$(awk '$1=="musa"{print $2}'  "$TMP/ids.txt")

DATABASE_URL="$DB" PORT="$PORT" VISION_MODE=stub ENFORCE_VENUE_HOURS=false \
  MATCH_TTL=15m TRANSFER_TTL=30m SWEEP_INTERVAL=2s \
  "$TMP/api" > "$TMP/api.log" 2>&1 &
API_PID=$!
for _ in $(seq 1 40); do curl -sf "$API/health" >/dev/null 2>&1 && break; sleep 0.5; done
curl -sf "$API/health" >/dev/null || { echo "api failed to start:"; cat "$TMP/api.log"; exit 1; }
pass "api up"

# ---------------------------------------------------------------- escrow path
bold "Tier 0 — escrow settlement"
img 42 "$TMP/cash1.jpg"
curl -sX POST "$API/v1/pledge" -F senderId=$ADA -F recipientId=$BOLA \
  -F amountKobo=2000000 -F note="school fees" -F photo=@"$TMP/cash1.jpg" > "$TMP/p1.json"

expect "pledge accepted"        "$(jq -r '.accepted'          "$TMP/p1.json")" "true"
expect "counted 20 notes"       "$(jq -r '.vision.notes|length' "$TMP/p1.json")" "20"
expect "mode is escrow"         "$(jq -r '.transfer.mode'     "$TMP/p1.json")" "escrow"
expect "matched a counterparty" "$(jq -r '.transfer.state'    "$TMP/p1.json")" "matched"
expect "nearest counterparty"   "$(jq -r '.transfer.match.counterpartyName' "$TMP/p1.json")" "Chidi Nwosu"
expect "handover at a registered venue" "$(jq -r '.transfer.match.venueName' "$TMP/p1.json")" "Nwosu Provisions"

T1=$(jq -r '.transfer.id' "$TMP/p1.json")
CODE=$(jq -r '.transfer.match.handoverCode' "$TMP/p1.json")
expect "counterparty funds locked" "$(balance $CHIDI .escrowKobo)" "2000000"
expect "recipient not yet paid"    "$(balance $BOLA  .availableKobo)" "0"

bold "Replay guard"
img 42 "$TMP/cash1b.jpg"
R=$(curl -sX POST "$API/v1/pledge" -F senderId=$ADA -F recipientId=$BOLA \
      -F amountKobo=2000000 -F photo=@"$TMP/cash1b.jpg")
expect "same notes refused while the first transfer is open" \
  "$(echo "$R" | jq -r '.code')" "already_pledged"
info "$(echo "$R" | jq -r '.reason')"

bold "Handover"
R=$(curl -sX POST "$API/v1/transfers/$T1/confirm" -H 'Content-Type: application/json' \
      -d "{\"userId\":\"$CHIDI\",\"code\":\"000000\"}")
expect "wrong code refused" "$(echo "$R" | jq -r '.code')" "bad_code"

R=$(curl -sX POST "$API/v1/transfers/$T1/confirm" -H 'Content-Type: application/json' \
      -d "{\"userId\":\"$CHIDI\",\"code\":\"$CODE\"}")
expect "one-sided confirm does not settle" "$(echo "$R" | jq -r '.state')" "handover_pending"
expect "recipient still unpaid"            "$(balance $BOLA .availableKobo)" "0"

R=$(curl -sX POST "$API/v1/transfers/$T1/confirm" -H 'Content-Type: application/json' \
      -d "{\"userId\":\"$ADA\"}")
expect "both sides confirmed settles" "$(echo "$R" | jq -r '.state')" "settled"
expect "recipient paid less the fee"  "$(balance $BOLA  .availableKobo)" "1990000"
expect "counterparty escrow drained"  "$(balance $CHIDI .escrowKobo)"    "0"
expect "counterparty gave up cash"    "$(balance $CHIDI .availableKobo)" "3000000"

A=$(curl -s "$API/v1/ledger/audit")
expect "no unbalanced transactions" "$(echo "$A" | jq -r '.unbalancedTransactions')" "0"
expect "global ledger sum is zero"  "$(echo "$A" | jq -r '.globalSumKobo')" "0"
expect "fee collected"              "$(echo "$A" | jq -r '.revenueKobo')" "10000"
expect "no capital at risk"         "$(echo "$A" | jq -r '.capitalAtRiskKobo')" "0"

# ---------------------------------------------------------------- expiry
bold "Abandoned handover leaves nobody out of pocket"
# A second instance with very short windows, so the expiry path can be observed
# in seconds. It shares the database; its sweeper only touches records whose own
# deadline has passed, so the long-window transfers above are unaffected.
FAST_PORT=$((PORT + 1))
FAST="http://localhost:$FAST_PORT"
DATABASE_URL="$DB" PORT="$FAST_PORT" VISION_MODE=stub ENFORCE_VENUE_HOURS=false \
  MATCH_TTL=2s TRANSFER_TTL=3s SWEEP_INTERVAL=1s \
  "$TMP/api" > "$TMP/fast.log" 2>&1 &
FAST_PID=$!
for _ in $(seq 1 40); do curl -sf "$FAST/health" >/dev/null 2>&1 && break; sleep 0.5; done

post_cashout "$CHIDI" 2000000
BEFORE=$(balance $CHIDI .availableKobo)
img 91 "$TMP/cash2.jpg"
curl -sX POST "$FAST/v1/pledge" -F senderId=$ADA -F recipientId=$BOLA \
  -F amountKobo=2000000 -F photo=@"$TMP/cash2.jpg" > "$TMP/p2.json"
T2=$(jq -r '.transfer.id' "$TMP/p2.json")
expect "funds locked again" "$(balance $CHIDI .escrowKobo)" "2000000"

info "waiting out the 2s match window and the 3s transfer window..."
sleep 7
expect "transfer expired"         "$(curl -s "$API/v1/transfers/$T2" | jq -r '.state')" "expired"
expect "escrow released"          "$(balance $CHIDI .escrowKobo)" "0"
expect "counterparty made whole"  "$(balance $CHIDI .availableKobo)" "$BEFORE"
expect "recipient never credited" "$(balance $BOLA .availableKobo)" "1990000"
expect "still balanced after expiry" "$(curl -s "$API/v1/ledger/audit" | jq -r '.unbalancedTransactions')" "0"
{ kill "$FAST_PID" && wait "$FAST_PID"; } 2>/dev/null || true; FAST_PID=""

# ---------------------------------------------------------------- credit
bold "Tier 1 — instant credit is earned, not granted"
expect "no credit line at sign-up" "$(curl -s "$API/v1/users/$ADA" | jq -r '.creditLimitKobo')" "0"

# Chidi runs low on digital funds after each swap, so the second round is
# posted by Musa. This is the demand book working as intended.
for pair in "101 $CHIDI 6.4577 3.3855" "102 $MUSA 6.4650 3.3960"; do
  set -- $pair; seed=$1; who=$2
  post_cashout "$who" 2000000
  img $seed "$TMP/c$seed.jpg"
  curl -sX POST "$API/v1/pledge" -F senderId=$ADA -F recipientId=$BOLA \
    -F amountKobo=2000000 -F photo=@"$TMP/c$seed.jpg" > "$TMP/pp.json"
  TX=$(jq -r '.transfer.id' "$TMP/pp.json"); CC=$(jq -r '.transfer.match.handoverCode' "$TMP/pp.json")
  CP=$(jq -r '.transfer.match.counterpartyId' "$TMP/pp.json")
  curl -sX POST "$API/v1/transfers/$TX/confirm" -H 'Content-Type: application/json' \
    -d "{\"userId\":\"$CP\",\"code\":\"$CC\"}" >/dev/null
  curl -sX POST "$API/v1/transfers/$TX/confirm" -H 'Content-Type: application/json' \
    -d "{\"userId\":\"$ADA\"}" >/dev/null
done
expect "three settlements recorded" "$(curl -s "$API/v1/users/$ADA" | jq -r '.settledCount')" "3"
expect "credit line unlocked"       "$(curl -s "$API/v1/users/$ADA" | jq -r '.creditLimitKobo')" "500000"

BEFORE=$(balance $BOLA .availableKobo)
img 200 "$TMP/small.jpg"
curl -sX POST "$API/v1/pledge" -F senderId=$ADA -F recipientId=$BOLA \
  -F amountKobo=500000 -F photo=@"$TMP/small.jpg" > "$TMP/p3.json"
T3=$(jq -r '.transfer.id' "$TMP/p3.json")
expect "mode is credit"            "$(jq -r '.transfer.mode' "$TMP/p3.json")" "credit"
AFTER=$(balance $BOLA .availableKobo)
expect "recipient paid at snap time" "$AFTER" "$((BEFORE + 497500))"
expect "platform now carries risk"   "$(curl -s "$API/v1/ledger/audit" | jq -r '.capitalAtRiskKobo')" "500000"

CODE3=$(curl -s "$API/v1/transfers/$T3" | jq -r '.match.handoverCode')
CP3=$(curl -s "$API/v1/transfers/$T3" | jq -r '.match.counterpartyId')
curl -sX POST "$API/v1/transfers/$T3/confirm" -H 'Content-Type: application/json' \
  -d "{\"userId\":\"$CP3\",\"code\":\"$CODE3\"}" >/dev/null
R=$(curl -sX POST "$API/v1/transfers/$T3/confirm" -H 'Content-Type: application/json' \
      -d "{\"userId\":\"$ADA\"}")
expect "credit transfer settles"  "$(echo "$R" | jq -r '.state')" "settled"
expect "obligation cleared"       "$(curl -s "$API/v1/users/$ADA" | jq -r '.owedKobo')" "0"
A=$(curl -s "$API/v1/ledger/audit")
expect "risk back to zero"        "$(echo "$A" | jq -r '.capitalAtRiskKobo')" "0"
expect "ledger balanced at close" "$(echo "$A" | jq -r '.unbalancedTransactions')" "0"
expect "global sum still zero"    "$(echo "$A" | jq -r '.globalSumKobo')" "0"

# ---------------------------------------------------------------- safety
bold "The demand book is not public"
R=$(curl -s "$API/v1/cashouts?userId=$ADA")
expect "a sender sees nobody else's cash requests" "$(echo "$R" | jq -r 'length')" "0"
expect "an operator sees their own"                "$(curl -s "$API/v1/cashouts?userId=$FUNKE" | jq -r 'length >= 0')" "true"
expect "listing requires an identity"              "$(curl -s "$API/v1/cashouts" | jq -r '.code')" "missing_user"

bold "Cash can only be collected from premises you operate"
VID=$(curl -s "$API/v1/venues?operatorId=$CHIDI" | jq -r '.[0].id')
R=$(curl -sX POST "$API/v1/cashouts" -H 'Content-Type: application/json' \
      -d "{\"userId\":\"$FUNKE\",\"amountKobo\":100000,\"venueId\":\"$VID\"}")
expect "cannot post against somebody else's venue" "$(echo "$R" | jq -r '.code')" "not_your_venue"

bold "A single handover is capped"
img 300 "$TMP/big.jpg"
R=$(curl -sX POST "$API/v1/pledge" -F senderId=$ADA -F recipientId=$BOLA \
      -F amountKobo=50000000 -F photo=@"$TMP/big.jpg")
expect "an oversized handover is refused" "$(echo "$R" | jq -r '.code')" "amount_too_large"

# ---------------------------------------------------------------- incidents
bold "A reported incident holds the money instead of releasing it"
post_cashout "$MUSA" 2000000
img 400 "$TMP/inc.jpg"
curl -sX POST "$API/v1/pledge" -F senderId=$ADA -F recipientId=$BOLA \
  -F amountKobo=2000000 -F photo=@"$TMP/inc.jpg" > "$TMP/p4.json"
T4=$(jq -r '.transfer.id' "$TMP/p4.json")
CP4=$(jq -r '.transfer.match.counterpartyId' "$TMP/p4.json")
expect "matched for the incident case" "$(jq -r '.transfer.match.counterpartyName' "$TMP/p4.json")" "Musa Ibrahim"
expect "counterparty funds locked"     "$(balance $CP4 .escrowKobo)" "2000000"

# Ada hands over the cash; the counterparty takes it and never confirms.
R=$(curl -sX POST "$API/v1/transfers/$T4/incident" -H 'Content-Type: application/json' \
      -d "{\"userId\":\"$ADA\",\"kind\":\"cash_taken\",\"detail\":\"handed over, no confirmation\"}")
expect "the report freezes escrow"  "$(echo "$R" | jq -r '.frozeEscrow')" "true"
expect "transfer is disputed"       "$(curl -s "$API/v1/transfers/$T4" | jq -r '.state')" "disputed"
expect "funds stay locked"          "$(balance $CP4 .escrowKobo)" "2000000"
expect "accused is suspended"       "$(curl -s "$API/v1/users/$CP4" | jq -r '.suspended')" "true"
expect "duplicate report refused"   "$(curl -sX POST "$API/v1/transfers/$T4/incident" -H 'Content-Type: application/json' -d "{\"userId\":\"$ADA\",\"kind\":\"cash_taken\"}" | jq -r '.code')" "already_reported"

info "waiting through two sweep cycles to prove the hold survives expiry..."
sleep 5
expect "the sweeper does not release a disputed hold" "$(balance $CP4 .escrowKobo)" "2000000"
expect "still disputed"                               "$(curl -s "$API/v1/transfers/$T4" | jq -r '.state')" "disputed"
expect "a suspended operator cannot post cash requests" \
  "$(curl -sX POST "$API/v1/cashouts" -H 'Content-Type: application/json' -d "{\"userId\":\"$CP4\",\"amountKobo\":100000,\"venueId\":\"$(curl -s "$API/v1/venues?operatorId=$CP4" | jq -r '.[0].id')\"}" | jq -r '.code')" "suspended"
expect "ledger still balanced" "$(curl -s "$API/v1/ledger/audit" | jq -r '.unbalancedTransactions')" "0"

bold "Result"
if [ "$FAILED" = "0" ]; then
  printf '  \033[32mall checks passed\033[0m\n\n'
else
  printf '  \033[31msome checks failed\033[0m\n\n'
  echo "--- api log (last 25 lines) ---"; tail -25 "$TMP/api.log"
  exit 1
fi
