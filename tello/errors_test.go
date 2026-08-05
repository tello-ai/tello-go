package tello

import (
	"errors"
	"testing"
)

// codeOf reads the promoted TelloError.Code off any concrete SDK error.
// errors.As cannot do this: embedding TelloError by value promotes its fields
// and methods but does not make *ValidationError a *TelloError.
func codeOf(err error) string {
	switch typed := err.(type) {
	case *ConnectionClosedError:
		return typed.Code
	case *SessionReplacedError:
		return typed.Code
	case *AuthenticationError:
		return typed.Code
	case *ValidationError:
		return typed.Code
	case *CallAlreadyActiveError:
		return typed.Code
	case *NoActiveCallError:
		return typed.Code
	case *CallRejectedError:
		return typed.Code
	case *CallRefusedError:
		return typed.Code
	case *CallProviderError:
		return typed.Code
	case *TelloServerError:
		return typed.Code
	default:
		return ""
	}
}

// Every code in docs/errors/errors.v1.json, in the order the contract lists
// them. asFn reports whether err is the class the contract names for that code.
var errorContract = []struct {
	code  string
	class string
	as    func(error) bool
}{
	{"unauthenticated", "AuthenticationError", func(err error) bool {
		var target *AuthenticationError
		return errors.As(err, &target)
	}},
	{"callAlreadyActive", "CallAlreadyActiveError", func(err error) bool {
		var target *CallAlreadyActiveError
		return errors.As(err, &target)
	}},
	{"toRequired", "ValidationError", isValidation},
	{"callIdRequired", "ValidationError", isValidation},
	{"callNotFound", "ValidationError", isValidation},
	{"callNotCompleted", "ValidationError", isValidation},
	{"noActiveCall", "NoActiveCallError", func(err error) bool {
		var target *NoActiveCallError
		return errors.As(err, &target)
	}},
	{"dtmfDigitsRequired", "ValidationError", isValidation},
	{"dtmfDigitsInvalid", "ValidationError", isValidation},
	{"callRejected", "CallRejectedError", func(err error) bool {
		var target *CallRejectedError
		return errors.As(err, &target)
	}},
	{"insufficientCredit", "CallRefusedError", isRefused},
	{"concurrentLimitExceeded", "CallRefusedError", isRefused},
	{"callerNotVerified", "CallRefusedError", isRefused},
	{"noRepresentativeNumber", "CallRefusedError", isRefused},
	{"callProviderUnauthorized", "CallProviderError", isProvider},
	{"callProviderDraining", "CallProviderError", isProvider},
	{"callProviderUnavailable", "CallProviderError", isProvider},
	{"callSetupFailed", "CallProviderError", isProvider},
	{"internalError", "TelloServerError", isServer},
}

func isValidation(err error) bool {
	var target *ValidationError
	return errors.As(err, &target)
}

func isRefused(err error) bool {
	var target *CallRefusedError
	return errors.As(err, &target)
}

func isProvider(err error) bool {
	var target *CallProviderError
	return errors.As(err, &target)
}

func isServer(err error) bool {
	var target *TelloServerError
	return errors.As(err, &target)
}

func TestErrorForMapsEveryContractCode(t *testing.T) {
	if len(errorContract) != 19 {
		t.Fatalf("contract defines 19 codes, table has %d", len(errorContract))
	}

	for _, entry := range errorContract {
		t.Run(entry.code, func(t *testing.T) {
			err := ErrorFor(entry.code, "message", "")
			if !entry.as(err) {
				t.Fatalf("expected %s, got %T", entry.class, err)
			}

			// Callers branch on Code, never on Message: the gateway may reword
			// the message, the code is the contract.
			if got := codeOf(err); got != entry.code {
				t.Fatalf("expected code %q, got %q", entry.code, got)
			}
		})
	}
}

func TestErrorForFallsBackToServerErrorOnUnknownCode(t *testing.T) {
	err := ErrorFor("somethingNewUpstream", "message", "")
	if !isServer(err) {
		t.Fatalf("expected TelloServerError, got %T", err)
	}
}

func TestCallRejectedPreservesQuestion(t *testing.T) {
	err := ErrorFor("callRejected", "Call rejected", "why?")
	var rejected *CallRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("expected CallRejectedError, got %T", err)
	}
	if rejected.Question != "why?" {
		t.Fatalf("expected question, got %q", rejected.Question)
	}
}
