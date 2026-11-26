package hashutil

import "testing"

func TestSHA256Hex(t *testing.T) {
	const emptyHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	const abcHash = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"

	if got := SHA256Hex(nil); got != emptyHash {
		t.Fatalf("SHA256Hex(nil) = %q, want %q", got, emptyHash)
	}
	if got := SHA256Hex([]byte("abc")); got != abcHash {
		t.Fatalf("SHA256Hex(abc) = %q, want %q", got, abcHash)
	}
	if got := SHA256HexString("abc"); got != abcHash {
		t.Fatalf("SHA256HexString(abc) = %q, want %q", got, abcHash)
	}
}
