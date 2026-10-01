package tello

const ProtocolVersion = "1.0"

// Version is this SDK release, without the leading "v" of the module tag.
// Bump it in the same commit that is tagged for release.
const Version = "0.2.4"

const (
	EventTypeAuthOK            = "auth.ok"
	EventTypeCallCreated       = "call.created"
	EventTypeUserTurn          = "user.turn"
	EventTypeAgentTurn         = "agent.turn"
	EventTypeCallSummary       = "call.summary"
	EventTypeAnswerAccepted    = "answer.accepted"
	EventTypeDtmfAccepted      = "dtmf.accepted"
	EventTypeCallStatusChanged = "call.statusChanged"
	EventTypeCallCompleted     = "call.completed"
	EventTypeCallNoAnswer      = "call.noAnswer"
	EventTypeCallFailed        = "call.failed"
	EventTypeError             = "error"
	EventTypeDisconnected      = "disconnected"
)

type Event struct {
	Type            string
	Version         string
	SessionID       string
	CallID          string
	Timestamp       string
	Raw             map[string]any
	TurnIndex       int
	Text            string
	MessageID       string
	Digits          string
	Status          string
	PreviousStatus  string
	FailureReason   string
	Code            string
	Message         string
	RequestID       string
	Question        string
	DurationSeconds int
	Transcript      string
	Summary         string
	CreditCharged   int
}
