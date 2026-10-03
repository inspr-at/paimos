//go:build darwin && cgo && aeon_enclave

// SPDX-License-Identifier: AGPL-3.0-only
#import <Foundation/Foundation.h>
#import <Security/Security.h>
#import <unistd.h>

// Synthetic requirements make these in-memory checks work without a signed
// executable. These legacy APIs are exported but not declared by the SDK.
extern OSStatus SecTrustedApplicationCreateFromRequirement(const char *, SecRequirementRef, SecTrustedApplicationRef *) __attribute__((weak_import));
extern SecAccessRef aeon_vault_access_for_application(SecTrustedApplicationRef);
extern BOOL aeon_vault_access_matches(SecAccessRef, SecAccessRef, CFDataRef);
extern BOOL aeon_vault_persisted_access_matches(SecAccessRef, SecAccessRef, CFDataRef);
extern void aeon_vault_free_owner_acl(CSSM_ACL_OWNER_PROTOTYPE_PTR, uint32, CSSM_ACL_ENTRY_INFO_PTR);

static OSStatus add_fixture_acl(SecAccessRef access, NSArray *apps, NSArray *permissions, SecKeychainPromptSelector prompt) {
 SecACLRef acl = NULL;
 OSStatus status = SecACLCreateWithSimpleContents(access, (__bridge CFArrayRef)apps, CFSTR("Aeon pairing fixture"), prompt, &acl);
 if (status == errSecSuccess) status = SecACLUpdateAuthorizations(acl, (__bridge CFArrayRef)permissions);
 if (acl) CFRelease(acl);
 return status;
}

static NSArray *fixture_acls(SecAccessRef access) {
 CFArrayRef acls = NULL;
 if (SecAccessCopyACLList(access, &acls) != errSecSuccess || !acls) return nil;
 return CFBridgingRelease(acls);
}

static SecACLRef copy_fixture_acl(SecAccessRef access, BOOL simple) {
 for (id entry in fixture_acls(access)) {
  SecACLRef acl = (__bridge SecACLRef)entry;
  CFArrayRef apps = NULL; CFStringRef desc = NULL; SecKeychainPromptSelector prompt = 0;
  OSStatus status = SecACLCopyContents(acl, &apps, &desc, &prompt);
  if (apps) CFRelease(apps); if (desc) CFRelease(desc);
  NSArray *permissions = CFBridgingRelease(SecACLCopyAuthorizations(acl));
  if ([permissions isEqualToArray:@[(__bridge id)kSecACLAuthorizationAny]] && status == (simple ? errSecSuccess : errSecACLNotSimple)) return (SecACLRef)CFRetain(acl);
 }
 return NULL;
}

// SecACLRemove marks entries deleted but leaves them in an in-memory SecAccess,
// where owner introspection can no longer serialize them. Clone the raw ACLs
// with the entry omitted instead; this still never writes a Keychain item.
static SecAccessRef access_without_entry(SecAccessRef access, BOOL omitUID) {
 CSSM_ACL_OWNER_PROTOTYPE_PTR owner = NULL; CSSM_ACL_ENTRY_INFO_PTR entries = NULL;
 uint32 count = 0;
 if (SecAccessGetOwnerAndACL(access, &owner, &count, &entries) != errSecSuccess || !owner) return NULL;
 NSMutableData *kept = [NSMutableData new];
 for (uint32 i = 0; i < count; i++) {
  CSSM_LIST_ELEMENT_PTR subject = entries[i].EntryPublicInfo.TypedSubject.Head;
  BOOL uidSubject = subject && subject->WordID == CSSM_ACL_SUBJECT_TYPE_PROCESS;
  if (uidSubject != omitUID) [kept appendBytes:&entries[i] length:sizeof(entries[i])];
 }
 SecAccessRef result = NULL;
 SecAccessCreateFromOwnerAndACL(owner, (uint32)(kept.length / sizeof(*entries)), kept.bytes, &result);
 aeon_vault_free_owner_acl(owner, count, entries);
 return result;
}

