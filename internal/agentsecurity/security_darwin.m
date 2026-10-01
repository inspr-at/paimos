//go:build darwin && cgo && aeon_enclave

// SPDX-License-Identifier: AGPL-3.0-only
#import <Foundation/Foundation.h>
#import <Security/Security.h>
#import <Security/AuthSession.h>
#import <LocalAuthentication/LocalAuthentication.h>
#import <stdlib.h>
#import <string.h>

// Security.framework exports this macOS legacy ACL introspection API, but the
// public SDK omits its declaration. Weak-link it and fail closed if unavailable.
// Apple source: OSX/libsecurity_keychain/lib/SecTrustedApplicationPriv.h.
// CopyData returns only a display pathname and cannot establish a requirement.
extern OSStatus SecTrustedApplicationCopyRequirement(SecTrustedApplicationRef, SecRequirementRef *) __attribute__((weak_import));

// Release signing uses --identifier paimos-agentd. A renamed binary retains
// that identity. Team alone is insufficient: other signed tools do not qualify.
static BOOL signed_daemon(void) {
 SecCodeRef code = NULL; SecRequirementRef req = NULL; CFDictionaryRef info = NULL;
 BOOL ok = NO;
 if (SecCodeCopySelf(kSecCSDefaultFlags, &code) != errSecSuccess) goto done;
 if (SecRequirementCreateWithString(CFSTR("anchor apple generic and identifier \"paimos-agentd\" and certificate leaf[subject.OU] = \"P66J39QV6V\" and certificate leaf[field.1.2.840.113635.100.6.1.13] exists"), kSecCSDefaultFlags, &req) != errSecSuccess) goto done;
 if (SecCodeCheckValidity(code, kSecCSStrictValidate, req) != errSecSuccess) goto done;
 if (SecCodeCopySigningInformation(code, kSecCSSigningInformation, &info) != errSecSuccess) goto done;
 {
  NSDictionary *dict = (__bridge NSDictionary *)info;
  NSDictionary *entitlements = dict[(__bridge NSString *)kSecCodeInfoEntitlementsDict];
  unsigned flags = [dict[(__bridge NSString *)kSecCodeInfoFlags] unsignedIntValue];
  ok = (flags & kSecCodeSignatureRuntime) != 0 &&
    ![entitlements[@"com.apple.security.get-task-allow"] boolValue] &&
    ![entitlements[@"com.apple.security.cs.disable-library-validation"] boolValue] &&
    ![entitlements[@"com.apple.security.cs.allow-dyld-environment-variables"] boolValue];
 }
done:
 if (info) CFRelease(info); if (req) CFRelease(req); if (code) CFRelease(code);
 return ok;
}

int aeon_enclave_capability(void) {
 @autoreleasepool {
  if (!signed_daemon()) return 1;
  SecuritySessionId session; SessionAttributeBits attrs;
  if (SessionGetInfo(callerSecuritySession, &session, &attrs) != errSecSuccess || !(attrs & sessionHasGraphicAccess)) return 2;
  LAContext *context = [LAContext new]; context.interactionNotAllowed = YES;
  return [context canEvaluatePolicy:LAPolicyDeviceOwnerAuthenticationWithBiometrics error:nil] ? 0 : 3;
 }
}

static NSMutableDictionary *vault_query(const char *keyID) {
 if (!keyID || strlen(keyID) > 256) return nil;
 NSString *account = [NSString stringWithUTF8String:keyID];
 if (!account) return nil;
 return [@{(__bridge id)kSecClass:(__bridge id)kSecClassGenericPassword,
  (__bridge id)kSecAttrService:@"cm.paimos.aeon.agentd.pairing.v1",
  (__bridge id)kSecAttrAccount:account,
  (__bridge id)kSecUseAuthenticationUI:(__bridge id)kSecUseAuthenticationUIFail} mutableCopy];
}

