// loopback_datasource_test.go verifies strict Purpose isolation for outbound.allow_loopback_datasource:
// enabling it allows loopback ONLY for PurposeDatasource and PurposeOSC while keeping loopback forbidden for all
// other outbound purposes (AI, OIDC, LDAP, Webhook, SMTP, ReleaseFeed, RetentionDB, Generic) and keeping cloud
// metadata endpoints hard-rejected for all purposes.
package addrguard

import (
	"context"
	"errors"
	"testing"
)

func TestAllowLoopbackDatasourceStrictPurposeIsolation(t *testing.T) {
	bypassOff(t)
	t.Cleanup(ResetSelfGuardForTest())
	t.Cleanup(SetAllowLoopbackDatasourceForTest(true))

	allowedPurposes := []Purpose{PurposeDatasource, PurposeOSC}
	for _, p := range allowedPurposes {
		for _, ip := range []string{"127.0.0.1", "::1"} {
			if err := ValidateIPFor(p, mustIP(t, ip)); err != nil {
				t.Errorf("allow_loopback_datasource: %s ValidateIPFor(%s) = %v; want nil", p, ip, err)
			}
			if err := ValidateHostFor(context.Background(), p, ip, nil); err != nil {
				t.Errorf("allow_loopback_datasource: %s ValidateHostFor(%s) = %v; want nil", p, ip, err)
			}
		}
		if err := ValidateIPFor(p, mustIP(t, "169.254.169.254")); !errors.Is(err, ErrCloudMetadataForbidden) {
			t.Errorf("%s cloud metadata = %v; want ErrCloudMetadataForbidden", p, err)
		}
		if err := ValidateIPFor(p, mustIP(t, "100.100.100.200")); !errors.Is(err, ErrCloudMetadataForbidden) {
			t.Errorf("%s aliyun metadata = %v; want ErrCloudMetadataForbidden", p, err)
		}
	}

	deniedPurposes := []Purpose{
		PurposeGeneric,
		PurposeAIProvider,
		PurposeOIDC,
		PurposeLDAP,
		PurposeWebhook,
		PurposeSMTP,
		PurposeReleaseFeed,
		PurposeRetentionDB,
	}
	for _, p := range deniedPurposes {
		for _, ip := range []string{"127.0.0.1", "::1"} {
			err := ValidateIPFor(p, mustIP(t, ip))
			if !errors.Is(err, ErrLoopbackForbidden) || !errors.Is(err, ErrAddressForbidden) {
				t.Errorf("allow_loopback_datasource must not exempt %s (%s): got %v; want ErrLoopbackForbidden", p, ip, err)
			}
		}
	}

	// Default off rejects datasource loopback as well.
	t.Cleanup(SetAllowLoopbackDatasourceForTest(false))
	if err := ValidateIPFor(PurposeDatasource, mustIP(t, "127.0.0.1")); !errors.Is(err, ErrLoopbackForbidden) {
		t.Fatalf("datasource loopback with toggle off = %v; want ErrLoopbackForbidden", err)
	}
}
