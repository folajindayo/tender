package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"tender/api/internal/domain"
	"tender/api/internal/money"
	"tender/api/internal/settle"
)

const maxUpload = 12 << 20 // phones produce large photos even after downscaling

func (a *API) getUser(w http.ResponseWriter, r *http.Request) {
	id, err := uuidParam(r, "id")
	if err != nil {
		fail(w, err)
		return
	}
	u, err := a.Store.GetUser(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func (a *API) userTransfers(w http.ResponseWriter, r *http.Request) {
	id, err := uuidParam(r, "id")
	if err != nil {
		fail(w, err)
		return
	}
	ts, err := a.Store.TransfersForUser(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	if ts == nil {
		ts = []domain.Transfer{} // keep JSON as [] rather than null
	}
	writeJSON(w, http.StatusOK, ts)
}

func (a *API) getTransfer(w http.ResponseWriter, r *http.Request) {
	id, err := uuidParam(r, "id")
	if err != nil {
		fail(w, err)
		return
	}
	t, err := a.Store.GetTransfer(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// listCashouts returns only the caller's own requests.
//
// There is no endpoint that lists everybody's, by design. An open request means
// "this person will be holding cash, at this place, shortly" -- publishing the
// book was a target list that did not even require using the app to read.
func (a *API) listCashouts(w http.ResponseWriter, r *http.Request) {
	uid, err := uuid.Parse(r.URL.Query().Get("userId"))
	if err != nil {
		fail(w, &settle.Rejection{Code: "missing_user",
			Reason: "A userId is required to list cash requests."})
		return
	}
	cs, err := a.Store.CashoutsFor(r.Context(), uid)
	if err != nil {
		fail(w, err)
		return
	}
	if cs == nil {
		cs = []domain.CashoutRequest{}
	}
	writeJSON(w, http.StatusOK, cs)
}

// listVenues returns the premises an operator runs, or every verified venue when
// no operator is named. Venue addresses are public -- they are shopfronts -- but
// which of them currently wants cash is not.
func (a *API) listVenues(w http.ResponseWriter, r *http.Request) {
	var (
		vs  []domain.Venue
		err error
	)
	if q := r.URL.Query().Get("operatorId"); q != "" {
		id, parseErr := uuid.Parse(q)
		if parseErr != nil {
			fail(w, parseErr)
			return
		}
		vs, err = a.Store.VenuesFor(r.Context(), id)
	} else {
		vs, err = a.Store.ActiveVenues(r.Context())
	}
	if err != nil {
		fail(w, err)
		return
	}
	if vs == nil {
		vs = []domain.Venue{}
	}
	writeJSON(w, http.StatusOK, vs)
}

type cashoutBody struct {
	UserID    string `json:"userId"`
	Amount    int64  `json:"amountKobo"`
	Tolerance int64  `json:"toleranceKobo"`
	VenueID   string `json:"venueId"`
}

// createCashout posts a request for physical cash at premises the requester
// actually operates. This is the supply side, and the reason the platform never
// has to handle notes itself.
//
// The venue must be one the caller runs. Letting anybody nominate any address --
// or worse, type one in -- is what turns a cash-out request into a lure.
func (a *API) createCashout(w http.ResponseWriter, r *http.Request) {
	var b cashoutBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&b); err != nil {
		fail(w, err)
		return
	}
	uid, err := uuid.Parse(b.UserID)
	if err != nil {
		fail(w, err)
		return
	}
	venueID, err := uuid.Parse(b.VenueID)
	if err != nil {
		fail(w, &settle.Rejection{Code: "no_venue",
			Reason: "Choose which of your registered locations the cash should be collected from."})
		return
	}
	if b.Amount <= 0 {
		fail(w, &settle.Rejection{Code: "bad_amount", Reason: "Enter an amount greater than zero."})
		return
	}

	user, err := a.Store.GetUser(r.Context(), uid)
	if err != nil {
		fail(w, err)
		return
	}
	if user.Suspended {
		fail(w, &settle.Rejection{Code: "suspended",
			Reason: "This account is suspended while a reported incident is reviewed."})
		return
	}
	if money.Kobo(b.Amount) > user.Available {
		fail(w, &settle.Rejection{Code: "insufficient_funds",
			Reason: "You cannot ask for more cash than you can pay for."})
		return
	}

	var ok bool
	err = a.Store.Pool.QueryRow(r.Context(), `
		SELECT true FROM venues
		 WHERE id = $1 AND operator_id = $2 AND active AND verified`,
		venueID, uid).Scan(&ok)
	if err != nil {
		fail(w, &settle.Rejection{Code: "not_your_venue",
			Reason: "Cash can only be collected from a verified location you operate."})
		return
	}

	var id uuid.UUID
	err = a.Store.Pool.QueryRow(r.Context(), `
		INSERT INTO cashout_requests (user_id, amount_kobo, tolerance_kobo, venue_id)
		VALUES ($1,$2,$3,$4) RETURNING id`,
		uid, b.Amount, b.Tolerance, venueID).Scan(&id)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

type incidentBody struct {
	UserID string `json:"userId"`
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
}

// reportIncident holds the money when a handover goes wrong.
func (a *API) reportIncident(w http.ResponseWriter, r *http.Request) {
	id, err := uuidParam(r, "id")
	if err != nil {
		fail(w, err)
		return
	}
	var b incidentBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&b); err != nil {
		fail(w, err)
		return
	}
	uid, err := uuid.Parse(b.UserID)
	if err != nil {
		fail(w, err)
		return
	}
	inc, err := a.Service.ReportIncident(r.Context(), id, uid, b.Kind, b.Detail)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, inc)
}

// pledge accepts a photograph of cash, either as multipart (what phones send)
// or as a base64 JSON body (what scripts send).
func (a *API) pledge(w http.ResponseWriter, r *http.Request) {
	req, err := parsePledge(r)
	if err != nil {
		fail(w, err)
		return
	}

	res, err := a.Service.Pledge(r.Context(), *req)
	if err != nil {
		fail(w, err)
		return
	}
	status := http.StatusCreated
	if !res.Accepted {
		status = http.StatusOK // a refusal is a normal outcome, not an error
	}
	writeJSON(w, status, res)
}

func parsePledge(r *http.Request) (*settle.PledgeRequest, error) {
	ct := r.Header.Get("Content-Type")

	if strings.HasPrefix(ct, "multipart/form-data") {
		if err := r.ParseMultipartForm(maxUpload); err != nil {
			return nil, fmt.Errorf("read upload: %w", err)
		}
		file, _, err := r.FormFile("photo")
		if err != nil {
			return nil, fmt.Errorf("no photo attached: %w", err)
		}
		defer file.Close()

		raw, err := io.ReadAll(io.LimitReader(file, maxUpload))
		if err != nil {
			return nil, err
		}
		return buildPledge(pledgeFields{
			Sender:        r.FormValue("senderId"),
			Recipient:     r.FormValue("recipientId"),
			AccountNumber: r.FormValue("accountNumber"),
			SortCode:      r.FormValue("sortCode"),
			AccountName:   r.FormValue("accountName"),
			BankName:      r.FormValue("bankName"),
			Amount:        r.FormValue("amountKobo"),
			Note:          r.FormValue("note"),
		}, raw)
	}

	var b struct {
		SenderID    string `json:"senderId"`
		RecipientID string `json:"recipientId"`
		// A bank destination. The account name is the one the bank returned
		// from name enquiry, not free text the sender chose.
		AccountNumber string `json:"accountNumber"`
		SortCode      string `json:"sortCode"`
		AccountName   string `json:"accountName"`
		BankName      string `json:"bankName"`

		AmountKobo  int64  `json:"amountKobo"`
		Note        string `json:"note"`
		PhotoBase64 string `json:"photoBase64"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxUpload*2)).Decode(&b); err != nil {
		return nil, err
	}
	// Tolerate a data: URL prefix from the browser.
	if i := strings.Index(b.PhotoBase64, ","); i >= 0 && strings.HasPrefix(b.PhotoBase64, "data:") {
		b.PhotoBase64 = b.PhotoBase64[i+1:]
	}
	raw, err := base64.StdEncoding.DecodeString(b.PhotoBase64)
	if err != nil {
		return nil, fmt.Errorf("photo is not valid base64: %w", err)
	}
	return buildPledge(pledgeFields{
		Sender:        b.SenderID,
		Recipient:     b.RecipientID,
		AccountNumber: b.AccountNumber,
		SortCode:      b.SortCode,
		AccountName:   b.AccountName,
		BankName:      b.BankName,
		Amount:        fmt.Sprintf("%d", b.AmountKobo),
		Note:          b.Note,
	}, raw)
}

// pledgeFields is the union of both request encodings: multipart from the
// camera, JSON from everything else.
type pledgeFields struct {
	Sender, Recipient                              string
	AccountNumber, SortCode, AccountName, BankName string
	Amount, Note                                   string
}

func buildPledge(f pledgeFields, raw []byte) (*settle.PledgeRequest, error) {
	sid, err := uuid.Parse(f.Sender)
	if err != nil {
		return nil, fmt.Errorf("bad senderId: %w", err)
	}
	var kobo int64
	if _, err := fmt.Sscanf(f.Amount, "%d", &kobo); err != nil {
		return nil, fmt.Errorf("bad amount: %w", err)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("the photograph is empty")
	}

	req := &settle.PledgeRequest{
		SenderID: sid,
		Amount:   money.Kobo(kobo),
		Note:     f.Note,
		Image:    raw,
	}

	// A bank destination wins when present; the service rejects a request that
	// somehow carries both.
	if f.AccountNumber != "" {
		req.Bank = &domain.BankAccount{
			AccountNumber: strings.TrimSpace(f.AccountNumber),
			SortCode:      strings.TrimSpace(f.SortCode),
			AccountName:   strings.TrimSpace(f.AccountName),
			BankName:      strings.TrimSpace(f.BankName),
		}
		return req, nil
	}
	if f.Recipient != "" {
		rid, err := uuid.Parse(f.Recipient)
		if err != nil {
			return nil, fmt.Errorf("bad recipientId: %w", err)
		}
		req.RecipientID = &rid
	}
	return req, nil
}

func (a *API) rematch(w http.ResponseWriter, r *http.Request) {
	id, err := uuidParam(r, "id")
	if err != nil {
		fail(w, err)
		return
	}
	m, err := a.Service.TryMatch(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	if m == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"matched": false,
			"reason":  "Nobody nearby wants this amount in cash right now. Still looking.",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"matched": true, "match": m})
}

type confirmBody struct {
	UserID string `json:"userId"`
	Code   string `json:"code"`
	Reason string `json:"reason"`
}

func (a *API) confirm(w http.ResponseWriter, r *http.Request) {
	id, err := uuidParam(r, "id")
	if err != nil {
		fail(w, err)
		return
	}
	var b confirmBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		fail(w, err)
		return
	}
	uid, err := uuid.Parse(b.UserID)
	if err != nil {
		fail(w, err)
		return
	}
	t, err := a.Service.ConfirmHandover(r.Context(), id, uid, strings.TrimSpace(b.Code))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (a *API) rejectHandover(w http.ResponseWriter, r *http.Request) {
	id, err := uuidParam(r, "id")
	if err != nil {
		fail(w, err)
		return
	}
	var b confirmBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		fail(w, err)
		return
	}
	uid, err := uuid.Parse(b.UserID)
	if err != nil {
		fail(w, err)
		return
	}
	t, err := a.Service.RejectHandover(r.Context(), id, uid, b.Reason)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (a *API) audit(w http.ResponseWriter, r *http.Request) {
	audit, err := a.Store.LedgerAudit(r.Context(), 60)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, audit)
}

type createUserBody struct {
	Phone       string  `json:"phone"`
	DisplayName string  `json:"displayName"`
	AvatarEmoji string  `json:"avatarEmoji"`
	City        string  `json:"city"`
	Lat         float64 `json:"lat"`
	Lng         float64 `json:"lng"`
}

// createUser registers an account against a phone number. New accounts start
// with no balance and no credit line: the credit limit is earned by settling,
// never granted at sign-up.
func (a *API) createUser(w http.ResponseWriter, r *http.Request) {
	var b createUserBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&b); err != nil {
		fail(w, err)
		return
	}
	b.Phone = strings.TrimSpace(b.Phone)
	b.DisplayName = strings.TrimSpace(b.DisplayName)
	if b.Phone == "" || b.DisplayName == "" {
		fail(w, &settle.Rejection{Code: "missing_fields",
			Reason: "A phone number and a name are both required."})
		return
	}
	if b.AvatarEmoji == "" {
		b.AvatarEmoji = "🙂"
	}

	var id uuid.UUID
	err := a.Store.Pool.QueryRow(r.Context(), `
		INSERT INTO users (phone, display_name, avatar_emoji, city, lat, lng)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (phone) DO UPDATE SET display_name = users.display_name
		RETURNING id`,
		b.Phone, b.DisplayName, b.AvatarEmoji, b.City, b.Lat, b.Lng).Scan(&id)
	if err != nil {
		fail(w, err)
		return
	}
	u, err := a.Store.GetUser(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, u)
}
