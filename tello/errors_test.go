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
	case *PermissionDeniedError:
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
	{"ipNotAllowed", "AuthenticationError", func(err error) bool {
		var target *AuthenticationError
		return errors.As(err, &target)
	}},
	{"insufficientScope", "PermissionDeniedError", func(err error) bool {
		var target *PermissionDeniedError
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
	if len(errorContract) != 21 {
		t.Fatalf("contract defines 21 codes, table has %d", len(errorContract))
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

func TestErrorForEventCarriesExtraFields(t *testing.T) {
	scope := ErrorForEvent(Event{Code: "insufficientScope", Message: "m", RequestID: "r-1", RequiredScope: "calls:write"})
	var denied *PermissionDeniedError
	if !errors.As(scope, &denied) || denied.RequiredScope != "calls:write" || denied.Code != "insufficientScope" {
		t.Fatalf("expected PermissionDeniedError with requiredScope, got %T: %+v", scope, scope)
	}

	ip := ErrorForEvent(Event{Code: "ipNotAllowed", Message: "m", IP: "203.0.113.7"})
	var auth *AuthenticationError
	if !errors.As(ip, &auth) || auth.IP != "203.0.113.7" || auth.Code != "ipNotAllowed" {
		t.Fatalf("expected AuthenticationError with ip, got %T: %+v", ip, ip)
	}

	rejected := ErrorForEvent(Event{Code: "callRejected", Message: "m", Question: "q"})
	var rej *CallRejectedError
	if !errors.As(rejected, &rej) || rej.Question != "q" {
		t.Fatalf("expected CallRejectedError with question, got %T: %+v", rejected, rejected)
	}
}
