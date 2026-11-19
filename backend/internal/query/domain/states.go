// states.go defines Query domain state machines (frozen in state-machines.json):
// query_access_request / query_grant / query_session /
// query_session_datasource. Any undeclared (state, trigger) pair returns an error (fail closed).
package domain

import "errors"

// RequestState is the controlled vocabulary for query_access_requests.state.
type RequestState string

const (
	RequestAccessPending RequestState = "access_pending"
	RequestGrantActive   RequestState = "grant_active"
	RequestRejected      RequestState = "access_rejected"
	RequestWithdrawn     RequestState = "withdrawn"
	RequestInvalid       RequestState = "invalid"
)

// TerminalRequest reports whether s is a terminal request state (grant_active is terminal for the request;
// subsequent lifecycle transitions live on the Grant and Session).
func TerminalRequest(s RequestState) bool {
	switch s {
	case RequestGrantActive, RequestRejected, RequestWithdrawn, RequestInvalid:
		return true
	}
	return false
}

// RequestTrigger is a state machine trigger on a query access request.
type RequestTrigger string

const (
	RequestApproveFinal RequestTrigger = "approve_final_step"
	RequestReject       RequestTrigger = "reject"
	RequestWithdraw     RequestTrigger = "withdraw"
	RequestActorsGone   RequestTrigger = "all_current_step_actors_missing"
)

var requestEdges = map[RequestState]map[RequestTrigger]RequestState{
	RequestAccessPending: {
		RequestApproveFinal: RequestGrantActive,
		RequestReject:       RequestRejected,
		RequestWithdraw:     RequestWithdrawn,
		RequestActorsGone:   RequestInvalid,
	},
}

// RequestTransition returns the next request state, or an error for undeclared transitions.
func RequestTransition(s RequestState, t RequestTrigger) (RequestState, error) {
	edges, ok := requestEdges[s]
	if !ok {
		return "", illegal("query_access_request", string(s), string(t))
	}
	next, ok := edges[t]
	if !ok {
		return "", illegal("query_access_request", string(s), string(t))
	}
	return next, nil
}

// AllRequestStates lists all valid request states (for tests and static gates).
func AllRequestStates() []RequestState {
	return []RequestState{RequestAccessPending, RequestGrantActive,
		RequestRejected, RequestWithdrawn, RequestInvalid}
}

// GrantState is the controlled vocabulary for query_grants.state.
type GrantState string

const (
	GrantActive       GrantState = "active"
	GrantRevoked      GrantState = "revoked"
	GrantExpired      GrantState = "expired"
	GrantRelinquished GrantState = "relinquished"
)

// TerminalGrant reports whether s is a terminal Grant state.
func TerminalGrant(s GrantState) bool { return s != GrantActive }

// GrantTrigger is a state machine trigger on a Grant.
type GrantTrigger string

const (
	GrantRevoke        GrantTrigger = "revoke"
	GrantExpiryReached GrantTrigger = "expiry_reached"
	GrantRelinquish    GrantTrigger = "relinquish"
)

var grantEdges = map[GrantState]map[GrantTrigger]GrantState{
	GrantActive: {
		GrantRevoke:        GrantRevoked,
		GrantExpiryReached: GrantExpired,
		GrantRelinquish:    GrantRelinquished,
	},
}

// GrantTransition returns the next Grant state, or an error for undeclared transitions.
func GrantTransition(s GrantState, t GrantTrigger) (GrantState, error) {
	edges, ok := grantEdges[s]
	if !ok {
		return "", illegal("query_grant", string(s), string(t))
	}
	next, ok := edges[t]
	if !ok {
		return "", illegal("query_grant", string(s), string(t))
	}
	return next, nil
}

// AllGrantStates lists all valid Grant states.
func AllGrantStates() []GrantState {
	return []GrantState{GrantActive, GrantRevoked, GrantExpired, GrantRelinquished}
}

// SessionState is the controlled vocabulary for query_sessions.state.
type SessionState string