// The legacy file Keychain retains the signed-daemon ACL and is device-local,
// not iCloud-synced. Its bridge strips accessibility and synchronizable
// attributes; no accessibility-class or device-bound backup guarantee is made.
// The injected add operation lets fixtures exercise storage without Keychain I/O.
OSStatus aeon_vault_add_item(const char *keyID, SecAccessRef access, NSData *data,
 OSStatus (^add)(CFDictionaryRef, CFTypeRef *)) {
 NSMutableDictionary *query = vault_query(keyID);
 if (!query || !access || !data || !add) return errSecParam;
 query[(__bridge id)kSecAttrSynchronizable] = @NO;
 query[(__bridge id)kSecAttrAccess] = (__bridge id)access;
 query[(__bridge id)kSecValueData] = data;
 return add((__bridge CFDictionaryRef)query, NULL);
}

static int copy_data(NSData *data, void **raw, int *size) {
 if (!data || data.length == 0 || data.length > 1024*1024) return errSecAuthFailed;
 *raw = malloc(data.length); if (!*raw) return errSecAllocate;
 memcpy(*raw, data.bytes, data.length); *size = (int)data.length; return errSecSuccess;
}

static SecAccessRef daemon_access(void);

// Inspect only in-memory access objects. Non-simple owner entries are not
// allow-any application lists: macOS returns errSecACLNotSimple for UID subjects.
// Preserve their order and authorization sets for comparison with our reference.
static NSArray *access_shape(SecAccessRef access, CFDataRef expected) {
 CFArrayRef acls = NULL; BOOL hasGrant = NO, hasIntegrity = NO, hasPartition = NO;
 NSMutableArray *nonSimple = [NSMutableArray new];
 uid_t owner = (uid_t)-1; gid_t group; SecAccessOwnerType ownerType = 0;
 if (!access || !expected || !SecTrustedApplicationCopyRequirement) return nil;
 if (SecAccessCopyOwnerAndACL(access, &owner, &group, &ownerType, NULL) != errSecSuccess || owner != 0 || ownerType != kSecUseOnlyUID) return nil;
 if (SecAccessCopyACLList(access, &acls) != errSecSuccess || !acls) return nil;
 for (id entry in (__bridge NSArray *)acls) {
  SecACLRef acl = (__bridge SecACLRef)entry;
  CFArrayRef auth = SecACLCopyAuthorizations(acl);
  if (!auth) goto denied;
  NSArray *permissions = CFBridgingRelease(auth);
  CFArrayRef apps = NULL; CFStringRef desc = NULL; SecKeychainPromptSelector prompt = 0;
  OSStatus status = SecACLCopyContents(acl, &apps, &desc, &prompt);
  if (desc) CFRelease(desc);
  if (status == errSecACLNotSimple) {
   if (apps) CFRelease(apps);
   [nonSimple addObject:[NSSet setWithArray:permissions]];
   continue;
  }
  BOOL integrity = permissions.count == 1 && [permissions containsObject:(__bridge id)kSecACLAuthorizationIntegrity];
  BOOL partition = permissions.count == 1 && [permissions containsObject:(__bridge id)kSecACLAuthorizationPartitionID];
  if (status == errSecSuccess && !apps && prompt == 0 && ((integrity && !hasIntegrity) || (partition && !hasPartition))) {
   hasIntegrity |= integrity; hasPartition |= partition;
   continue;
  }
  if (status != errSecSuccess || hasGrant || permissions.count != 1 || ![permissions containsObject:(__bridge id)kSecACLAuthorizationAny] || !apps || CFArrayGetCount(apps) != 1 || prompt != 0) { if (apps) CFRelease(apps); goto denied; }
  CFDataRef actual = NULL; SecRequirementRef requirement = NULL;
  status = SecTrustedApplicationCopyRequirement((SecTrustedApplicationRef)CFArrayGetValueAtIndex(apps, 0), &requirement);
  if (status == errSecSuccess && requirement) status = SecRequirementCopyData(requirement, kSecCSDefaultFlags, &actual);
  if (requirement) CFRelease(requirement);
  CFRelease(apps);
  BOOL matches = status == errSecSuccess && actual && CFEqual(actual, expected);
  if (actual) CFRelease(actual);
  if (!matches) goto denied;
  hasGrant = YES;
 }
 CFRelease(acls);
 return hasGrant ? nonSimple : nil;
denied:
 CFRelease(acls);
 return nil;
}

// Pure shape check: no signing validation, Keychain reads/writes or prompts.
BOOL aeon_vault_access_matches(SecAccessRef access, SecAccessRef reference, CFDataRef expected) {
 NSArray *actualShape = access_shape(access, expected);
 NSArray *referenceShape = access_shape(reference, expected);
 return actualShape && referenceShape && [actualShape isEqualToArray:referenceShape];
}

