// Package secrets implements at-rest encryption for sensitive values (most
// notably GitHub Personal Access Tokens) stored in the SQLite database.
//
// Threat model: an attacker who lifts the SQLite file off disk should NOT be
// able to recover GitHub tokens without also obtaining the encryption key,
// which lives outside the database (env var, keyfile, or distinct file with
// 0600 perms). The service is intentionally simple — AES-256-GCM with a
// random nonce per encrypt, prefixed with a single-byte version so future
// key-rotation rolls can be deployed without a one-shot migration.
//
// Ciphertext layout: [1 byte version=0x01][12 byte nonce][N bytes ciphertext+tag]
//
// Key resolution order (the first source that yields a valid 32-byte key wins):
//  1. CIX_SECRET_KEY  — base64 (std or url) or hex-encoded 32 bytes
//  2. CIX_SECRET_KEYFILE — absolute path to a file containing the key
//     (raw 32 bytes, base64, or hex). File must have 0600 permissions or
//     stricter or the service refuses to start.
//  3. <datadir>/.secret_key — auto-generated on first run when neither env
//     is set. Written with 0600. The operator can rotate by deleting the
//     file before any github_tokens row exists; once tokens exist, deleting
//     the key bricks them — Open() refuses to start in that situation.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// KeySize is the AES-256 key length in bytes.
const KeySize = 32

// nonceSize is the GCM nonce length in bytes. 12 is the AEAD-recommended
// value and what crypto/cipher.NewGCM defaults to.
const nonceSize = 12

// currentVersion is the version byte prepended to every ciphertext. Bump
// when changing the AEAD or KDF; older rows decrypt via legacy branches.
const currentVersion byte = 0x01

// minKeyFilePerm is the maximum permissive bit set we accept on a key
// file. 0o600 (owner read/write only) — any group/other bits make us
// refuse to read it.
const minKeyFilePerm os.FileMode = 0o077

var (
	// ErrNoKey means key resolution found no candidate at any of the
	// resolution sources. Surfaced at Open() so the operator sees a clear
	// startup error rather than a confusing "decrypt failed" later.
	ErrNoKey = errors.New("no encryption key configured (CIX_SECRET_KEY / CIX_SECRET_KEYFILE / generated keyfile)")

	// ErrCiphertextTooShort signals a malformed input — short of the
	// version+nonce+tag overhead, so it cannot even be parsed.
	ErrCiphertextTooShort = errors.New("ciphertext too short")

	// ErrUnknownVersion is returned when the version byte does not match
	// any decryption branch we know about. Usually means the DB came from
	// a future server build.
	ErrUnknownVersion = errors.New("unknown ciphertext version")
)

// Service is the in-process encryption helper. Construct via Open. Safe for
// concurrent use — the underlying cipher.AEAD is stateless.
type Service struct {
	aead       cipher.AEAD
	source     string // human-friendly description for logs / status: "env", "keyfile:/path", "generated:/path"
	autogenKey bool   // true when we wrote a fresh keyfile this boot
}

// OpenOptions configures Open. EnvVar / EnvKeyFileVar override the default
// "CIX_SECRET_KEY" / "CIX_SECRET_KEYFILE" lookup; useful for tests.
// DataDir is the directory the auto-generated keyfile is created in when
// nothing else resolved.
type OpenOptions struct {
	EnvVar        string // default: "CIX_SECRET_KEY"
	EnvKeyFileVar string // default: "CIX_SECRET_KEYFILE"
	DataDir       string // required when fallback generation is desired
	Logger        *slog.Logger
	// AllowGenerate, when true, lets Open() create a keyfile under
	// DataDir if no other key source resolved. Tests that exercise the
	// "no key" path can leave this false to force ErrNoKey.
	AllowGenerate bool
}

// Open resolves the encryption key from the configured sources and returns
// a ready-to-use Service. Returns ErrNoKey when AllowGenerate is false and
// none of the sources yield a key.
func Open(opts OpenOptions) (*Service, error) {
	envKey := opts.EnvVar
	if envKey == "" {
		envKey = "CIX_SECRET_KEY"
	}
	envKeyFile := opts.EnvKeyFileVar
	if envKeyFile == "" {
		envKeyFile = "CIX_SECRET_KEYFILE"
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	var (
		key        []byte
		source     string
		autogen    bool
	)

	// 1. Direct env var.
	if v := strings.TrimSpace(os.Getenv(envKey)); v != "" {
		parsed, err := decodeKey(v)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", envKey, err)
		}
		key = parsed
		source = "env:" + envKey
	}

	// 2. Keyfile path env var.
	if key == nil {
		if path := strings.TrimSpace(os.Getenv(envKeyFile)); path != "" {
			parsed, err := readKeyFile(path)
			if err != nil {
				return nil, fmt.Errorf("%s=%s: %w", envKeyFile, path, err)
			}
			key = parsed
			source = "keyfile:" + path
		}
	}

	// 3. Auto-generated keyfile under DataDir.
	if key == nil && opts.AllowGenerate && opts.DataDir != "" {
		path := filepath.Join(opts.DataDir, ".secret_key")
		existing, err := readKeyFile(path)
		switch {
		case err == nil:
			key = existing
			source = "keyfile:" + path
		case errors.Is(err, os.ErrNotExist):
			generated, gerr := generateKeyFile(path)
			if gerr != nil {
				return nil, fmt.Errorf("auto-generate keyfile %s: %w", path, gerr)
			}
			key = generated
			source = "generated:" + path
			autogen = true
			logger.Warn("secrets: generated new encryption keyfile",
				"path", path,
				"action_required", "back this file up — losing it makes all encrypted github_tokens unreadable")
		default:
			return nil, fmt.Errorf("read keyfile %s: %w", path, err)
		}
	}

	if key == nil {
		return nil, ErrNoKey
	}
	if len(key) != KeySize {
		return nil, fmt.Errorf("encryption key must be %d bytes, got %d (source=%s)", KeySize, len(key), source)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes.NewCipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("cipher.NewGCM: %w", err)
	}
	// Wipe the key bytes from the local slice now that the AEAD holds its
	// internal copy. (cipher.NewGCM copies the key into the gcm struct.)
	for i := range key {
		key[i] = 0
	}
	runtime.KeepAlive(key)

	return &Service{
		aead:       aead,
		source:    source,
		autogenKey: autogen,
	}, nil
}

