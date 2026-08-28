// Command pushkeygen generates a VAPID key pair for Web Push (v0.6.30 app
// notifications). Run it once per deployment; the private key goes into the
// server config's [push] section (NEVER into bbolt or the repo), and the
// public key is served by /api/push/vapid-key.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
)

func main() {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		fmt.Fprintln(os.Stderr, "generate:", err)
		os.Exit(1)
	}
	pub := key.PublicKey
	rawPub := elliptic.Marshal(pub.Curve, pub.X, pub.Y)
	priv := make([]byte, 32)
	key.D.FillBytes(priv)
	fmt.Printf("vapid_public_key  = %s\n", base64.RawURLEncoding.EncodeToString(rawPub))
	fmt.Printf("vapid_private_key = %s\n", base64.RawURLEncoding.EncodeToString(priv))
}
