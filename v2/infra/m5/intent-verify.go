package main

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: intent-verify <public-pem> <intent> <signature>")
		os.Exit(2)
	}
	keyData, err := os.ReadFile(os.Args[1])
	if err != nil {
		panic(err)
	}
	block, rest := pem.Decode(keyData)
	if block == nil || block.Type != "PUBLIC KEY" || len(rest) != 0 {
		panic("unreviewed public key encoding")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		panic(err)
	}
	key, ok := parsed.(ed25519.PublicKey)
	if !ok {
		panic("public key is not Ed25519")
	}
	intent, err := os.ReadFile(os.Args[2])
	if err != nil {
		panic(err)
	}
	signature, err := os.ReadFile(os.Args[3])
	if err != nil {
		panic(err)
	}
	if !ed25519.Verify(key, intent, signature) {
		panic("intent signature invalid")
	}
}
