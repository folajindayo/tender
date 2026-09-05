package fintava

import (
	"encoding/json"
	"testing"
)

// The shape Fintava actually returns from name enquiry. The published reference
// documents this response as an empty object, so this test is the only record
// of it -- it came from a live call, and an earlier decoder that assumed the
// payload sat under "data" reported every real account as missing.
const liveNameEnquiry = `{
  "status": 200,
  "account": {
    "accountName": "TEMIDAYO FOLAJIN",
    "accountNumber": "5655858793",
    "bankCode": "090405",
    "responseCode": "00"
  }
}`

func decode(t *testing.T, body string) map[string]any {
	t.Helper()
	var fields map[string]any
	if err := json.Unmarshal([]byte(body), &fields); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return fields
}

func TestPickStringFindsNameNestedUnderAccount(t *testing.T) {
	fields := decode(t, liveNameEnquiry)

	if got := pickString(fields, "accountName", "account_name", "name"); got != "TEMIDAYO FOLAJIN" {
		t.Errorf("accountName = %q, want the name nested under \"account\"", got)
	}
	if got := pickString(fields, "accountNumber", "account_number"); got != "5655858793" {
		t.Errorf("accountNumber = %q", got)
	}
	if got := pickString(fields, "sortCode", "sort_code", "bankCode"); got != "090405" {
		t.Errorf("sortCode = %q", got)
	}
}

// Whichever wrapper a provider picks, the field is found. Guessing one name was
// the original bug.
func TestPickStringIsIndifferentToTheWrapper(t *testing.T) {
	for _, body := range []string{
		`{"accountName":"ADA OKAFOR"}`,
		`{"data":{"accountName":"ADA OKAFOR"}}`,
		`{"account":{"accountName":"ADA OKAFOR"}}`,
		`{"status":200,"result":{"details":{"accountName":"ADA OKAFOR"}}}`,
	} {
		if got := pickString(decode(t, body), "accountName"); got != "ADA OKAFOR" {
			t.Errorf("%s -> %q, want ADA OKAFOR", body, got)
		}
	}
}

// An outer field beats one buried deeper, so a summary value cannot be
// shadowed by an unrelated sub-object that happens to share a key name.
func TestPickStringPrefersTheOutermostMatch(t *testing.T) {
	body := `{"accountName":"OUTER","account":{"accountName":"INNER"}}`
	if got := pickString(decode(t, body), "accountName"); got != "OUTER" {
		t.Errorf("got %q, want OUTER", got)
	}
}

func TestPickStringReportsNothingWhenAbsent(t *testing.T) {
	body := `{"status":400,"account":{"responseCode":"07"}}`
	if got := pickString(decode(t, body), "accountName", "name"); got != "" {
		t.Errorf("got %q, want empty so the caller can report it honestly", got)
	}
}

// Money is read the same way, and must survive the same nesting.
func TestPickKoboFindsNestedAmounts(t *testing.T) {
	for _, tc := range []struct {
		body string
		want int64
	}{
		{`{"amount":1500.50}`, 150050},
		{`{"data":{"amount":"20000.00"}}`, 2000000},
		{`{"transaction":{"amount":0.01}}`, 1},
	} {
		if got := int64(pickKobo(decode(t, tc.body), "amount")); got != tc.want {
			t.Errorf("%s -> %d kobo, want %d", tc.body, got, tc.want)
		}
	}
}

// Fintava answers a refused key with HTTP 404 and "Invalid API Key" rather than
// a 401, so status alone cannot tell a rejected credential from a missing
// route. Getting that wrong sent us hunting a network fault for half an hour.
func TestAuthRejectionIsRecognisedDespiteTheStatus(t *testing.T) {
	rejected := []struct {
		status int
		body   string
	}{
		{404, `{"status":404,"message":["Invalid API Key"],"path":"/api/dev/banks"}`},
		{404, `{"message":["invalid api key"]}`},
		{401, `{"message":"nope"}`},
		{403, `{"message":"forbidden"}`},
		{400, `{"message":"Unauthorized"}`},
	}
	for _, tc := range rejected {
		if !isAuthRejection(tc.status, []byte(tc.body)) {
			t.Errorf("http %d %s: should read as a refused credential", tc.status, tc.body)
		}
	}

	// A real 404 must stay a 404: telling an operator to check a key that is
	// fine is the same wrong turn in the other direction.
	notAuth := []struct {
		status int
		body   string
	}{
		{404, `{"status":404,"message":["Account not found"]}`},
		{400, `{"message":["email must be an email"]}`},
		{500, `{"message":"internal error"}`},
	}
	for _, tc := range notAuth {
		if isAuthRejection(tc.status, []byte(tc.body)) {
			t.Errorf("http %d %s: should NOT read as a refused credential", tc.status, tc.body)
		}
	}
}

// Whether a payout dies or waits turns on this. The live case: the float was
// short by a few naira, Fintava said "Account balance is insufficient", and the
// payout was marked failed and the money returned -- cancelling a transfer that
// a top-up would have completed.
func TestTemporaryRefusalsAreToldApartFromFinalOnes(t *testing.T) {
	temporary := []struct {
		status int
		body   string
	}{
		{400, `{"status":400,"message":["Account balance is insufficient"],"path":"/api/dev/bank/credit/merchant"}`},
		{429, `{"message":"Too Many Requests"}`},
		{500, `{"message":"internal"}`},
		{503, `{"message":"Service Unavailable"}`},
		{400, `{"message":["Please try again later"]}`},
	}
	for _, tc := range temporary {
		if !isTemporary(tc.status, []byte(tc.body)) {
			t.Errorf("http %d %s: should wait and retry, not fail the payout", tc.status, tc.body)
		}
	}

	// A refusal about the account itself is final. Retrying it forever would
	// leave the sender's money in limbo instead of giving it back.
	final := []struct {
		status int
		body   string
	}{
		{400, `{"message":["Invalid account number"]}`},
		{400, `{"message":["Account does not exist"]}`},
		{400, `{"message":["email must be an email"]}`},
		{404, `{"message":["Account not found"]}`},
	}
	for _, tc := range final {
		if isTemporary(tc.status, []byte(tc.body)) {
			t.Errorf("http %d %s: waiting cannot fix this; the money should go back",
				tc.status, tc.body)
		}
	}
}
