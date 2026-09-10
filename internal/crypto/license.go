// Package crypto implements license.lic signing and verification. A license.lic is a
// JSON payload plus an Ed25519 signature, encoded as "<base64url payload>.<base64url
// signature>" (JWT-shaped, but with no header/alg negotiation — there is exactly one
// scheme, Ed25519, so there is nothing to negotiate and nothing for a client to be
// tricked into downgrading).
//
// The server holds the private key (LICENSE_SIGNING_SEED) and is the only thing that
// ever calls Sign. The product being licensed (FixUnit, and any future product) embeds
// only the public key and calls a client-side equivalent of Verify — that split is the
// entire point: a copied/tampered license.lic fails Verify without the product ever
// needing to contact this server again (see README's "Runtime validation" section).
package crypto

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// LicensePayload is the signed content of a license.lic. Field names are kept short
// and stable — this shape is a cross-repo contract with every product that verifies
// license.lic client-side, so it must not change in a backwards-incompatible way once
// any product ships against it.
type LicensePayload struct {
	Email              string    `json:"email"`
	ProductCode        string    `json:"product_code"`
	MachineFingerprint string    `json:"machine_fingerprint"`
	ActivationID       string    `json:"activation_id"`
	IssuedAt           time.Time `json:"issued_at"`
	ExpiresAt          time.Time `json:"expires_at"`
}

// Signer holds the Ed25519 private key used to issue license.lic content.
type Signer struct {
	priv ed25519.PrivateKey
}

// NewSigner derives the Ed25519 keypair from a 32-byte seed given as 64 hex chars
// (LICENSE_SIGNING_SEED) — a seed rather than a raw generated key so the same value
// can be stored as one plain env var and regenerating the keypair from it is
// deterministic (see GenerateSeed for producing one).
func NewSigner(seedHex string) (*Signer, error) {
	seed, err := hex.DecodeString(strings.TrimSpace(seedHex))
	if err != nil {
		return nil, fmt.Errorf("crypto: signing seed is not valid hex: %w", err)
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("crypto: signing seed must be %d bytes (%d hex chars), got %d bytes", ed25519.SeedSize, ed25519.SeedSize*2, len(seed))
	}
	return &Signer{priv: ed25519.NewKeyFromSeed(seed)}, nil
}

// PublicKeyHex returns the public key half as hex — this is the value every consuming
// product embeds at build time to verify license.lic offline.
func (s *Signer) PublicKeyHex() string {
	pub := s.priv.Public().(ed25519.PublicKey)
	return hex.EncodeToString(pub)
}

// Sign encodes and signs payload, returning the license.lic content.
func (s *Signer) Sign(payload LicensePayload) (string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("crypto: marshal payload: %w", err)
	}
	sig := ed25519.Sign(s.priv, body)
	return base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// Verify checks a license.lic's signature against the given hex-encoded public key and
// returns the decoded payload. It does NOT check ExpiresAt or MachineFingerprint —
// those are policy decisions for the caller (e.g. Deactivate accepts an expired
// license.lic on purpose, since freeing a seat should work even after expiry; a
// runtime "is this license still good to run" check is a different, stricter caller).
func Verify(licenseLic, publicKeyHex string) (LicensePayload, error) {
	pubBytes, err := hex.DecodeString(strings.TrimSpace(publicKeyHex))
	if err != nil || len(pubBytes) != ed25519.PublicKeySize {
		return LicensePayload{}, fmt.Errorf("crypto: invalid public key")
	}
	parts := strings.SplitN(licenseLic, ".", 2)
	if len(parts) != 2 {
		return LicensePayload{}, fmt.Errorf("crypto: malformed license content")
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return LicensePayload{}, fmt.Errorf("crypto: malformed license payload")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return LicensePayload{}, fmt.Errorf("crypto: malformed license signature")
	}
	if !ed25519.Verify(ed25519.PublicKey(pubBytes), body, sig) {
		return LicensePayload{}, fmt.Errorf("crypto: signature verification failed")
	}
	var payload LicensePayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return LicensePayload{}, fmt.Errorf("crypto: malformed license payload json: %w", err)
	}
	return payload, nil
}

// GenerateSeedHex returns a fresh random 32-byte Ed25519 seed as 64 hex chars — used
// only by the one-off `go run ./cmd/genkey` helper, never at server-runtime.
func GenerateSeedHex() (string, error) {
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(priv.Seed()), nil
}