// Never adopt a same-user pre-created Keychain item with a permissive ACL.
// Build the reference in memory with the very same code used to create items.
static BOOL daemon_item_access(SecKeychainItemRef item) {
 SecAccessRef access = NULL, reference = NULL; SecCodeRef self = NULL;
 SecRequirementRef expectedRequirement = NULL; CFDataRef expected = NULL; BOOL ok = NO;
 if (SecKeychainItemCopyAccess(item, &access) != errSecSuccess) goto done;
 reference = daemon_access(); if (!reference) goto done;
 if (SecCodeCopySelf(kSecCSDefaultFlags, &self) != errSecSuccess || SecCodeCopyDesignatedRequirement(self, kSecCSDefaultFlags, &expectedRequirement) != errSecSuccess || SecRequirementCopyData(expectedRequirement, kSecCSDefaultFlags, &expected) != errSecSuccess) goto done;
 ok = aeon_vault_access_matches(access, reference, expected);
done:
 if (expected) CFRelease(expected); if (expectedRequirement) CFRelease(expectedRequirement); if (self) CFRelease(self);
 if (reference) CFRelease(reference); if (access) CFRelease(access);
 return ok;
}

int aeon_vault_read(const char *keyID, void **raw, int *size) {
 @autoreleasepool {
  if (!signed_daemon()) return errSecAuthFailed;
  NSMutableDictionary *query = vault_query(keyID); if (!query) return errSecParam;
  query[(__bridge id)kSecReturnData] = @YES;
  query[(__bridge id)kSecReturnRef] = @YES;
  CFTypeRef result = NULL;
  OSStatus status = SecItemCopyMatching((__bridge CFDictionaryRef)query, &result);
  if (status != errSecSuccess) return status;
  NSDictionary *values = CFBridgingRelease(result);
  if (![values isKindOfClass:NSDictionary.class]) return errSecAuthFailed;
  SecKeychainItemRef item = (__bridge SecKeychainItemRef)values[(__bridge id)kSecValueRef];
  NSData *data = values[(__bridge id)kSecValueData];
  return item && daemon_item_access(item) && [data isKindOfClass:NSData.class] ? copy_data(data, raw, size) : errSecAuthFailed;
 }
}

// The trusted application captures this validated daemon's designated
// requirement, so signed updates retain access without trusting a pathname.
// The ACL owner is root, not this user's UID: same-user code cannot change the
// ACL as owner. All operations are granted solely to the signed daemon.
SecAccessRef aeon_vault_access_for_application(SecTrustedApplicationRef app) {
 if (!app) return NULL;
 SecAccessRef access = NULL; SecACLRef acl = NULL;
 CFErrorRef error = NULL;
 // Empty authorization sets mean Any in the legacy backend. The affected
 // persisted UID entry exposes Any explicitly; match that shape in memory.
 NSArray *ownerAuthorizations = @[(__bridge id)kSecACLAuthorizationAny];
 access = SecAccessCreateWithOwnerAndACL(0, 0, kSecUseOnlyUID, (__bridge CFArrayRef)ownerAuthorizations, &error);
 if (error) CFRelease(error);
 if (!access) return NULL;
 NSArray *apps = @[(__bridge id)app];
 OSStatus status = SecACLCreateWithSimpleContents(access, (__bridge CFArrayRef)apps, CFSTR("Aeon pairing"), 0, &acl);
 if (status == errSecSuccess) {
  NSArray *authorizations = @[(__bridge id)kSecACLAuthorizationAny];
  status = SecACLUpdateAuthorizations(acl, (__bridge CFArrayRef)authorizations);
 }
 if (acl) CFRelease(acl);
 if (status != errSecSuccess) { CFRelease(access); return NULL; }
 return access;
}

static SecAccessRef daemon_access(void) {
 SecTrustedApplicationRef app = NULL;
 if (SecTrustedApplicationCreateFromPath(NULL, &app) != errSecSuccess) return NULL;
 SecAccessRef access = aeon_vault_access_for_application(app);
 CFRelease(app);
 return access;
}