const (
	SessionActive   SessionState = "active"
	SessionClosed   SessionState = "closed"
	SessionRevoked  SessionState = "revoked"
	SessionExpired  SessionState = "expired"
	SessionUserGone SessionState = "user_deleted"
)

// TerminalSession reports whether s is a terminal Session state.
func TerminalSession(s SessionState) bool { return s != SessionActive }

// SessionTrigger is a state machine trigger on a Session.
type SessionTrigger string

const (
	SessionClose        SessionTrigger = "close"
	SessionGrantRevoked SessionTrigger = "grant_revoked"
	SessionGrantExpired SessionTrigger = "grant_expired"
	SessionUserDeleted  SessionTrigger = "user_deleted"
)

var sessionEdges = map[SessionState]map[SessionTrigger]SessionState{
	SessionActive: {
		SessionClose:        SessionClosed,
		SessionGrantRevoked: SessionRevoked,
		SessionGrantExpired: SessionExpired,
		SessionUserDeleted:  SessionUserGone,
	},
}

// SessionTransition returns the next Session state, or an error for undeclared transitions.
func SessionTransition(s SessionState, t SessionTrigger) (SessionState, error) {
	edges, ok := sessionEdges[s]
	if !ok {
		return "", illegal("query_session", string(s), string(t))
	}
	next, ok := edges[t]
	if !ok {
		return "", illegal("query_session", string(s), string(t))
	}
	return next, nil
}

// AllSessionStates lists all valid Session states.
func AllSessionStates() []SessionState {
	return []SessionState{SessionActive, SessionClosed, SessionRevoked,
		SessionExpired, SessionUserGone}
}

// CapabilityState is the controlled vocabulary for query_session_capabilities.state.
type CapabilityState string

const (
	CapActive      CapabilityState = "active"
	CapUnavailable CapabilityState = "datasource_unavailable"
	CapIdentity    CapabilityState = "identity_changed"
)

// TerminalCapability reports whether s is a terminal capability state (identity_changed is unrecoverable).
func TerminalCapability(s CapabilityState) bool { return s == CapIdentity }

// CapabilityTrigger is a state machine trigger on a session capability row.
type CapabilityTrigger string

// Trigger names align with the state-machines.json event vocabulary
// (datasource_unavailable / same_identity_recovered / different_identity_detected).
const (
	CapDatasourceUnavailable CapabilityTrigger = "datasource_unavailable"
	CapSameIdentityRecovered CapabilityTrigger = "same_identity_recovered"
	CapDifferentIdentity     CapabilityTrigger = "different_identity_detected"
)

var capabilityEdges = map[CapabilityState]map[CapabilityTrigger]CapabilityState{
	CapActive: {
		CapDatasourceUnavailable: CapUnavailable,
		CapDifferentIdentity:     CapIdentity,
	},
	CapUnavailable: {
		CapSameIdentityRecovered: CapActive,
		CapDifferentIdentity:     CapIdentity,
	},
}

// CapabilityTransition returns the next capability state, or an error for undeclared transitions.
func CapabilityTransition(s CapabilityState, t CapabilityTrigger) (CapabilityState, error) {
	edges, ok := capabilityEdges[s]
	if !ok {
		return "", illegal("query_session_datasource", string(s), string(t))
	}
	next, ok := edges[t]
	if !ok {
		return "", illegal("query_session_datasource", string(s), string(t))
	}
	return next, nil
}

// AllCapabilityStates lists all valid capability states.
func AllCapabilityStates() []CapabilityState {
	return []CapabilityState{CapActive, CapUnavailable, CapIdentity}
}

// StepState is the controlled vocabulary for approval step states (frozen in 000003 CHECK).
type StepState string

const (
	StepPending  StepState = "pending"
	StepActive   StepState = "active"
	StepApproved StepState = "approved"
	StepRejected StepState = "rejected"
	StepInvalid  StepState = "invalid"
)

func illegal(machine, state, trigger string) error {
	return errors.New("query: illegal transition " + machine + " " + state + " x " + trigger)
}
