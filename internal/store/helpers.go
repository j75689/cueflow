package store

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Slugify derives an ASCII slug; non-ASCII names (e.g. Chinese) fall back to "agent-xxxx".
func Slugify(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteRune('-')
			dash = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		s = "agent-" + uuid.NewString()[:4]
	}
	if len(s) > 63 {
		s = s[:63]
	}
	return s
}

// JobFinished reports whether a runner job is over.
func JobFinished(status string) bool {
	return status != JobQueued && status != JobRunning && status != JobCanceling
}

// NewToken returns a random token with prefix and its SHA-256 (the only thing stored).
func NewToken(prefix string) (plain string, hash []byte) {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	plain = prefix + hex.EncodeToString(b)
	return plain, HashToken(plain)
}

func HashToken(plain string) []byte {
	h := sha256.Sum256([]byte(plain))
	return h[:]
}

func runnerLabels(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, l := range in {
		l = strings.ToLower(strings.TrimSpace(l))
		if l != "" && !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	return out
}

// CleanLabels normalizes runner labels the way the store keeps them.
func CleanLabels(in []string) []string { return runnerLabels(in) }

// Invalid is a ValidationError for a caller error.
func Invalid(msg string) error { return &ValidationError{Msg: msg} }

// ValidTaskFileName checks a file name: valid UTF-8, 1–200 characters, "/"-separated parts that are
// neither empty nor "." or "..", no backslashes or control characters.
func ValidTaskFileName(name string) error {
	if !utf8.ValidString(name) {
		return Invalid("a file name must be valid UTF-8")
	}
	if n := utf8.RuneCountInString(name); n == 0 || n > maxTaskFileName {
		return Invalid(fmt.Sprintf("a file name must be 1 to %d characters", maxTaskFileName))
	}
	for _, r := range name {
		if r == '\\' || unicode.IsControl(r) {
			return Invalid("a file name can't contain backslashes or control characters")
		}
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return Invalid(`a file name can't start or end with "/", or contain "//", "." or ".." parts`)
		}
	}
	return nil
}

// ValidPriority reports whether p is one of TaskPriorities.
func ValidPriority(p string) bool {
	for _, v := range TaskPriorities {
		if v == p {
			return true
		}
	}
	return false
}

// SameID reports whether two optional ids are both unset or equal.
func SameID(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
