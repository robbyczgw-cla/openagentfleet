package id

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

func New(prefix string) string {
	var bytes [8]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return fmt.Sprintf("%s-fallback", prefix)
	}
	return fmt.Sprintf("%s-%s", prefix, hex.EncodeToString(bytes[:]))
}
