//go:build darwin && cgo

// SPDX-License-Identifier: AGPL-3.0-only
#import <Foundation/Foundation.h>
#import <LocalAuthentication/LocalAuthentication.h>
#import <Security/Security.h>
#import <Security/AuthSession.h>

@interface AeonLocalConfirmation : NSObject
@property(nonatomic, strong) LAContext *context;
@property(atomic) int result;
@end
@implementation AeonLocalConfirmation
@end

// Validate this running process, not a helper or a path selected by the caller.
// Ad-hoc Go builds intentionally fail closed. Hardened runtime and library
// validation are required so ordinary same-UID injection/debugging is refused.
static BOOL aeon_signed_daemon(void) {
 if (![NSProcessInfo.processInfo.processName isEqualToString:@"aeon-agentd"]) return NO;
 SecCodeRef code = NULL;
 SecRequirementRef requirement = NULL;
 CFDictionaryRef info = NULL;
 BOOL valid = NO;
 if (SecCodeCopySelf(kSecCSDefaultFlags, &code) != errSecSuccess) goto done;
 if (SecRequirementCreateWithString(CFSTR("anchor apple generic and certificate leaf[field.1.2.840.113635.100.6.1.13] exists"), kSecCSDefaultFlags, &requirement) != errSecSuccess) goto done;
 if (SecCodeCheckValidity(code, kSecCSStrictValidate, requirement) != errSecSuccess) goto done;
 if (SecCodeCopySigningInformation(code, kSecCSSigningInformation, &info) != errSecSuccess) goto done;
 {
  NSDictionary *signing = (__bridge NSDictionary *)info;
  NSNumber *flags = signing[(__bridge NSString *)kSecCodeInfoFlags];
  NSString *team = signing[(__bridge NSString *)kSecCodeInfoTeamIdentifier];
  NSDictionary *entitlements = signing[(__bridge NSString *)kSecCodeInfoEntitlementsDict];
  valid = team.length > 0 && (flags.unsignedIntValue & kSecCodeSignatureRuntime) != 0
    && ![entitlements[@"com.apple.security.get-task-allow"] boolValue]
    && ![entitlements[@"com.apple.security.cs.disable-library-validation"] boolValue]
    && ![entitlements[@"com.apple.security.cs.allow-dyld-environment-variables"] boolValue];
 }
done:
 if (info) CFRelease(info);
 if (requirement) CFRelease(requirement);
 if (code) CFRelease(code);
 return valid;
}

// No evaluatePolicy call: this must not open a system prompt.
int aeon_local_auth_capability(void) {
 @autoreleasepool {
  if (!aeon_signed_daemon()) return 1;
  SecuritySessionId session;
  SessionAttributeBits attributes;
  if (SessionGetInfo(callerSecuritySession, &session, &attributes) != errSecSuccess || !(attributes & sessionHasGraphicAccess)) return 2;
  LAContext *context = [LAContext new];
  context.touchIDAuthenticationAllowableReuseDuration = 0;
  if (![context canEvaluatePolicy:LAPolicyDeviceOwnerAuthentication error:nil]) return 3;
  return 0;
 }
}

void *aeon_local_auth_start(const char *reason, int *failure) {
 @autoreleasepool {
  *failure = 1;
  if (!aeon_signed_daemon()) return NULL;
  *failure = 2;
  SecuritySessionId session;
  SessionAttributeBits attributes;
  if (SessionGetInfo(callerSecuritySession, &session, &attributes) != errSecSuccess || !(attributes & sessionHasGraphicAccess)) return NULL;
  *failure = 3;
  AeonLocalConfirmation *state = [AeonLocalConfirmation new];
  state.context = [LAContext new];
  // Each watch needs a fresh evaluation; never reuse a prior unlock or context.
  state.context.touchIDAuthenticationAllowableReuseDuration = 0;
  if (![state.context canEvaluatePolicy:LAPolicyDeviceOwnerAuthentication error:nil]) return NULL;
  [state.context evaluatePolicy:LAPolicyDeviceOwnerAuthentication
    localizedReason:[NSString stringWithUTF8String:reason]
    reply:^(BOOL success, NSError *error) { state.result = success ? 1 : -1; }];
  *failure = 0;
  return (__bridge_retained void *)state;
 }
}
int aeon_local_auth_result(void *handle) {
 return ((__bridge AeonLocalConfirmation *)handle).result;
}
void aeon_local_auth_close(void *handle) {
 @autoreleasepool {
  AeonLocalConfirmation *state = (__bridge_transfer AeonLocalConfirmation *)handle;
  [state.context invalidate];
  state.context = nil;
 }
}