static SecAccessRef access_with_extra_uid(SecAccessRef access, BOOL reverse) {
 CSSM_ACL_OWNER_PROTOTYPE_PTR owner = NULL; CSSM_ACL_ENTRY_INFO_PTR entries = NULL;
 uint32 count = 0;
 if (SecAccessGetOwnerAndACL(access, &owner, &count, &entries) != errSecSuccess || !owner) return NULL;
 NSMutableData *expanded = [NSMutableData dataWithBytes:entries length:count * sizeof(*entries)];
 CSSM_ACL_AUTHORIZATION_TAG extraPermission = CSSM_ACL_AUTHORIZATION_DECRYPT;
 BOOL found = NO;
 for (uint32 i = 0; i < count; i++) {
  CSSM_LIST_ELEMENT_PTR subject = entries[i].EntryPublicInfo.TypedSubject.Head;
  if (!subject || subject->WordID != CSSM_ACL_SUBJECT_TYPE_PROCESS) continue;
  CSSM_ACL_ENTRY_INFO extra = entries[i];
  // Give the two UID entries deterministic handles to control their order.
  ((CSSM_ACL_ENTRY_INFO_PTR)expanded.mutableBytes)[i].EntryHandle = reverse ? 2 : 1;
  extra.EntryHandle = reverse ? 1 : 2;
  extra.EntryPublicInfo.Authorization = (CSSM_AUTHORIZATIONGROUP){1, &extraPermission};
  [expanded appendBytes:&extra length:sizeof(extra)]; found = YES; break;
 }
 SecAccessRef result = NULL;
 if (found) SecAccessCreateFromOwnerAndACL(owner, count + 1, expanded.bytes, &result);
 aeon_vault_free_owner_acl(owner, count, entries);
 return result;
}

// Mutate only the non-owner Any subject. The owner and authorization sets stay
// unchanged, so the old authorization-only comparison accepts the UID swap.
static SecAccessRef access_with_subject(SecAccessRef access, int scenario) {
 CSSM_ACL_OWNER_PROTOTYPE_PTR owner = NULL; CSSM_ACL_ENTRY_INFO_PTR entries = NULL; uint32 count = 0;
 if (SecAccessGetOwnerAndACL(access, &owner, &count, &entries) != errSecSuccess || !owner) return NULL;
 NSMutableData *changed = [NSMutableData dataWithBytes:entries length:count * sizeof(*entries)];
 CSSM_ACL_ENTRY_INFO_PTR copies = changed.mutableBytes;
 CSSM_ACL_PROCESS_SUBJECT_SELECTOR selector = {CSSM_ACL_PROCESS_SELECTOR_CURRENT_VERSION, CSSM_ACL_MATCH_UID, (uint32)getuid(), 0};
 CSSM_LIST_ELEMENT datum = {0}; datum.ElementType = CSSM_LIST_ELEMENT_DATUM;
 datum.Element.Word = (CSSM_DATA){sizeof(selector), (uint8 *)&selector};
 CSSM_LIST_ELEMENT process = {0}; process.ElementType = CSSM_LIST_ELEMENT_WORDID;
 process.WordID = CSSM_ACL_SUBJECT_TYPE_PROCESS; process.NextElement = &datum;
 CSSM_LIST processList = {CSSM_LIST_TYPE_UNKNOWN, &process, &datum};
 CSSM_LIST_ELEMENT any = {0}; any.ElementType = CSSM_LIST_ELEMENT_WORDID; any.WordID = CSSM_ACL_SUBJECT_TYPE_ANY;
 CSSM_LIST_ELEMENT threshold[4] = {0};
 threshold[0].ElementType = CSSM_LIST_ELEMENT_WORDID; threshold[0].WordID = CSSM_ACL_SUBJECT_TYPE_THRESHOLD;
 threshold[1].ElementType = CSSM_LIST_ELEMENT_WORDID; threshold[1].WordID = 1;
 threshold[2].ElementType = CSSM_LIST_ELEMENT_WORDID; threshold[2].WordID = 1;
 threshold[3].ElementType = CSSM_LIST_ELEMENT_SUBLIST; threshold[3].Element.Sublist = processList;
 for (int i = 0; i < 3; i++) threshold[i].NextElement = &threshold[i + 1];
 BOOL found = NO;
 for (uint32 i = 0; i < count; i++) {
  if (entries[i].EntryPublicInfo.Authorization.NumberOfAuthTags != 1 || entries[i].EntryPublicInfo.Authorization.AuthTags[0] != CSSM_ACL_AUTHORIZATION_ANY) continue;
  CSSM_LIST_ELEMENT_PTR type = entries[i].EntryPublicInfo.TypedSubject.Head;
  if (!type || type->WordID != CSSM_ACL_SUBJECT_TYPE_PROCESS) continue;
  if (scenario == 16) copies[i].EntryPublicInfo.TypedSubject = (CSSM_LIST){CSSM_LIST_TYPE_UNKNOWN, &any, &any};
  else if (scenario == 17) copies[i].EntryPublicInfo.TypedSubject = (CSSM_LIST){CSSM_LIST_TYPE_UNKNOWN, &threshold[0], &threshold[3]};
  else {
   if (scenario != 15) selector.uid = 0;
   if (scenario == 21) selector.mask |= CSSM_ACL_MATCH_HONOR_ROOT;
   if (scenario == 22) copies[i].EntryPublicInfo.Delegate = CSSM_TRUE;
   if (scenario == 23) selector.version++;
   if (scenario == 24) { selector.mask = CSSM_ACL_MATCH_GID; selector.gid = (uint32)getgid(); }
   copies[i].EntryPublicInfo.TypedSubject = processList;
  }
  found = YES; break;
 }
 SecAccessRef result = NULL;
 if (found) SecAccessCreateFromOwnerAndACL(owner, count, copies, &result);
 aeon_vault_free_owner_acl(owner, count, entries);
 return result;
}

