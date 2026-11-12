package tello

import (
	"context"
	"fmt"
)

type TelloError struct {
	Code     string
	Message  string
	Question string
}

func (e *TelloError) Error() string {
	return e.Message
}

type ConnectionClosedError struct{ TelloError }
type SessionReplacedError struct{ TelloError }
type AuthenticationError struct{ TelloError }
type ValidationError struct{ TelloError }
type CallAlreadyActiveError struct{ TelloError }
type NoActiveCallError struct{ TelloError }
type CallRejectedError struct{ TelloError }
type TelloServerError struct{ TelloError }

// CallRefusedError reports that CreateCall was refused by an account policy
// gate before any call existed: no call.created, no callID, no charge. The
// account owner can act on insufficientCredit, callerNotVerified and
// noRepresentativeNumber; only concurrentLimitExceeded can succeed on a later
// attempt. The gateway never retries, so any retry policy is the caller's.
// Branch on Code.
type CallRefusedError struct{ TelloError }

// CallProviderError reports that CreateCall was refused by a condition on the
// service side. The caller did not cause it and cannot fix it.
// callProviderDraining and callProviderUnavailable may succeed later; the other
// two will not.
type CallProviderError struct{ TelloError }

// APIError is an error answer of the REST API (see NewAPI), or a success
// answer the SDK could not accept.
//
// Code is the server's error code verbatim, in snake_case as the public REST
// docs list it (for example "tool_not_found"); branch on Code and Status.
// A response without the documented error envelope (a proxy page, for one)
// gets Code "http_<status>" with the HTTP status text as Message. A success
// status the API does not document for the method, or a body that is not a
// JSON object, gets Code "invalid_response".
//
// Data is the envelope's data object (for example missingVariables or
// reason), nil when absent. IdempotencyKey is the Idempotency-Key the
// request sent, "" when it sent none; retry with the same key.
type APIError struct {
	TelloError
	Status         int
	Data           Object
	IdempotencyKey string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("tello api: %d %s: %s", e.Status, e.Code, e.Message)
}

// APIConnectionError reports that a REST request got no complete response:
// the connection failed, the timeout passed or ctx ended. Err is the cause,
// so errors.Is(err, context.DeadlineExceeded) and errors.Is(err,
// context.Canceled) work. Code is empty.
//
// A write (create, update, delete, crew generation, call draft) that failed
// this way may already have been applied, and a confirmed call draft may
// already be dialing. Check with get or list before sending it again, and
// resend a create with the same IdempotencyKey (the key the request sent, ""
// when none) so the server replays the first answer instead of acting twice.
type APIConnectionError struct {
	TelloError
	IdempotencyKey string
	Err            error
}

func (e *APIConnectionError) Error() string {
	return e.Message
}

func (e *APIConnectionError) Unwrap() error {
	return e.Err
}

// CrewGenerationTimeoutError reports that Crews.GenerateAndWait stopped
// waiting before the job finished. The job keeps running; follow it with
// CrewGenerations.Get(JobID). It unwraps to context.DeadlineExceeded.
type CrewGenerationTimeoutError struct {
	TelloError
	JobID string
}

func (e *CrewGenerationTimeoutError) Error() string {
	return e.Message
}

func (e *CrewGenerationTimeoutError) Unwrap() error {
	return context.DeadlineExceeded
}

// Timeout reports true, like net.Error.
func (e *CrewGenerationTimeoutError) Timeout() bool {
	return true
}

// ErrorFor maps a gateway error code to its typed error. Use it to turn an
// EventTypeError event into the error WaitClosed would return:
// ErrorFor(event.Code, event.Message, event.Question).
func ErrorFor(code, message, question string) error {
	base := TelloError{Code: code, Message: message, Question: question}
	switch code {
	case "unauthenticated":
		return &AuthenticationError{base}
	case "toRequired",
		"callIdRequired",
		"dtmfDigitsRequired",
		"dtmfDigitsInvalid",
		"callNotFound",
		"callNotCompleted":
		return &ValidationError{base}
	case "callAlreadyActive":
		return &CallAlreadyActiveError{base}
	case "noActiveCall":
		return &NoActiveCallError{base}
	case "callRejected":
		return &CallRejectedError{base}
	case "insufficientCredit",
		"concurrentLimitExceeded",
		"callerNotVerified",
		"noRepresentativeNumber":
		return &CallRefusedError{base}
	case "callProviderUnauthorized",
		"callProviderDraining",
		"callProviderUnavailable",
		"callSetupFailed":
		return &CallProviderError{base}
	case "internalError":
		fallthrough
	default:
		return &TelloServerError{base}
	}
}
