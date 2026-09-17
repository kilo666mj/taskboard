package main

import (
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	webpush "github.com/SherClockHolmes/webpush-go"
	pwakit "github.com/kilo666mj/pwa-kit"
)

func main() {
	output := flag.String("output", "ansible/private.yml", "protected YAML file to create")
	contact := flag.String("vapid-contact", "", "Required public email or HTTPS Web Push contact")
	flag.Parse()
	if _, err := pwakit.NormalizeContact(*contact); err != nil {
		fail(err)
	}
	if _, err := os.Stat(*output); err == nil {
		fmt.Fprintln(os.Stderr, "refusing to overwrite existing configuration")
		os.Exit(1)
	} else if !os.IsNotExist(err) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	secret := make([]byte, 48)
	if _, err := rand.Read(secret); err != nil {
		fail(err)
	}
	privateKey, publicKey, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		fail(err)
	}
	content := fmt.Sprintf("taskboard_auth_token: %q\ntaskboard_vapid_public_key: %q\ntaskboard_vapid_private_key: %q\ntaskboard_vapid_contact: %q\n", base64.RawURLEncoding.EncodeToString(secret), publicKey, privateKey, *contact)
	if err := os.MkdirAll(filepath.Dir(*output), 0700); err != nil {
		fail(err)
	}
	temporary := *output + ".tmp"
	if err := os.WriteFile(temporary, []byte(content), 0600); err != nil {
		fail(err)
	}
	if err := os.Rename(temporary, *output); err != nil {
		fail(err)
	}
	fmt.Printf("Created protected Taskboard deployment configuration at %s\n", *output)
}

func fail(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
