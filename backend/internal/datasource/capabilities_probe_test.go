package datasource

import "testing"

func TestClassBProbeFlavorDetectionAndCapabilityFailClosed(t *testing.T) {
	cases := []struct {
		name         string
		engine       string
		mode         string
		version      string
		banner       string
		wantFlavor   string
		wantTier     string
		wantGhOstCap bool
	}{
		{
			name:         "native_mysql_8_0_class_a",
			engine:       "mysql",
			mode:         "mysql",
			version:      "8.0.36",
			banner:       "8.0.36",
			wantFlavor:   "mysql_native",
			wantTier:     "class_a",
			wantGhOstCap: true,
		},
		{
			name:         "native_mysql_5_7_class_b",
			engine:       "mysql",
			mode:         "mysql",
			version:      "5.7.44",
			banner:       "5.7.44-log",
			wantFlavor:   "mysql_native",
			wantTier:     "class_b",
			wantGhOstCap: true,
		},
		{
			name:         "native_mysql_8_4_lts_class_b",
			engine:       "mysql",
			mode:         "mysql",
			version:      "8.4.3",
			banner:       "8.4.3-LTS",
			wantFlavor:   "mysql_native",
			wantTier:     "class_b",
			wantGhOstCap: true,
		},
		{
			name:         "mariadb_under_mysql_engine_disables_gh_ost",
			engine:       "mysql",
			mode:         "mysql",
			version:      "10.11.8",
			banner:       "10.11.8-MariaDB-ubu2204",
			wantFlavor:   "mariadb",
			wantTier:     "class_b",
			wantGhOstCap: false,
		},
		{
			name:         "tidb_banner_disables_gh_ost",
			engine:       "tidb",
			mode:         "mysql",
			version:      "8.0.11",
			banner:       "8.0.11-TiDB-v7.5.1",
			wantFlavor:   "tidb",
			wantTier:     "class_b",
			wantGhOstCap: false,
		},
		{
			name:         "oceanbase_banner_disables_gh_ost",
			engine:       "oceanbase",
			mode:         "mysql",
			version:      "5.7.25",
			banner:       "5.7.25-OceanBase_CE-v4.2.1.0",
			wantFlavor:   "oceanbase",
			wantTier:     "class_b",
			wantGhOstCap: false,
		},
		{
			name:         "polardb_mysql_banner_disables_gh_ost",
			engine:       "polardb",
			mode:         "mysql",
			version:      "8.0.2",
			banner:       "8.0.2-PolarDB-8.0.2.2.20",
			wantFlavor:   "polardb_mysql",
			wantTier:     "class_b",
			wantGhOstCap: false,
		},
		{
			name:         "polardb_postgres_class_b",
			engine:       "polardb",
			mode:         "postgresql",
			version:      "14.11",
			banner:       "PostgreSQL 14.11 (PolarDB 14.11.20.0)",
			wantFlavor:   "polardb_postgresql",
			wantTier:     "class_b",
			wantGhOstCap: false,
		},
		{
			name:         "native_postgres_17_class_a",
			engine:       "postgresql",
			mode:         "postgresql",
			version:      "17.2",
			banner:       "PostgreSQL 17.2",
			wantFlavor:   "postgresql_native",
			wantTier:     "class_a",
			wantGhOstCap: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target, caps, flavor, tier, notice, err := ResolveCapabilitiesWithProbe(tc.engine, tc.mode, tc.version, tc.banner)
			if err != nil {
				t.Fatalf("ResolveCapabilitiesWithProbe failed: %v", err)
			}
			if target == "" {
				t.Fatal("expected non-empty capability target")
			}
			if flavor != tc.wantFlavor {
				t.Fatalf("flavor = %q, want %q", flavor, tc.wantFlavor)
			}
			if tier != tc.wantTier {
				t.Fatalf("tier = %q, want %q", tier, tc.wantTier)
			}
			if caps[CapGhOst] != tc.wantGhOstCap {
				t.Fatalf("capabilities[%s] = %v, want %v", CapGhOst, caps[CapGhOst], tc.wantGhOstCap)
			}
			if tc.wantTier == "class_b" && notice == "" {
				t.Fatal("expected non-empty compatibility_mode_notice for class_b flavor")
			}
		})
	}
}
