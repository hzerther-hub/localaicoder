package codera

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

func sha256Hex16(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])[:16]
}

func fmtSprintf(format string, args ...any) string {
	return fmt.Sprintf(format, args...)
}
