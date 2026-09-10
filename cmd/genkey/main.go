// genkey generates a fresh Ed25519 signing seed and prints both halves:
// the seed (for this server's LICENSE_SIGNING_SEED env var) and the public key
// (for every consuming product to embed at build time to verify license.lic).
//
// Run once per deployment: go run ./cmd/genkey
package main

import (
	"fmt"
	"log"

	"github.com/andyresta/licence-product/internal/crypto"
)

func main() {
	seedHex, err := crypto.GenerateSeedHex()
	if err != nil {
		log.Fatalf("generate seed: %v", err)
	}
	signer, err := crypto.NewSigner(seedHex)
	if err != nil {
		log.Fatalf("derive signer: %v", err)
	}
	fmt.Println("LICENSE_SIGNING_SEED (server-side secret — set this in the server's environment, never commit it):")
	fmt.Println(seedHex)
	fmt.Println()
	fmt.Println("Public key (embed this in every product that verifies license.lic offline):")
	fmt.Println(signer.PublicKeyHex())
}
