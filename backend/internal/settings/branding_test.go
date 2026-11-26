package settings

import (
	"context"
	"testing"
)

func TestReadPublicBranding_NilDBReturnsDefaults(t *testing.T) {
	got := ReadPublicBranding(context.Background(), nil)
	if got.ProductName != DefaultProductName {
		t.Fatalf("expected default product_name %q, got %q", DefaultProductName, got.ProductName)
	}
	if got.LoginDescription != "" {
		t.Fatalf("expected empty login_description, got %q", got.LoginDescription)
	}
}

func TestParsePublicBranding(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want PublicBranding
	}{
		{
			name: "empty",
			raw:  "",
			want: PublicBranding{ProductName: DefaultProductName, LoginDescription: ""},
		},
		{
			name: "invalid_json",
			raw:  "{not-json",
			want: PublicBranding{ProductName: DefaultProductName, LoginDescription: ""},
		},
		{
			name: "custom_values_trimmed",
			raw:  `{"product_name":"  AcmeSQL  ","login_description":"  Internal portal  "}`,
			want: PublicBranding{ProductName: "AcmeSQL", LoginDescription: "Internal portal"},
		},
		{
			name: "blank_product_name_falls_back_to_default",
			raw:  `{"product_name":"   ","login_description":"Only description"}`,
			want: PublicBranding{ProductName: DefaultProductName, LoginDescription: "Only description"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parsePublicBranding([]byte(tc.raw))
			if got != tc.want {
				t.Fatalf("parsePublicBranding(%q) = %+v, want %+v", tc.raw, got, tc.want)
			}
		})
	}
}
