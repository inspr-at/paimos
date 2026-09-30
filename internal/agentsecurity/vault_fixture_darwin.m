//go:build darwin && cgo && aeon_enclave

// SPDX-License-Identifier: AGPL-3.0-only
#import <Foundation/Foundation.h>
#import <Security/Security.h>

extern OSStatus aeon_vault_add_item(const char *, SecAccessRef, NSData *, OSStatus (^)(CFDictionaryRef, CFTypeRef *));

// A memory-only SecItem layer. Mirror the legacy bridge's pruning in Apple's
// OSX/libsecurity_keychain/lib/SecItem.cpp, SecItemCopyTranslatedAttributes.
// The ACL is an opaque fixture identity; this does not qualify a real Mac ACL.
@interface AeonLegacySecItemFixture : NSObject
@property(nonatomic, strong) NSDictionary *stored;
- (OSStatus)add:(CFDictionaryRef)attributes injectAccessibility:(BOOL)inject;
- (NSDictionary *)copyMatching:(NSString *)account;
@end
@implementation AeonLegacySecItemFixture
- (OSStatus)add:(CFDictionaryRef)attributes injectAccessibility:(BOOL)inject {
 NSMutableDictionary *item = [(__bridge NSDictionary *)attributes mutableCopy];
 if ([item[(__bridge id)kSecUseDataProtectionKeychain] boolValue] || [item[(__bridge id)kSecAttrSynchronizable] boolValue] || !item[(__bridge id)kSecAttrAccess]) return errSecParam;
 if (self.stored) return errSecDuplicateItem;
 if (inject) item[(__bridge id)kSecAttrAccessible] = (__bridge id)kSecAttrAccessibleWhenUnlockedThisDeviceOnly;
 for (id key in @[(__bridge id)kSecAttrAccessible, (__bridge id)kSecAttrAccessControl,
  (__bridge id)kSecAttrAccessGroup, (__bridge id)kSecAttrSynchronizable,
  (__bridge id)kSecUseDataProtectionKeychain,
  (__bridge id)kSecUseAuthenticationUI]) [item removeObjectForKey:key];
 self.stored = [item copy];
 return errSecSuccess;
}
- (NSDictionary *)copyMatching:(NSString *)account {
 return [self.stored[(__bridge id)kSecAttrAccount] isEqual:account] ? [self.stored copy] : nil;
}
@end

int aeon_vault_legacy_stored_attributes(const char *keyID, int inject) {
 @autoreleasepool {
  if (!keyID) return 0;
  AeonLegacySecItemFixture *items = [AeonLegacySecItemFixture new];
  NSString *acl = @"signed-daemon-ACL-fixture";
  NSData *data = [@"fixture-password" dataUsingEncoding:NSUTF8StringEncoding];
  __block BOOL submittedLegacy = NO;
  OSStatus status = aeon_vault_add_item(keyID, (__bridge SecAccessRef)acl, data, ^OSStatus(CFDictionaryRef attrs, CFTypeRef *result) {
   NSDictionary *query = (__bridge NSDictionary *)attrs;
   submittedLegacy = !query[(__bridge id)kSecAttrAccessible] &&
    [query[(__bridge id)kSecAttrSynchronizable] isEqual:@NO] &&
    ![query[(__bridge id)kSecUseDataProtectionKeychain] boolValue];
   return [items add:attrs injectAccessibility:inject != 0];
  });
  if (status != errSecSuccess) return 0;
  NSDictionary *stored = [items copyMatching:[NSString stringWithUTF8String:keyID]];
  if (!stored) return 0;
  int observed = 0;
  if ([stored[(__bridge id)kSecClass] isEqual:(__bridge id)kSecClassGenericPassword]) observed |= 1;
  if ([stored[(__bridge id)kSecAttrService] isEqual:@"cm.paimos.aeon.agentd.pairing.v1"]) observed |= 2;
  if ([stored[(__bridge id)kSecAttrAccess] isEqual:acl]) observed |= 4;
  if ([stored[(__bridge id)kSecValueData] isEqual:data]) observed |= 8;
  if (!stored[(__bridge id)kSecAttrAccessible]) observed |= 16;
  if (!stored[(__bridge id)kSecAttrSynchronizable]) observed |= 32;
  if (!stored[(__bridge id)kSecAttrAccessControl] && !stored[(__bridge id)kSecAttrAccessGroup] && !stored[(__bridge id)kSecUseDataProtectionKeychain]) observed |= 64;
  if (submittedLegacy) observed |= 128;
  return observed;
 }
}