// Add a real native PARTITION subject with the same serialized plist format
// securityd persists. A simple NULL-app ACL with a label is not that subject.
static SecAccessRef access_with_partition(SecAccessRef access, id value, NSPropertyListFormat format) {
 CSSM_ACL_OWNER_PROTOTYPE_PTR owner = NULL; CSSM_ACL_ENTRY_INFO_PTR entries = NULL; uint32 count = 0;
 if (SecAccessGetOwnerAndACL(access, &owner, &count, &entries) != errSecSuccess || !owner) return NULL;
 NSData *payload = [value isKindOfClass:NSData.class] ? value : [NSPropertyListSerialization dataWithPropertyList:value format:format options:0 error:NULL];
 CSSM_LIST_ELEMENT datum = {0}; datum.ElementType = CSSM_LIST_ELEMENT_DATUM;
 datum.Element.Word = (CSSM_DATA){payload.length, (uint8 *)payload.bytes};
 CSSM_LIST_ELEMENT type = {0}; type.ElementType = CSSM_LIST_ELEMENT_WORDID;
 type.WordID = CSSM_ACL_SUBJECT_TYPE_PARTITION; type.NextElement = &datum;
 CSSM_ACL_AUTHORIZATION_TAG permission = CSSM_ACL_AUTHORIZATION_PARTITION_ID;
 CSSM_ACL_ENTRY_INFO extra = {0}; extra.EntryPublicInfo.TypedSubject = (CSSM_LIST){CSSM_LIST_TYPE_UNKNOWN, &type, &datum};
 extra.EntryPublicInfo.Authorization = (CSSM_AUTHORIZATIONGROUP){1, &permission};
 // Find an unused handle without changing any existing entry's order.
 extra.EntryHandle = 1;
 for (uint32 i = 0; i < count; i++) if (entries[i].EntryHandle == extra.EntryHandle) { extra.EntryHandle++; i = (uint32)-1; }
 NSMutableData *expanded = [NSMutableData dataWithBytes:entries length:count * sizeof(*entries)];
 [expanded appendBytes:&extra length:sizeof(extra)];
 SecAccessRef result = NULL;
 if (payload) SecAccessCreateFromOwnerAndACL(owner, count + 1, expanded.bytes, &result);
 aeon_vault_free_owner_acl(owner, count, entries);
 return result;
}

// Assert the attributes from the Darwin 27 affected-item probe, without reading
// any Keychain item. The UID subjects are generated by the production factory;
// only the system's simple integrity/partition entries are added by this fixture.
static BOOL affected_item_shape(SecAccessRef access) {
 uid_t owner = (uid_t)-1; gid_t group = (gid_t)-1; SecAccessOwnerType ownerType = 0;
 if (SecAccessCopyOwnerAndACL(access, &owner, &group, &ownerType, NULL) != errSecSuccess || owner != 0 || group != 0 || ownerType != kSecUseOnlyUID) return NO;
 NSArray *acls = fixture_acls(access); if (acls.count != 5) return NO;
 NSMutableArray *nonSimple = [NSMutableArray new];
 int grants = 0, integrity = 0, partition = 0;
 for (id entry in acls) {
  SecACLRef acl = (__bridge SecACLRef)entry;
  NSArray *permissions = CFBridgingRelease(SecACLCopyAuthorizations(acl));
  CFArrayRef apps = NULL; CFStringRef desc = NULL; SecKeychainPromptSelector prompt = 0;
  OSStatus status = SecACLCopyContents(acl, &apps, &desc, &prompt);
  CFIndex appCount = apps ? CFArrayGetCount(apps) : -1;
  if (apps) CFRelease(apps); if (desc) CFRelease(desc);
  if (status == errSecACLNotSimple && appCount == -1) [nonSimple addObject:permissions];
  else if (status != errSecSuccess || prompt != 0) return NO;
  else if (appCount == 1 && [permissions isEqualToArray:@[(__bridge id)kSecACLAuthorizationAny]]) grants++;
  else if (appCount == -1 && [permissions isEqualToArray:@[(__bridge id)kSecACLAuthorizationIntegrity]]) integrity++;
  else if (appCount == -1 && [permissions isEqualToArray:@[(__bridge id)kSecACLAuthorizationPartitionID]]) partition++;
  else return NO;
 }
 return grants == 1 && integrity == 1 && partition == 1 &&
  [nonSimple isEqualToArray:@[@[(__bridge id)kSecACLAuthorizationAny], @[(__bridge id)kSecACLAuthorizationChangeACL]]];
}

