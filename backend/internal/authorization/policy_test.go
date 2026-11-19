package authorization

import (
	"testing"

	"github.com/cookieY/YearningX/backend/internal/identity/session"
)

// actor_deny_matrix: for every operation, iterate through all actor classes;
// only the minimum class combinations declared by the rule are allowed, and all others are denied (default deny).
func TestActorDenyMatrix(t *testing.T) {
	anonymous := (*session.Identity)(nil)
	normal := &session.Identity{UserID: "u1", Username: "alice", IsBuiltinAdmin: false}
	admin := &session.Identity{UserID: "u2", Username: "admin", IsBuiltinAdmin: true}

	actors := map[string]*session.Identity{
		"anonymous":     anonymous,
		"authenticated": normal,
		"builtin_admin": admin,
	}
	adminOps := []string{
		OpListUsers, OpCreateUser, OpUpdateUser, OpDeleteUser, OpGetUserDeletionImpact,
		OpListFlows, OpCreateFlow, OpGetFlow, OpReplaceFlow, OpDeleteFlow,
		OpListDatasources, OpCreateDatasource, OpGetDatasource, OpReplaceDatasource, OpDeleteDatasource,
		OpTestDatasourceConnection, OpGetDatasourceCapabilities,
		OpListPermissionGroups, OpCreatePermissionGroup, OpGetPermissionGroup, OpReplacePermissionGroup, OpDeletePermissionGroup,
		OpListIdentityProviders, OpCreateIdentityProvider, OpGetIdentityProvider, OpReplaceIdentityProvider,
		OpDeleteIdentityProvider, OpTestIdentityProviderConnection,
	}
	// admin_surface_cases: management surface allows only builtin_admin.
	for _, op := range adminOps {
		for name, actor := range actors {
			want := name == "builtin_admin"
			if got := Decide(op, actor); got != want {
				t.Fatalf("%s by %s: decide=%v want=%v", op, name, got, want)
			}
		}
	}
	// Authentication entry points allow all classes (anonymous-authentication rule:
	// callable by both anonymous and authenticated_user).
	for _, op := range []string{OpLogin, OpRegister, OpListAuthenticationProviders, OpLoginWithLdap, OpStartOidcLogin, OpFinishOidcLogin} {
		for name, actor := range actors {
			if got := Decide(op, actor); !got {
				t.Fatalf("%s by %s: decide=%v want=true", op, name, got)
			}
		}
	}
	// Authenticated operations: deny anonymous, allow all others.
	for _, op := range []string{OpLogout, OpGetCurrentUser, OpListCurrentUserFlows, OpStreamDomainEvents} {
		for name, actor := range actors {
			want := name != "anonymous"
			if got := Decide(op, actor); got != want {
				t.Fatalf("%s by %s: decide=%v want=%v", op, name, got, want)
			}
		}
	}
}

// Unclassified operations default to deny (runtime defense for unclassified_operation: build_failure semantics).
func TestDefaultDenyUnclassified(t *testing.T) {
	admin := &session.Identity{UserID: "u", Username: "admin", IsBuiltinAdmin: true}
	for _, op := range []string{"revealOrderSql", "decideChangeOrder", "executeSelect", "totally-unknown"} {
		if Decide(op, admin) {
			t.Fatalf("unclassified op %q allowed", op)
		}
	}
}

// admin_is_not_business_override: admin class does not imply eligibility for business operations;
// business operations absent from the B3 policy table are denied (extended by subsequent Packages per frozen actor rules).
func TestAdminIsNotBusinessOverride(t *testing.T) {
	admin := &session.Identity{UserID: "u", Username: "admin", IsBuiltinAdmin: true}
	if Decide("decideChangeOrder", admin) {
		t.Fatal("admin must not decide business approvals by identity alone")
	}
}

func TestClassify(t *testing.T) {
	if Classify(nil) != ActorAnonymous {
		t.Fatal("nil identity must classify anonymous")
	}
	if Classify(&session.Identity{UserID: "x"}) != ActorAuthenticated {
		t.Fatal("normal user must classify authenticated")
	}
	if Classify(&session.Identity{UserID: "x", IsBuiltinAdmin: true}) != ActorBuiltinAdmin {
		t.Fatal("builtin admin must classify builtin_admin")
	}
	if Classify(&session.Identity{UserID: "x", Role: "admin"}) != ActorBuiltinAdmin {
		t.Fatal("role=admin user must classify builtin_admin")
	}
}
