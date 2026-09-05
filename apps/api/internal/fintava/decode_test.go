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
