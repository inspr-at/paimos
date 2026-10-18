// SPDX-License-Identifier: AGPL-3.0-only
package config

import (
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
)

// PhonePushConfig is host-provisioned VAPID configuration. The private key is
// read from existing file-based secret storage, never generated into the DB.
type PhonePushConfig struct {
	PublicKey  string `json:"public_key"`
	PrivateKey string `json:"private_key"`
	Subject    string `json:"subject"`
}

func phonePushFromFile(file string) (*PhonePushConfig, error) {
	if file == "" {
		return nil, nil
	}
	f, err := os.Open(file)
	if err != nil {
		return nil, fmt.Errorf("AEON_PHONE_PUSH_VAPID_FILE unavailable")
	}
	defer f.Close()
	var cfg PhonePushConfig
	d := json.NewDecoder(io.LimitReader(f, 4097))
	d.DisallowUnknownFields()
	var extra any
	if d.Decode(&cfg) != nil || d.Decode(&extra) != io.EOF {
		return nil, fmt.Errorf("AEON_PHONE_PUSH_VAPID_FILE invalid")
	}
	raw, err := base64.RawURLEncoding.DecodeString(cfg.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("invalid VAPID key pair")
	}
	key, err := ecdh.P256().NewPrivateKey(raw)
	if err != nil || base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()) != cfg.PublicKey {
		return nil, fmt.Errorf("invalid VAPID key pair")
	}
	u, err := url.Parse(cfg.Subject)
	if err != nil || !(u.Scheme == "mailto" && u.Opaque != "" || u.Scheme == "https" && u.Host != "" && u.User == nil) {
		return nil, fmt.Errorf("invalid VAPID subject")
	}
	return &cfg, nil
}
