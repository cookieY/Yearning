// states_test.go tests the Query domain state machine transition matrices (frozen copy of state-machines.json).
package domain

import "testing"

func TestRequestTransitions(t *testing.T) {
	ok := []struct {
		from RequestState
		trig RequestTrigger
		want RequestState
	}{
		{RequestAccessPending, RequestApproveFinal, RequestGrantActive},
		{RequestAccessPending, RequestReject, RequestRejected},
		{RequestAccessPending, RequestWithdraw, RequestWithdrawn},
		{RequestAccessPending, RequestActorsGone, RequestInvalid},
	}
	for _, c := range ok {
		got, err := RequestTransition(c.from, c.trig)
		if err != nil || got != c.want {
			t.Errorf("RequestTransition(%s,%s) = %v,%v; want %s", c.from, c.trig, got, err, c.want)
		}
	}
	bad := []struct {
		from RequestState
		trig RequestTrigger
	}{
		{RequestGrantActive, RequestReject},
		{RequestWithdrawn, RequestWithdraw},
		{RequestAccessPending, RequestTrigger("unknown")},
	}
	for _, c := range bad {
		if _, err := RequestTransition(c.from, c.trig); err == nil {
			t.Errorf("RequestTransition(%s,%s) accepted; want illegal", c.from, c.trig)
		}
	}
}

func TestGrantTransitions(t *testing.T) {
	ok := []struct {
		from GrantState
		trig GrantTrigger
		want GrantState
	}{
		{GrantActive, GrantRevoke, GrantRevoked},
		{GrantActive, GrantExpiryReached, GrantExpired},
		{GrantActive, GrantRelinquish, GrantRelinquished},
	}
	for _, c := range ok {
		got, err := GrantTransition(c.from, c.trig)
		if err != nil || got != c.want {
			t.Errorf("GrantTransition(%s,%s) = %v,%v; want %s", c.from, c.trig, got, err, c.want)
		}
	}
	for _, s := range AllGrantStates() {
		if !TerminalGrant(s) && s != GrantActive {
			t.Errorf("grant state %s misclassified", s)
		}
	}
	for _, c := range []struct {
		from GrantState
		trig GrantTrigger
	}{{GrantRevoked, GrantRevoke}, {GrantExpired, GrantRelinquish}} {
		if _, err := GrantTransition(c.from, c.trig); err == nil {
			t.Errorf("GrantTransition(%s,%s) accepted terminal", c.from, c.trig)
		}
	}
}

func TestSessionTransitions(t *testing.T) {
	ok := []struct {
		from SessionState
		trig SessionTrigger
		want SessionState
	}{
		{SessionActive, SessionClose, SessionClosed},
		{SessionActive, SessionGrantRevoked, SessionRevoked},
		{SessionActive, SessionGrantExpired, SessionExpired},
		{SessionActive, SessionUserDeleted, SessionUserGone},
	}
	for _, c := range ok {
		got, err := SessionTransition(c.from, c.trig)
		if err != nil || got != c.want {
			t.Errorf("SessionTransition(%s,%s) = %v,%v; want %s", c.from, c.trig, got, err, c.want)
		}
	}
	if _, err := SessionTransition(SessionClosed, SessionClose); err == nil {
		t.Error("terminal session close accepted")
	}
}

func TestCapabilityTransitions(t *testing.T) {
	ok := []struct {
		from CapabilityState
		trig CapabilityTrigger
		want CapabilityState
	}{
		{CapActive, CapDatasourceUnavailable, CapUnavailable},
		{CapUnavailable, CapSameIdentityRecovered, CapActive},
		{CapActive, CapDifferentIdentity, CapIdentity},
		{CapUnavailable, CapDifferentIdentity, CapIdentity},
	}
	for _, c := range ok {
		got, err := CapabilityTransition(c.from, c.trig)
		if err != nil || got != c.want {
			t.Errorf("CapabilityTransition(%s,%s) = %v,%v; want %s", c.from, c.trig, got, err, c.want)
		}
	}
	// identity_changed is terminal and unrecoverable.
	if _, err := CapabilityTransition(CapIdentity, CapSameIdentityRecovered); err == nil {
		t.Error("identity_changed is recoverable")
	}
	if !TerminalCapability(CapIdentity) || TerminalCapability(CapUnavailable) {
		t.Error("capability terminality misclassified")
	}
}