int aeon_vault_write(const char *keyID, const void *raw, int size, int first) {
 @autoreleasepool {
  if (!signed_daemon()) return errSecAuthFailed;
  NSMutableDictionary *query = vault_query(keyID);
  if (!query || !raw || size < 1 || size > 1024*1024) return errSecParam;
  NSData *data = [NSData dataWithBytes:raw length:size];
  if (!first) {
   query[(__bridge id)kSecReturnRef] = @YES;
   SecKeychainItemRef item = NULL;
   OSStatus status = SecItemCopyMatching((__bridge CFDictionaryRef)query, (CFTypeRef *)&item);
   [query removeObjectForKey:(__bridge id)kSecReturnRef];
   if (status == errSecSuccess) {
    if (!daemon_item_access(item)) { CFRelease(item); return errSecAuthFailed; }
    status = SecKeychainItemModifyAttributesAndData(item, NULL, size, raw);
    CFRelease(item); return status;
   }
   if (status != errSecItemNotFound) return status;
  }
  SecAccessRef access = daemon_access(); if (!access) return errSecAuthFailed;
  OSStatus status = aeon_vault_add_item(keyID, access, data, ^OSStatus(CFDictionaryRef attrs, CFTypeRef *result) {
   return SecItemAdd(attrs, result);
  });
  CFRelease(access); return status;
 }
}
int aeon_vault_delete(const char *keyID) {
 @autoreleasepool {
  if (!signed_daemon()) return errSecAuthFailed;
  NSMutableDictionary *query = vault_query(keyID); if (!query) return errSecParam;
  query[(__bridge id)kSecReturnRef] = @YES;
  SecKeychainItemRef item = NULL;
  OSStatus status = SecItemCopyMatching((__bridge CFDictionaryRef)query, (CFTypeRef *)&item);
  if (status != errSecSuccess) return status;
  if (!daemon_item_access(item)) { CFRelease(item); return errSecAuthFailed; }
  status = SecKeychainItemDelete(item); CFRelease(item); return status;
 }
}

// errSecSuccess means a key with this tag already exists. Treat that as a
// duplicate instead of returning its public key without checking biometryCurrentSet.
int aeon_enclave_create_disposition(int copyStatus) {
 if (copyStatus == errSecItemNotFound) return errSecItemNotFound;
 if (copyStatus == errSecSuccess) return errSecDuplicateItem;
 return copyStatus;
}

