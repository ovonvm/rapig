// Package passwd hashes and verifies passwords with argon2id (PHC string
// format). It is shared by account passwords and share-link passwords so both
// use the same memory-hard parameters (19 MiB, t=2, p=1).
package passwd

import (
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"

	"rapig/internal/platform/idgen"
)

const (
	argonMemory  uint32 = 19456 // KiB
	argonTime    uint32 = 2
	argonThreads uint8  = 1
	argonSaltLen        = 16
	argonKeyLen  uint32 = 32
)

var (
	ErrMalformed = errors.New("passwd: malformed hash")
	ErrMismatch  = errors.New("passwd: password mismatch")
)

// Hash produces a PHC string (`$argon2id$v=19$m=19456,t=2,p=1$salt$key`).
func Hash(password string) (string, error) {
	salt, err := idgen.Bytes(argonSaltLen)
	if err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// Verify checks a password against a stored PHC string, using the parameters
// recorded in that string so old hashes keep verifying after a parameter bump.
func Verify(encoded, password string) error {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return ErrMalformed
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return ErrMalformed
	}
	memory, iters, threads, err := parseParams(parts[3])
	if err != nil {
		return err
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return ErrMalformed
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return ErrMalformed
	}
	got := argon2.IDKey([]byte(password), salt, iters, memory, threads, uint32(len(want)))
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrMismatch
	}
	return nil
}

func parseParams(s string) (memory, iters uint32, threads uint8, err error) {
	for _, field := range strings.Split(s, ",") {
		kv := strings.SplitN(field, "=", 2)
		if len(kv) != 2 {
			return 0, 0, 0, ErrMalformed
		}
		v, convErr := strconv.ParseUint(kv[1], 10, 32)
		if convErr != nil {
			return 0, 0, 0, ErrMalformed
		}
		switch kv[0] {
		case "m":
			memory = uint32(v)
		case "t":
			iters = uint32(v)
		case "p":
			threads = uint8(v)
		default:
			return 0, 0, 0, ErrMalformed
		}
	}
	if memory == 0 || iters == 0 || threads == 0 {
		return 0, 0, 0, ErrMalformed
	}
	return memory, iters, threads, nil
}
