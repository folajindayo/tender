package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"tender/api/internal/ledger"
	"tender/api/internal/money"
)

// Premises a handover may happen at. A venue is fixed, publicly known, and
// answerable to somebody -- never a coordinate typed in for one transaction.
var venueKinds = map[string]bool{
	"agent": true, "bank": true, "filling_station": true, "market_office": true,
}

// registerVenue lets an operator put their premises forward.
//
// It creates the venue unverified, and that is deliberate. Matching only ever
// proposes verified venues, so self-registration cannot put a stranger's
// address into circulation as a place to carry cash to. Registering is a claim;
// verification is somebody accepting it.
func (a *API) registerVenue(w http.ResponseWriter, r *http.Request) {
	user, err := a.userFor(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, errBody(errors.New("sign in to register premises")))
		return
	}

	var b struct {
		Name     string  `json:"name"`
		Kind     string  `json:"kind"`
		Address  string  `json:"address"`
		Lat      float64 `json:"lat"`
		Lng      float64 `json:"lng"`
		OpensAt  string  `json:"opensAt"`
		ClosesAt string  `json:"closesAt"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}

	b.Name = strings.TrimSpace(b.Name)
	b.Address = strings.TrimSpace(b.Address)
	b.Kind = strings.TrimSpace(b.Kind)
	if b.Kind == "" {
		b.Kind = "agent"
	}
	switch {
	case b.Name == "":
		writeJSON(w, http.StatusBadRequest, errBody(errors.New("name the premises")))
		return
	case b.Address == "":
		writeJSON(w, http.StatusBadRequest,
			errBody(errors.New("an address is required: somebody has to be able to find it")))
		return
	case !venueKinds[b.Kind]:
		writeJSON(w, http.StatusBadRequest,
			errBody(errors.New("kind must be agent, bank, filling_station or market_office")))
		return
	// Nigeria spans roughly 4-14N, 2-15E. A zero pair is the giveaway that
	// coordinates were never filled in, and it would put the premises in the
	// Atlantic -- which the distance sort would then treat as a real place.
	case b.Lat < 4 || b.Lat > 14 || b.Lng < 2 || b.Lng > 15:
		writeJSON(w, http.StatusBadRequest,
			errBody(errors.New("latitude and longitude must be inside Nigeria")))
		return
	}

	opens, closes := strings.TrimSpace(b.OpensAt), strings.TrimSpace(b.ClosesAt)
	if opens == "" {
		opens = "08:00"
	}
	if closes == "" {
		closes = "18:00"
	}

	var id uuid.UUID
	err = a.Store.Pool.QueryRow(r.Context(), `
		INSERT INTO venues (name, kind, address, lat, lng, operator_id, opens_at, closes_at, verified)
		VALUES ($1,$2::venue_kind,$3,$4,$5,$6,$7::time,$8::time,false)
		RETURNING id`,
		b.Name, b.Kind, b.Address, b.Lat, b.Lng, user.ID, opens, closes).Scan(&id)
	if err != nil {
		fail(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"id":       id,
		"name":     b.Name,
		"kind":     b.Kind,
		"address":  b.Address,
		"verified": false,
		"note": "Registered, and not yet verified. Handovers are only ever proposed " +
			"at verified premises, so this cannot be matched against until somebody " +
			"has checked it.",
	})
}

// verifyVenue accepts an operator's claim about their premises.
//
// Operator-only, because a verified venue is where the product tells two
// strangers to meet and hand over cash. Letting the person who registered it
// also vouch for it would make the check ceremonial.
func (a *API) verifyVenue(w http.ResponseWriter, r *http.Request) {
	if err := a.operatorOnly(r); err != nil {
		writeJSON(w, http.StatusUnauthorized, errBody(err))
		return
	}
	id, err := uuidParam(r, "id")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}

	var b struct {
		Verified *bool `json:"verified"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&b)
	verified := true
	if b.Verified != nil {
		verified = *b.Verified
	}

	var name string
	err = a.Store.Pool.QueryRow(r.Context(), `
		UPDATE venues SET verified = $2 WHERE id = $1 RETURNING name`, id, verified).Scan(&name)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errBody(errors.New("no such venue")))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "name": name, "verified": verified})
}

// allocateToUser puts a counterparty in funds from the platform's own float.
//
// Operator-only, and it moves rather than mints: the float goes down by exactly
// what the balance goes up by, so the wallet at the bank still backs every claim
// in the books. This is how the demand side exists before there are customers
// paying money in -- the platform lends its capital to somebody willing to hand
// out cash.
func (a *API) allocateToUser(w http.ResponseWriter, r *http.Request) {
	if err := a.operatorOnly(r); err != nil {
		writeJSON(w, http.StatusUnauthorized, errBody(err))
		return
	}
	id, err := uuidParam(r, "id")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}

	var b struct {
		AmountKobo int64  `json:"amountKobo"`
		Reference  string `json:"reference"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	if b.AmountKobo <= 0 {
		writeJSON(w, http.StatusBadRequest, errBody(errors.New("say how much, in kobo")))
		return
	}
	ref := strings.TrimSpace(b.Reference)
	if ref == "" {
		ref = "manual-" + strconv.FormatInt(b.AmountKobo, 10)
	}

	// Refuse rather than overdraw the float. The books would still balance --
	// that is what double entry guarantees -- but a negative float means the
	// platform has promised more than the bank is holding.
	audit, err := a.Store.LedgerAudit(r.Context(), 1)
	if err != nil {
		fail(w, err)
		return
	}
	if money.Kobo(b.AmountKobo) > audit.FloatKobo {
		writeJSON(w, http.StatusBadRequest, errBody(errors.New(
			"that is more than the float holds; top the float up first")))
		return
	}

	if err := ledger.AllocateFromFloat(
		r.Context(), a.Store.Pool, id, money.Kobo(b.AmountKobo), ref); err != nil {
		fail(w, err)
		return
	}

	user, err := a.Store.GetUser(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	after, _ := a.Store.LedgerAudit(r.Context(), 1)
	writeJSON(w, http.StatusOK, map[string]any{
		"userId":        id,
		"availableKobo": user.Available,
		"floatKobo":     after.FloatKobo,
	})
}
