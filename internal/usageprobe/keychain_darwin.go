// SPDX-License-Identifier: AGPL-3.0-only
//go:build darwin && cgo

package usageprobe

/*
#cgo CFLAGS: -Wno-deprecated-declarations
#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include <Security/Security.h>
#include <CoreFoundation/CoreFoundation.h>
#include <stdlib.h>
#include <string.h>
static int read_claude_login(void **out, int *length) {
  const void *keys[] = {kSecClass, kSecAttrService, kSecReturnData, kSecMatchLimit, kSecUseAuthenticationUI};
  const void *values[] = {kSecClassGenericPassword, CFSTR("Claude Code-credentials"), kCFBooleanTrue, kSecMatchLimitOne, kSecUseAuthenticationUIFail};
  CFDictionaryRef query = CFDictionaryCreate(NULL, keys, values, 5, &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
  CFTypeRef result = NULL;
  OSStatus status = SecItemCopyMatching(query, &result);
  CFRelease(query);
  if (status != errSecSuccess || !result) return 0;
  if (CFGetTypeID(result) != CFDataGetTypeID()) { CFRelease(result); return 0; }
  CFIndex size = CFDataGetLength((CFDataRef)result);
  if (size < 1 || size > 65536) { CFRelease(result); return 0; }
  *out = malloc(size);
  if (!*out) { CFRelease(result); return 0; }
  memcpy(*out, CFDataGetBytePtr((CFDataRef)result), size);
  *length = (int)size;
  CFRelease(result);
  return 1;
}
static void discard_login(void *p, int length) { memset(p, 0, length); free(p); }
*/
import "C"

import (
	"os"
	"path/filepath"
	"unsafe"
)

func claudeKeychain(home string) ([]byte, error) {
	userHome, err := os.UserHomeDir()
	// The default service must never stand in for a different selected profile.
	if err != nil || home != filepath.Join(userHome, ".claude") {
		return nil, errLogin
	}
	var data unsafe.Pointer
	var length C.int
	if C.read_claude_login(&data, &length) != 1 {
		return nil, errLogin
	}
	defer C.discard_login(data, length)
	return C.GoBytes(data, length), nil
}
