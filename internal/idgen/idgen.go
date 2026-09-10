// Package idgen generates short, sortable, unique-enough string IDs for primary keys —
// a 3-letter prefix (identifies the entity type at a glance in logs/DB browsers) plus
// a base36-encoded timestamp and a few random characters for collision safety.
package idgen

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"
	"time"
)

const randomChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// Generate returns prefix + 9-char base36 millisecond timestamp + 5 random chars.
// Collisions are astronomically unlikely (36^5 random space per millisecond) and are
// not otherwise guarded against — callers relying on strict uniqueness should still
// have a DB-level UNIQUE/PRIMARY KEY constraint, same discipline as any generated ID.
func Generate(prefix string) (string, error) {
	if len(prefix) != 3 {
		return "", fmt.Errorf("idgen: prefix must be exactly 3 characters, got %q", prefix)
	}
	ts := strings.ToUpper(fmt.Sprintf("%09s", toBase36(time.Now().UnixMilli())))

	var sb strings.Builder
	sb.WriteString(strings.ToUpper(prefix))
	sb.WriteString(ts)
	for i := 0; i < 5; i++ {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(randomChars))))
		if err != nil {
			return "", fmt.Errorf("idgen: read random: %w", err)
		}
		sb.WriteByte(randomChars[n.Int64()])
	}
	return sb.String(), nil
}

func toBase36(n int64) string {
	if n == 0 {
		return "0"
	}
	const digits = "0123456789abcdefghijklmnopqrstuvwxyz"
	var sb strings.Builder
	for n > 0 {
		sb.WriteByte(digits[n%36])
		n /= 36
	}
	s := []byte(sb.String())
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
	return string(s)
}
