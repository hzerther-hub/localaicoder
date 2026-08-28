package mcp

import (
	"crypto/sha256"
	"encoding/hex"
)

func hashName(raw []byte) string {
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])[:16]
}