static NSMutableDictionary *key_query(const char *keyID, LAContext *context) {
 if (!keyID || strlen(keyID) > 256) return nil;
 NSData *tag = [[NSString stringWithFormat:@"cm.paimos.aeon.consent.v1/%s",keyID] dataUsingEncoding:NSUTF8StringEncoding];
 NSMutableDictionary *query = [@{(__bridge id)kSecClass:(__bridge id)kSecClassKey,
  (__bridge id)kSecAttrApplicationTag:tag,
  (__bridge id)kSecAttrKeyType:(__bridge id)kSecAttrKeyTypeECSECPrimeRandom,
  (__bridge id)kSecAttrKeyClass:(__bridge id)kSecAttrKeyClassPrivate,
  (__bridge id)kSecAttrTokenID:(__bridge id)kSecAttrTokenIDSecureEnclave,
  (__bridge id)kSecReturnRef:@YES} mutableCopy];
 if (context) query[(__bridge id)kSecUseAuthenticationContext] = context;
 return query;
}
int aeon_enclave_create(const char *keyID, void **raw, int *size) {
 @autoreleasepool {
  if (!signed_daemon()) return errSecAuthFailed;
  LAContext *context = [LAContext new]; context.interactionNotAllowed = YES;
  if (![context canEvaluatePolicy:LAPolicyDeviceOwnerAuthenticationWithBiometrics error:nil]) return 1;
  NSMutableDictionary *query = key_query(keyID, context); if (!query) return errSecParam;
  SecKeyRef key = NULL;
  OSStatus status = SecItemCopyMatching((__bridge CFDictionaryRef)query, (CFTypeRef *)&key);
  // An existing tag is a create failure. Do not return its public key:
  // that path skipped the biometryCurrentSet check.
  int disposition = aeon_enclave_create_disposition(status);
  if (disposition != errSecItemNotFound) {
   if (key) CFRelease(key);
   return disposition;
  }
  {
   CFErrorRef error = NULL;
   SecAccessControlRef access = SecAccessControlCreateWithFlags(kCFAllocatorDefault, kSecAttrAccessibleWhenUnlockedThisDeviceOnly, kSecAccessControlBiometryCurrentSet | kSecAccessControlPrivateKeyUsage, &error);
   if (error) { CFRelease(error); error = NULL; }
   if (!access) return errSecAuthFailed;
   // biometryCurrentSet is the enclave ACL. SecAccessControl cannot also pin
   // the daemon's designated requirement; generic passwords do that above.
   // The server binds the canonical reason into the signature hash.
   NSDictionary *attrs = @{(__bridge id)kSecAttrKeyType:(__bridge id)kSecAttrKeyTypeECSECPrimeRandom,
    (__bridge id)kSecAttrKeySizeInBits:@256,
    (__bridge id)kSecAttrTokenID:(__bridge id)kSecAttrTokenIDSecureEnclave,
    (__bridge id)kSecPrivateKeyAttrs:@{(__bridge id)kSecAttrIsPermanent:@YES,
     (__bridge id)kSecAttrApplicationTag:query[(__bridge id)kSecAttrApplicationTag],
     (__bridge id)kSecAttrAccessControl:(__bridge id)access}};
   key = SecKeyCreateRandomKey((__bridge CFDictionaryRef)attrs, &error);
   CFRelease(access); if (error) CFRelease(error);
   if (!key) return 1; // No Secure Enclave: pairing may still use Aeon approval.
  }
  SecKeyRef pub = SecKeyCopyPublicKey(key); CFRelease(key);
  if (!pub) return errSecAuthFailed;
  CFErrorRef error = NULL;
  CFDataRef result = SecKeyCopyExternalRepresentation(pub, &error); CFRelease(pub);
  if (error) CFRelease(error);
  if (!result) return errSecAuthFailed;
  NSData *data = CFBridgingRelease(result); return copy_data(data, raw, size);
 }
}

@interface AeonEnclaveSigning : NSObject
@property(nonatomic, strong) LAContext *context;
@property(nonatomic, strong) NSData *signature;
@property(atomic) int result;
@end
@implementation AeonEnclaveSigning
@end

void *aeon_enclave_sign_start(const char *keyID, const void *hash, int size, const char *reason) {
 @autoreleasepool {
  if (aeon_enclave_capability() != 0 || !hash || size != 32 || !reason || reason[0] == 0) return NULL;
  AeonEnclaveSigning *state = [AeonEnclaveSigning new];
  state.context = [LAContext new];
  state.context.touchIDAuthenticationAllowableReuseDuration = 0;
  state.context.localizedReason = [NSString stringWithUTF8String:reason];
  state.context.localizedFallbackTitle = @""; // Never offer a password fallback.
  NSMutableDictionary *query = key_query(keyID, state.context); if (!query) return NULL;
  NSData *digest = [NSData dataWithBytes:hash length:size];
  dispatch_async(dispatch_get_global_queue(QOS_CLASS_USER_INITIATED, 0), ^{
   @autoreleasepool {
    SecKeyRef key = NULL;
    OSStatus status = SecItemCopyMatching((__bridge CFDictionaryRef)query, (CFTypeRef *)&key);
    if (status != errSecSuccess) { state.result = -1; return; }
    CFErrorRef error = NULL;
    CFDataRef signature = SecKeyCreateSignature(key, kSecKeyAlgorithmECDSASignatureDigestX962SHA256, (__bridge CFDataRef)digest, &error);
    CFRelease(key); if (error) CFRelease(error);
    state.signature = CFBridgingRelease(signature);
    state.result = state.signature ? 1 : -1;
   }
  });
  return (__bridge_retained void *)state;
 }
}
int aeon_enclave_sign_result(void *handle, void **raw, int *size) {
 AeonEnclaveSigning *state = (__bridge AeonEnclaveSigning *)handle;
 if (state.result != 1) return state.result;
 return copy_data(state.signature, raw, size) == errSecSuccess ? 1 : -1;
}
void aeon_enclave_sign_close(void *handle) {
 @autoreleasepool {
  AeonEnclaveSigning *state = (__bridge_transfer AeonEnclaveSigning *)handle;
  [state.context invalidate];
 }
}