// Return 1 for accepted, 0 for denied, or a negative setup error. Every scenario
// constructs real SecAccess/SecACL objects; no Keychain operation or prompt runs.
int aeon_vault_access_fixture(int scenario) {
 @autoreleasepool {
  SecRequirementRef requirement = NULL; SecTrustedApplicationRef app = NULL;
  SecAccessRef reference = NULL, access = NULL; CFDataRef expected = NULL;
  int result = -1; OSStatus status = errSecSuccess; BOOL persisted = scenario == 1 || scenario >= 15;
  if (!SecTrustedApplicationCreateFromRequirement) goto done;
  if (SecRequirementCreateWithString(CFSTR("identifier \"aeon-487-fixture\""), kSecCSDefaultFlags, &requirement) != errSecSuccess) goto done;
  if (SecTrustedApplicationCreateFromRequirement(NULL, requirement, &app) != errSecSuccess) goto done;
  if (SecRequirementCopyData(requirement, kSecCSDefaultFlags, &expected) != errSecSuccess) goto done;
  reference = aeon_vault_access_for_application(app);
  access = aeon_vault_access_for_application(app);
  if (!reference || !access) goto done;
  result = -2;
  if (scenario == 1 || (scenario >= 15 && scenario != 26 && scenario != 30)) {
   NSArray *partitions = @[@"teamid:P66J39QV6V"];
   if (scenario == 18) partitions = @[@"teamid:P66J39QV6V", @"teamid:OTHERTEAM"];
   if (scenario == 19) partitions = @[@"teamid:P66J39QV6V", @"apple-tool:"];
   if (scenario == 20) partitions = @[@"teamid:P66J39QV6V", @"unsigned:"];
   if (scenario == 28) partitions = @[@"teamid:OTHERTEAM"];
   if (scenario == 31) partitions = @[@"teamid:P66J39QV6V", @"teamid:P66J39QV6V"];
   id payload = scenario == 29 ? (id)[@"malformed plist" dataUsingEncoding:NSUTF8StringEncoding] : @{@"Partitions": partitions};
   if (scenario == 32) payload = @{@"Partitions": partitions, @"extra": @YES};
   SecAccessRef expanded = access_with_partition(access, payload, scenario == 25 ? NSPropertyListBinaryFormat_v1_0 : NSPropertyListXMLFormat_v1_0);
   if (!expanded) goto done;
   CFRelease(access); access = expanded;
  }
  switch (scenario) {
   case 0: break; // Unmodified production reference.
   case 1: // Same five-entry shape as the affected persisted item.
    status = add_fixture_acl(access, nil, @[(__bridge id)kSecACLAuthorizationIntegrity], 0);
    if (status != errSecSuccess || !affected_item_shape(access)) goto done;
    break;
   case 2: // Even a second matching application grant is an unexpected entry.
    status = add_fixture_acl(access, @[(__bridge id)app], @[(__bridge id)kSecACLAuthorizationAny], 0);
    break;
   case 3: // A simple NULL-app Any entry grants every application access.
    status = add_fixture_acl(access, nil, @[(__bridge id)kSecACLAuthorizationAny], 0);
    break;
   case 4: {
    SecRequirementRef foreignRequirement = NULL; SecTrustedApplicationRef foreign = NULL;
    status = SecRequirementCreateWithString(CFSTR("identifier \"foreign-fixture\""), kSecCSDefaultFlags, &foreignRequirement);
    if (status == errSecSuccess) status = SecTrustedApplicationCreateFromRequirement(NULL, foreignRequirement, &foreign);
    SecACLRef grant = copy_fixture_acl(access, YES);
    if (status == errSecSuccess && grant) status = SecACLSetContents(grant, (__bridge CFArrayRef)@[(__bridge id)foreign], CFSTR("Aeon pairing"), 0);
    else status = errSecParam;
    if (grant) CFRelease(grant);
    if (foreign) CFRelease(foreign); if (foreignRequirement) CFRelease(foreignRequirement);
    break;
   }
   case 5: case 6: {
    CFRelease(access);
    CFErrorRef error = NULL;
    access = SecAccessCreateWithOwnerAndACL(scenario == 5 ? 501 : 0, 0, scenario == 6 ? kSecUseOnlyGID : kSecUseOnlyUID,
     (__bridge CFArrayRef)@[(__bridge id)kSecACLAuthorizationAny], &error);
    if (error) CFRelease(error);
    if (!access) goto done;
    status = add_fixture_acl(access, @[(__bridge id)app], @[(__bridge id)kSecACLAuthorizationAny], 0);
    break;
   }
   case 7: case 11: {
    SecAccessRef reduced = access_without_entry(access, scenario == 7);
    if (!reduced) goto done;
    CFRelease(access); access = reduced;
    break;
   }
   case 8: {
    SecACLRef ownerGrant = copy_fixture_acl(access, NO); if (!ownerGrant) goto done;
    status = SecACLUpdateAuthorizations(ownerGrant, (__bridge CFArrayRef)@[(__bridge id)kSecACLAuthorizationDecrypt]);
    CFRelease(ownerGrant);
    break;
   }
   case 9: { // A matching app must never introduce a prompt.
    SecACLRef grant = copy_fixture_acl(access, YES); if (!grant) goto done;
    status = SecACLSetContents(grant, (__bridge CFArrayRef)@[(__bridge id)app], CFSTR("Aeon pairing"), kSecKeychainPromptRequirePassphase);
    CFRelease(grant);
    break;
   }
   case 10: // Extra inert simple ACLs are still outside the permitted shape.
    status = add_fixture_acl(access, nil, @[], 0);
    break;
   case 12: // Duplicate metadata entry.
    status = add_fixture_acl(access, nil, @[(__bridge id)kSecACLAuthorizationIntegrity], 0);
    if (status == errSecSuccess) status = add_fixture_acl(access, nil, @[(__bridge id)kSecACLAuthorizationIntegrity], 0);
    break;
   case 13: case 14: {
    SecAccessRef expanded = access_with_extra_uid(access, scenario == 14);
    if (!expanded) goto done;
    CFRelease(access); access = expanded;
    if (scenario == 14) {
     // Same count and sets, but exchanged order. Check an unchanged control
     // first so rejection cannot be explained by the added entry itself.
     SecAccessRef ordered = access_with_extra_uid(reference, NO);
     if (!ordered) goto done;
     CFRelease(reference); reference = ordered;
     if (!aeon_vault_access_matches(reference, reference, expected)) goto done;
    }
    break;
   }
   case 15: case 16: case 17: case 21: case 22: case 23: case 24: {
    SecAccessRef changed = access_with_subject(access, scenario);
    if (!changed) goto done;
    CFRelease(access); access = changed;
    break;
   }
   case 27: {
    SecAccessRef expanded = access_with_partition(access, @{@"Partitions": @[@"teamid:P66J39QV6V"]}, NSPropertyListXMLFormat_v1_0);
    if (!expanded) goto done;
    CFRelease(access); access = expanded;
    break;
   }
   case 30: // A display label cannot stand in for a native partition payload.
    status = add_fixture_acl(access, nil, @[(__bridge id)kSecACLAuthorizationPartitionID], 0);
    break;
   case 18: case 19: case 20: case 25: case 26: case 28: case 29: case 31: case 32: break;
   default: goto done;
  }
  if (status != errSecSuccess) { result = (int)status; goto done; }
  result = (persisted ? aeon_vault_persisted_access_matches(access, reference, expected) : aeon_vault_access_matches(access, reference, expected)) ? 1 : 0;
done:
  if (access) CFRelease(access); if (reference) CFRelease(reference);
  if (expected) CFRelease(expected); if (app) CFRelease(app); if (requirement) CFRelease(requirement);
  return result;
 }
}
