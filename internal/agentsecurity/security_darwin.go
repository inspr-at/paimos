//go:build darwin && cgo && aeon_enclave

// SPDX-License-Identifier: AGPL-3.0-only
package agentsecurity

/*
#cgo CFLAGS: -x objective-c -fobjc-arc -Wno-deprecated-declarations
#cgo LDFLAGS: -framework Foundation -framework LocalAuthentication -framework Security
#include <stdlib.h>
int aeon_vault_read(const char *, void **, int *);
int aeon_vault_write(const char *, const void *, int, int);
int aeon_vault_delete(const char *);
int aeon_enclave_create(const char *, void **, int *);
int aeon_enclave_capability(void);
void *aeon_enclave_sign_start(const char *, const void *, int, const char *);
int aeon_enclave_sign_result(void *, void **, int *);
void aeon_enclave_sign_close(void *);
*/
import "C"

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"time"
	"unsafe"
)

type keychainVault struct{}
type enclaveSigner struct{}

func DefaultVault() Vault   { return keychainVault{} }
func DefaultSigner() Signer { return enclaveSigner{} }

func LocalAuthCapability() string {
	switch C.aeon_enclave_capability() {
	case 0:
		return "available"
	case 2:
		return "no_gui"
	case 3:
		return "policy"
	default:
		return "unsigned"
	}
}

func nativeError(status C.int) error {
	switch int(status) {
	case 0:
		return nil
	case -25300:
		return os.ErrNotExist
	case -25299:
		return ErrExists
	default:
		return ErrDenied
	}
}
func (keychainVault) Read(id string) ([]byte, error) {
	key := C.CString(id)
	defer C.free(unsafe.Pointer(key))
	var raw unsafe.Pointer
	var size C.int
	if err := nativeError(C.aeon_vault_read(key, &raw, &size)); err != nil {
		return nil, err
	}
	defer C.free(raw)
	return C.GoBytes(raw, size), nil
}
func (keychainVault) Write(id string, raw []byte, first bool) error {
	if len(raw) == 0 || len(raw) > 1<<20 {
		return ErrDenied
	}
	key := C.CString(id)
	defer C.free(unsafe.Pointer(key))
	var createOnly C.int
	if first {
		createOnly = 1
	}
	return nativeError(C.aeon_vault_write(key, unsafe.Pointer(&raw[0]), C.int(len(raw)), createOnly))
}
func (keychainVault) Delete(id string) error {
	key := C.CString(id)
	defer C.free(unsafe.Pointer(key))
	err := nativeError(C.aeon_vault_delete(key))
	if err == os.ErrNotExist {
		return nil
	}
	return err
}
func (enclaveSigner) Create(ctx context.Context, id string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	key := C.CString(id)
	defer C.free(unsafe.Pointer(key))
	var raw unsafe.Pointer
	var size C.int
	status := C.aeon_enclave_create(key, &raw, &size)
	if status == 1 {
		return "", ErrUnavailable
	}
	if err := nativeError(status); err != nil {
		return "", err
	}
	defer C.free(raw)
	return base64.StdEncoding.EncodeToString(C.GoBytes(raw, size)), nil
}
func (enclaveSigner) Sign(ctx context.Context, id string, hash []byte, reason string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(hash) != 32 {
		return "", ErrDenied
	}
	key := C.CString(id)
	defer C.free(unsafe.Pointer(key))
	text := C.CString(reason)
	defer C.free(unsafe.Pointer(text))
	handle := C.aeon_enclave_sign_start(key, unsafe.Pointer(&hash[0]), C.int(len(hash)), text)
	if handle == nil {
		return "", fmt.Errorf("Touch ID cancelled or unavailable; watch was not activated: %w", ErrDenied)
	}
	defer C.aeon_enclave_sign_close(handle)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		var raw unsafe.Pointer
		var size C.int
		switch C.aeon_enclave_sign_result(handle, &raw, &size) {
		case 1:
			defer C.free(raw)
			if err := ctx.Err(); err != nil {
				return "", err
			}
			return base64.StdEncoding.EncodeToString(C.GoBytes(raw, size)), nil
		case -1:
			return "", fmt.Errorf("Touch ID cancelled or unavailable; watch was not activated: %w", ErrDenied)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-tick.C:
		}
	}
}
