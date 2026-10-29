package oidc

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// The Argon2id floor of R-AUTH-014: RFC 9106 §4, second recommended option.
const (
	argonMinMemory  = 64 * 1024 // KiB
	argonMinTime    = 3
	argonMinThreads = 4
	argonKeyLen     = 32
	argonSaltLen    = 16
	// minPlaintext is the shortest plaintext break-glass password (R-AUTH-014).
	minPlaintext = 20
)

// password is a loaded break-glass verifier: Argon2id parameters, salt and key,
// and the SHA-256 of the file it came from, which tells a rotation from a touch.
type password struct {
	memory      uint32
	time        uint32
	threads     uint8
	salt, key   []byte
	fingerprint [32]byte
	// hashed is true when the file held a PHC hash: its strength cannot be
	// checked (R-AUTH-014).
	hashed bool
}

// parsePassword reads the content of the password file: an Argon2id hash in PHC
// string format, or a plaintext password that is hashed here, in memory. A
// plaintext shorter than 20 characters is refused (Q-AUTH-34); so is a hash
// below the floor of R-AUTH-014.
func parsePassword(content []byte) (*password, error) {
	fp := sha256.Sum256(content)
	text := strings.TrimRight(string(content), "\r\n")
	if strings.HasPrefix(text, "$argon2") {
		pw, err := parsePHC(text)
		if err != nil {
			return nil, err
		}
		pw.fingerprint, pw.hashed = fp, true
		return pw, nil
	}
	if n := utf8.RuneCountInString(text); n < minPlaintext {
		return nil, fmt.Errorf("the plaintext break-glass password has %d characters; the minimum is %d (R-AUTH-014)", n, minPlaintext)
	}
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	pw := &password{memory: argonMinMemory, time: argonMinTime, threads: argonMinThreads, salt: salt, fingerprint: fp}
	pw.key = argon2.IDKey([]byte(text), salt, pw.time, pw.memory, pw.threads, argonKeyLen)
	return pw, nil
}

// parsePHC reads $argon2id$v=19$m=<KiB>,t=<passes>,p=<lanes>$<salt>$<hash> with
// the unpadded standard base64 of the PHC string format.
func parsePHC(s string) (*password, error) {
	parts := strings.Split(s, "$")
	if len(parts) != 6 || parts[0] != "" {
		return nil, errors.New("the break-glass password hash is not a PHC string ($argon2id$v=19$m=...,t=...,p=...$salt$hash)")
	}
	if parts[1] != "argon2id" {
		return nil, fmt.Errorf("the break-glass password hash uses %s; only argon2id is accepted (R-AUTH-014)", parts[1])
	}
	if parts[2] != "v=19" {
		return nil, fmt.Errorf("the break-glass password hash has version %q; want v=19", parts[2])
	}
	pw := &password{}
	for _, kv := range strings.Split(parts[3], ",") {
		k, v, ok := strings.Cut(kv, "=")
		n, err := strconv.ParseUint(v, 10, 32)
		if !ok || err != nil {
			return nil, fmt.Errorf("the break-glass password hash has a bad parameter %q", kv)
		}
		switch k {
		case "m":
			pw.memory = uint32(n)
		case "t":
			pw.time = uint32(n)
		case "p":
			if n > 255 {
				return nil, errors.New("the break-glass password hash has more than 255 lanes")
			}
			pw.threads = uint8(n)
		default:
			return nil, fmt.Errorf("the break-glass password hash has an unknown parameter %q", k)
		}
	}
	if pw.memory < argonMinMemory || pw.time < argonMinTime || pw.threads < argonMinThreads {
		return nil, fmt.Errorf("the break-glass password hash has m=%d,t=%d,p=%d; R-AUTH-014 needs at least m=%d,t=%d,p=%d",
			pw.memory, pw.time, pw.threads, argonMinMemory, argonMinTime, argonMinThreads)
	}
	var err error
	if pw.salt, err = base64.RawStdEncoding.DecodeString(parts[4]); err != nil || len(pw.salt) < 8 {
		return nil, errors.New("the break-glass password hash has a bad salt")
	}
	if pw.key, err = base64.RawStdEncoding.DecodeString(parts[5]); err != nil || len(pw.key) < 16 {
		return nil, errors.New("the break-glass password hash has a bad hash part")
	}
	return pw, nil
}

// matches runs one Argon2id verification. It holds the memory its parameters
// name, which is why the caller holds a verification slot.
func (pw *password) matches(candidate string) bool {
	key := argon2.IDKey([]byte(candidate), pw.salt, pw.time, pw.memory, pw.threads, uint32(len(pw.key))) //nolint:gosec // the key length is the decoded hash's, at most a few hundred bytes
	return subtle.ConstantTimeCompare(key, pw.key) == 1
}

// HashPassword returns the PHC string of a password with the floor parameters,
// for operators who store a hash instead of the plaintext (local-profile.md).
func HashPassword(plain string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(plain), salt, argonMinTime, argonMinMemory, argonMinThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMinMemory, argonMinTime, argonMinThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}
