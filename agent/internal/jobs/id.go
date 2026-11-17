package jobs

import (
	"crypto/rand"
	"encoding/hex"
)

func randID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
