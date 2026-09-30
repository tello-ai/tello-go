package tello

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
