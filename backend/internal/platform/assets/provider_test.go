// Default build (!assets) semantic assertions: stub path returns a disabled Provider and all Validate branches are covered.
//go:build !assets

package assets

import (
	"testing"
	"testing/fstest"
)

const goodDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestProviderValidateAcceptsEmbeddedShape(t *testing.T) {
	fsys := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<!doctype html>")}}
	if err := NewProvider(fsys, goodDigest).Validate(); err != nil {
		t.Fatalf("valid provider rejected: %v", err)
	}
}

func TestProviderValidateDisabledIsClean(t *testing.T) {
	if err := NewProvider(nil, "").Validate(); err != nil {
		t.Fatalf("disabled provider must validate clean: %v", err)
	}
}

func TestProviderValidateRejectsBadDigestAndMissingIndex(t *testing.T) {
	fsys := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("x")}}
	if err := NewProvider(fsys, "short").Validate(); err == nil {
		t.Fatal("non-64-char digest accepted")
	}
	badHex := "Z23456789012345678901234567890123456789012345678901234567890123456"
	if err := NewProvider(fsys, badHex).Validate(); err == nil {
		t.Fatal("non-hex digest accepted")
	}
	upper := "ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789"
	if err := NewProvider(fsys, upper).Validate(); err == nil {
		t.Fatal("uppercase digest accepted")
	}
	empty := fstest.MapFS{}
	if err := NewProvider(empty, goodDigest).Validate(); err == nil {
		t.Fatal("missing index.html accepted")
	}
}

func TestReleaseProviderDisabledWithoutAssetsTag(t *testing.T) {
	// This file compiles without the assets build tag: the stub path must return a disabled Provider
	// so the default build routing surface remains strictly API-only.
	prov := ReleaseProvider()
	if prov.Enabled() {
		t.Fatal("ReleaseProvider enabled without -tags assets")
	}
}
