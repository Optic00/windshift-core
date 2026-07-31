package services

import (
	"crypto/sha256"
	"encoding/hex"
)

// hashTokenAtRest returns the at-rest representation of a high-entropy,
// single-use token (magic links, invitation tokens, email-verification
// tokens). A fast hash is sufficient — the token itself is generated from
// crypto/rand with 256 bits of entropy, so its only job is to ensure a
// read-only DB compromise (leaked backup, read replica, blind SQLi) yields
// no directly usable tokens. The hash is deterministic, so indexed equality
// lookups are preserved.
func hashTokenAtRest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
