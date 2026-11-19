package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// PolicyRevision changes whenever effective review-policy semantics change
// without a corresponding change to source configuration rows.
const PolicyRevision = "critical-review-blocking-v3-catalog-privacy"

// EffectiveToolConfigHash binds the frozen Prompt Tool hashes to the global
// review-policy revision. Review and submission both use this function so an
// unsubmitted result produced under different governance becomes outdated.
func EffectiveToolConfigHash(configHashes []string) string {
	hashes := append([]string(nil), configHashes...)
	sort.Strings(hashes)
	var b strings.Builder
	b.WriteString("yearning-effective-review-policy-v1\n")
	b.WriteString(PolicyRevision)
	b.WriteByte('\n')
	for _, hash := range hashes {
		b.WriteString(hash)
		b.WriteByte('\n')
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}
