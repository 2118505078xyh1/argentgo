package llm

// FailureKind is the closed vocabulary of failure classes exposed to callers.
// The string values are stable so frontends and the session layer can persist
// or compare them without importing this package's types.
//
// It is deliberately coarser than ActionableError.Family: several provider
// families (network, server) collapse into "transient", and a provider-side
// content block is an "agent_action" rather than a transport failure.
type FailureKind string

const (
	FailureAuth            FailureKind = "auth"
	FailureQuota           FailureKind = "quota"
	FailureRateLimit       FailureKind = "rate_limit"
	FailureConfig          FailureKind = "config"
	FailureTransient       FailureKind = "transient"
	FailureAgentAction     FailureKind = "agent_action"
	FailureContextOverflow FailureKind = "context_overflow"
	// FailureInternal is reserved for faults inside this process. ClassifyFailure
	// does not emit it yet; it exists so the vocabulary is closed and a future
	// internal-fault signal can be routed without changing the type.
	FailureInternal FailureKind = "internal"
	FailureUnknown  FailureKind = "unknown"
)

// UserAction is the affordance a caller should offer after a failure. It is a
// three-value routing hint, not UI copy:
//
//	none     nothing for the user to do; report the failure or handle it internally
//	retry    the user may try again (after a wait or a revised request)
//	settings the user must fix credentials, a model or other settings first
type UserAction string

const (
	UserActionNone     UserAction = "none"
	UserActionRetry    UserAction = "retry"
	UserActionSettings UserAction = "settings"
)

// FailureInfo is a redacted classification of a failed call. It deliberately
// has no message, body or detail field: ErrorID is a fixed identifier picked
// from a table, so it is safe to log, persist and send to a frontend.
type FailureInfo struct {
	Kind       FailureKind
	Retryable  bool
	UserAction UserAction
	ErrorID    string
}

// failureByFamily translates an ActionableError family into the closed
// vocabulary. Keeping the translation here means no sentinel or regex logic is
// duplicated: ClassifyAPIError stays the single source of truth for "what
// happened", including its authoritative-exception-first ordering, and this
// table only decides the kind, retryability, user affordance and short id.
//
// Retryable answers "will an unchanged retry succeed on its own?" while
// UserAction answers "what should the user do?". They can differ: a content
// block is not safe to replay unchanged (Retryable=false) but the user is still
// guided to revise the prompt and try again (UserAction=retry). A context
// overflow is also Retryable=false (the same oversized request would fail
// again) and is routed to settings so the caller can trigger compaction.
var failureByFamily = map[string]FailureInfo{
	FamilyNotConfigured:   {FailureConfig, false, UserActionSettings, "config.not_configured"},
	FamilyAuth:            {FailureAuth, false, UserActionSettings, "auth.invalid_key"},
	FamilyQuotaExhausted:  {FailureQuota, false, UserActionSettings, "quota.exhausted"},
	FamilyRateLimit:       {FailureRateLimit, true, UserActionRetry, "rate_limit.exceeded"},
	FamilyContentFilter:   {FailureAgentAction, false, UserActionRetry, "agent_action.content_filter"},
	FamilyContextOverflow: {FailureContextOverflow, false, UserActionSettings, "context_overflow.exceeded"},
	FamilyModelNotFound:   {FailureConfig, false, UserActionSettings, "config.model_not_found"},
	FamilyNetwork:         {FailureTransient, true, UserActionRetry, "transient.network"},
	FamilyServer:          {FailureTransient, true, UserActionRetry, "transient.server"},
	FamilyUnknown:         {FailureUnknown, false, UserActionNone, "unknown"},
}

// unknownFailure is the fallback for a nil error or a family this build does
// not know (for example an ActionableError constructed by a newer caller).
// An unrecognized failure is never assumed retryable and asks nothing of the
// user, so no request is silently replayed and no detail is invented.
var unknownFailure = FailureInfo{FailureUnknown, false, UserActionNone, "unknown"}

// ClassifyFailure maps any error onto the closed failure vocabulary.
//
// It reuses ClassifyAPIError, so authoritative exception classes (net/context
// sentinels, wrapped stream errors) win over generic text patterns before the
// family is translated. The result carries no provider text or credential:
// ErrorID is always one of the fixed identifiers in failureByFamily.
//
// nil is reported as unknown/none with no detail; callers that treat "no
// error" specially can keep checking err before calling.
func ClassifyFailure(err error) FailureInfo {
	if err == nil {
		return unknownFailure
	}
	if info, ok := failureByFamily[ClassifyAPIError(err).Family]; ok {
		return info
	}
	return unknownFailure
}
