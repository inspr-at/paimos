// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"

	"filippo.io/age"
	"filippo.io/age/agessh"
	"golang.org/x/crypto/ssh"
)

const (
	machineNotifierMaxAgeRecipients = 8
	machineNotifierMaxRecipientSize = 1024
)

var errMachineNotifierAgeRecipients = errors.New("machine notifier age recipients are invalid")

func parseMachineNotifierAgeRecipients(encoded []string) ([]age.Recipient, error) {
	if len(encoded) == 0 || len(encoded) > machineNotifierMaxAgeRecipients {
		return nil, errMachineNotifierAgeRecipients
	}
	recipients := make([]age.Recipient, 0, len(encoded))
	seen := make(map[string]struct{}, len(encoded))
	for _, value := range encoded {
		value = strings.TrimSpace(value)
		if value == "" || len([]byte(value)) > machineNotifierMaxRecipientSize {
			return nil, errMachineNotifierAgeRecipients
		}
		recipient, canonical, err := parseMachineNotifierAgeRecipient(value)
		if err != nil {
			return nil, errMachineNotifierAgeRecipients
		}
		if _, duplicate := seen[canonical]; duplicate {
			return nil, errMachineNotifierAgeRecipients
		}
		seen[canonical] = struct{}{}
		recipients = append(recipients, recipient)
	}
	return recipients, nil
}

func parseMachineNotifierAgeRecipient(value string) (age.Recipient, string, error) {
	if strings.HasPrefix(value, "age1") {
		recipient, err := age.ParseX25519Recipient(value)
		if err != nil {
			return nil, "", err
		}
		return recipient, recipient.String(), nil
	}
	if !strings.HasPrefix(value, "ssh-ed25519 ") && !strings.HasPrefix(value, "ssh-rsa ") {
		return nil, "", errMachineNotifierAgeRecipients
	}
	publicKey, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(value))
	if err != nil || len(bytes.TrimSpace(rest)) != 0 {
		return nil, "", errMachineNotifierAgeRecipients
	}
	canonical := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(publicKey)))
	recipient, err := agessh.ParseRecipient(canonical)
	if err != nil {
		return nil, "", err
	}
	return recipient, canonical, nil
}

func encryptMachineNotifierCredential(credential string, recipients []age.Recipient) (string, error) {
	var ciphertext bytes.Buffer
	writer, err := age.Encrypt(&ciphertext, recipients...)
	if err != nil {
		return "", fmt.Errorf("initialize age encryption: %w", err)
	}
	if _, err := io.WriteString(writer, credential+"\n"); err != nil {
		return "", fmt.Errorf("encrypt machine notifier credential: %w", err)
	}
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("finalize machine notifier credential: %w", err)
	}
	return base64.StdEncoding.EncodeToString(ciphertext.Bytes()), nil
}