// Source returns a human-friendly identifier of where the key came from.
// Used by /api/v1/status (admin-only) so operators can confirm the source
// without exposing the key itself.
func (s *Service) Source() string {
	if s == nil {
		return ""
	}
	return s.source
}

// Autogenerated reports whether Open() created a new keyfile this boot.
// Triggers a one-time warning banner in the startup log so the operator
// knows to back up the file before it gets paved by a redeploy.
func (s *Service) Autogenerated() bool {
	if s == nil {
		return false
	}
	return s.autogenKey
}

// Encrypt returns the canonical ciphertext layout for plaintext. Each call
// uses a fresh random nonce.
func (s *Service) Encrypt(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, nonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("nonce: %w", err)
	}
	out := make([]byte, 0, 1+nonceSize+len(plaintext)+s.aead.Overhead())
	out = append(out, currentVersion)
	out = append(out, nonce...)
	out = s.aead.Seal(out, nonce, plaintext, nil)
	return out, nil
}

// Decrypt reverses Encrypt. Returns ErrCiphertextTooShort or
// ErrUnknownVersion for malformed input; the underlying AEAD error
// otherwise (likely "cipher: message authentication failed" — surface as
// a generic decryption failure to avoid oracling).
func (s *Service) Decrypt(ciphertext []byte) ([]byte, error) {
	if len(ciphertext) < 1+nonceSize+s.aead.Overhead() {
		return nil, ErrCiphertextTooShort
	}
	version := ciphertext[0]
	if version != currentVersion {
		return nil, ErrUnknownVersion
	}
	nonce := ciphertext[1 : 1+nonceSize]
	body := ciphertext[1+nonceSize:]
	plain, err := s.aead.Open(nil, nonce, body, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	return plain, nil
}

// ConstantTimeEqual compares two byte slices in constant time. Re-exported
// so callers don't need to import subtle themselves for things like
// HMAC-secret comparisons.
func ConstantTimeEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare(a, b) == 1
}

// --- helpers ---

// decodeKey accepts the key in one of three encodings:
//   - hex (64 chars)
//   - base64 (std or url, with or without padding)
//   - raw 32 bytes (treated as already-decoded — only matched when len is
//     exactly KeySize, which would also match a 32-byte raw read from a
//     file but never a plausible env var value)
func decodeKey(v string) ([]byte, error) {
	v = strings.TrimSpace(v)
	if len(v) == 0 {
		return nil, fmt.Errorf("empty key")
	}
	// hex first — unambiguous given the strict 64-char + hex alphabet.
	if len(v) == KeySize*2 {
		if b, err := hex.DecodeString(v); err == nil && len(b) == KeySize {
			return b, nil
		}
	}
	// base64 (try url then std; both with and without padding).
	for _, enc := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.StdEncoding} {
		if b, err := enc.DecodeString(v); err == nil && len(b) == KeySize {
			return b, nil
		}
	}
	// Last resort: caller passed raw bytes directly.
	if len(v) == KeySize {
		return []byte(v), nil
	}
	return nil, fmt.Errorf("key must be %d bytes encoded as hex or base64", KeySize)
}

// readKeyFile loads a key file from disk. Refuses files whose permissions
// allow group/other read — the operator obviously meant 0600 and a wider
// mask is almost always a mistake.
func readKeyFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&minKeyFilePerm != 0 {
		return nil, fmt.Errorf("keyfile %s has insecure permissions %o, expected 0600", path, info.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	// File may be hex / base64 / raw bytes; try the structured forms first
	// so we don't accidentally treat a 32-byte hex string as raw.
	if v := strings.TrimSpace(string(raw)); v != "" {
		if b, err := decodeKey(v); err == nil {
			return b, nil
		}
	}
	if len(raw) == KeySize {
		return raw, nil
	}
	return nil, fmt.Errorf("keyfile %s did not contain %d bytes of key material", path, KeySize)
}

// generateKeyFile creates a new 32-byte CSPRNG key and writes it to path
// (hex-encoded for human readability) with 0600 permissions. Returns the
// generated key bytes.
func generateKeyFile(path string) ([]byte, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir keyfile parent: %w", err)
	}
	key := make([]byte, KeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("rand: %w", err)
	}
	// Hex encoding is friendlier to ops (curl-able, copy-pasteable) and
	// adds nothing to the security cost — the bytes hit disk regardless.
	encoded := []byte(hex.EncodeToString(key) + "\n")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		return nil, fmt.Errorf("write keyfile: %w", err)
	}
	return key, nil
}
