package domain

import (
	"time"

	"github.com/google/uuid"
	"tender/api/internal/money"
)

// Transfer states. See db/migrations/0002_settlement.sql for the enum.
const (
	StateDraft           = "draft"
	StatePledged         = "pledged"
	StateCredited        = "credited"
	StateMatched         = "matched"
	StateHandoverPending = "handover_pending"
	StateSettled         = "settled"
	StateExpired         = "expired"
	StateRejected        = "rejected"
	StateVoided          = "voided"
	StateDefaulted       = "defaulted"
)

const (
	ModeEscrow = "escrow"
	ModeCredit = "credit"
)

const (
	MatchProposed              = "proposed"
	MatchSenderConfirmed       = "sender_confirmed"
	MatchCounterpartyConfirmed = "counterparty_confirmed"
	MatchCompleted             = "completed"
	MatchExpired               = "expired"
	MatchRejected              = "rejected"
	MatchDisputed              = "disputed"
)

// Incident kinds. Anything other than a no-show holds the counterparty's escrow
// rather than releasing it, because a silent non-confirmation is exactly what a
// theft looks like from the outside.
const (
	IncidentCashTaken   = "cash_taken"
	IncidentWrongAmount = "wrong_amount"
	IncidentThreatened  = "threatened"
	IncidentNoShow      = "no_show"
)

// FreezesEscrow reports whether a report of this kind should hold funds pending
// review. A no-show is ordinary and settles itself through expiry.
func FreezesEscrow(kind string) bool { return kind != IncidentNoShow }

type User struct {
	ID             uuid.UUID  `json:"id"`
	Phone          string     `json:"phone"`
	DisplayName    string     `json:"displayName"`
	AvatarEmoji    string     `json:"avatarEmoji"`
	City           string     `json:"city"`
	Lat            float64    `json:"lat"`
	Lng            float64    `json:"lng"`
	TrustScore     int        `json:"trustScore"`
	SettledCount   int        `json:"settledCount"`
	DefaultedCount int        `json:"defaultedCount"`
	CreditLimit    money.Kobo `json:"creditLimitKobo"`
	SendingFrozen  bool       `json:"sendingFrozen"`
	Suspended      bool       `json:"suspended"`
	IncidentCount  int        `json:"incidentCount"`

	Available money.Kobo `json:"availableKobo"`
	Escrow    money.Kobo `json:"escrowKobo"`
	Owed      money.Kobo `json:"owedKobo"` // positive number = amount owed
}

// BankAccount is a destination outside Tender: the recipient of a transfer is
// normally somebody's existing bank account, and they need no Tender account of
// their own. AccountName is what the bank returned for the number, never what
// the sender typed -- it is the thing the sender checks before handing cash over.
type BankAccount struct {
	AccountNumber string `json:"accountNumber"`
	AccountName   string `json:"accountName"`
	SortCode      string `json:"sortCode"`
	BankName      string `json:"bankName,omitempty"`
}

type Transfer struct {
	ID       uuid.UUID `json:"id"`
	Ref      int64     `json:"ref"`
	SenderID uuid.UUID `json:"senderId"`

	// Exactly one of these is set. A transfer either credits another Tender
	// account or pays out to a bank account.
	RecipientID *uuid.UUID   `json:"recipientId,omitempty"`
	Bank        *BankAccount `json:"bank,omitempty"`

	Amount    money.Kobo `json:"amountKobo"`
	Fee       money.Kobo `json:"feeKobo"`
	Mode      string     `json:"mode"`
	State     string     `json:"state"`
	Note      string     `json:"note"`
	CreatedAt time.Time  `json:"createdAt"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	SettledAt *time.Time `json:"settledAt,omitempty"`

	SenderName    string  `json:"senderName,omitempty"`
	RecipientName string  `json:"recipientName,omitempty"`
	Payout        *Payout `json:"payout,omitempty"`
	Match         *Match  `json:"match,omitempty"`
}

// Delivered is what the recipient actually receives: the pledged amount less
// the platform fee, because the sender hands over whole banknotes.
func (t Transfer) Delivered() money.Kobo { return t.Amount - t.Fee }

type Match struct {
	ID               uuid.UUID  `json:"id"`
	TransferID       uuid.UUID  `json:"transferId"`
	CounterpartyID   uuid.UUID  `json:"counterpartyId"`
	CounterpartyName string     `json:"counterpartyName"`
	Amount           money.Kobo `json:"amountKobo"`
	HandoverCode     string     `json:"handoverCode"`
	DistanceM        int        `json:"distanceM"`
	VenueName        string     `json:"venueName"`
	VenueAddress     string     `json:"venueAddress"`
	State            string     `json:"state"`
	ExpiresAt        time.Time  `json:"expiresAt"`
}

// Venue is a place a handover may happen: fixed premises, publicly known, with
// an operator who is accountable for them.
type Venue struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	Kind     string    `json:"kind"`
	Address  string    `json:"address"`
	Lat      float64   `json:"lat"`
	Lng      float64   `json:"lng"`
	OpensAt  string    `json:"opensAt"`
	ClosesAt string    `json:"closesAt"`
	Verified bool      `json:"verified"`
	Open     bool      `json:"openNow"`
}

type CashoutRequest struct {
	ID        uuid.UUID  `json:"id"`
	UserID    uuid.UUID  `json:"userId"`
	UserName  string     `json:"userName"`
	Amount    money.Kobo `json:"amountKobo"`
	Tolerance money.Kobo `json:"toleranceKobo"`
	VenueID   uuid.UUID  `json:"venueId"`
	VenueName string     `json:"venueName"`
	Address   string     `json:"address"`
	State     string     `json:"state"`
}

type Incident struct {
	ID          uuid.UUID `json:"id"`
	TransferID  uuid.UUID `json:"transferId"`
	ReporterID  uuid.UUID `json:"reporterId"`
	AccusedID   uuid.UUID `json:"accusedId"`
	Kind        string    `json:"kind"`
	Detail      string    `json:"detail"`
	FrozeEscrow bool      `json:"frozeEscrow"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"createdAt"`
}

// Note is a single banknote identified inside a pledge photograph.
type Note struct {
	Denomination     money.Kobo `json:"denominationKobo"`
	Serial           string     `json:"serial"`
	SerialConfidence float64    `json:"serialConfidence"`
	PHash            string     `json:"phash"`
}

// Pledge is the outcome of photographing cash. It is evidence and a fraud
// signal -- it never secures value on its own.
type Pledge struct {
	ID                 uuid.UUID  `json:"id"`
	TransferID         uuid.UUID  `json:"transferId"`
	DeclaredKobo       money.Kobo `json:"declaredKobo"`
	DetectedKobo       money.Kobo `json:"detectedKobo"`
	Confidence         float64    `json:"confidence"`
	ScreenReplay       bool       `json:"screenReplay"`
	PhotocopySuspected bool       `json:"photocopySuspected"`
	Accepted           bool       `json:"accepted"`
	RejectionReason    string     `json:"rejectionReason,omitempty"`
	VisionMode         string     `json:"visionMode"`
	Notes              []Note     `json:"notes"`
}

// Payout is the delivery half of a bank-recipient transfer: settled in the
// ledger, on its way to a real account. The sender sees this, because "settled"
// and "arrived" are not the same claim and should not look the same.
type Payout struct {
	State       string `json:"state"`
	AccountName string `json:"accountName"`
	BankName    string `json:"bankName,omitempty"`
	Reference   string `json:"reference,omitempty"`
	LastError   string `json:"lastError,omitempty"`
}
