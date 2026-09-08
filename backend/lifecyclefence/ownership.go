// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
package lifecyclefence

// RuntimeSessionOwnershipSQL is the shared durable-message fence that decides
// whether a live runtime advertisement owns a managed harness session. v1/v2
// keep the singular registration class. v3 requires an exact account-scope
// membership of class, named key (or class-only empty key), and profile
// id@version. Aliases must be simple SQL identifiers; anything else fails closed.
func RuntimeSessionOwnershipSQL(runtimeAlias, sessionAlias string) string {
	runtime, session := sqlIdent(runtimeAlias), sqlIdent(sessionAlias)
	if runtime == "" || session == "" {
		return "(0)"
	}
	return `(CASE
 WHEN CAST(json_extract(` + runtime + `.registration_json,'$.schema_version') AS INTEGER)=3 THEN CASE WHEN EXISTS(
  SELECT 1 FROM json_each(` + runtime + `.registration_json,'$.account_scopes') AS scope
  WHERE json_extract(scope.value,'$.account_label')=` + session + `.account_label
   AND (
    (COALESCE(json_array_length(json_extract(scope.value,'$.accounts')),0)=0 AND COALESCE(` + session + `.account_key,'')='')
    OR EXISTS(
     SELECT 1 FROM json_each(scope.value,'$.accounts') AS account
     WHERE json_extract(account.value,'$.key')=` + session + `.account_key
      AND COALESCE(` + session + `.account_key,'')<>''
    )
   )
   AND EXISTS(
    SELECT 1 FROM json_each(scope.value,'$.profiles') AS profile
    WHERE json_extract(profile.value,'$.id')=` + session + `.dispatch_profile_id
     AND json_extract(profile.value,'$.version')=` + session + `.dispatch_profile_version
   )
 ) THEN 1 ELSE 0 END
 WHEN json_extract(` + runtime + `.registration_json,'$.account_label')=` + session + `.account_label THEN 1
 ELSE 0 END)`
}

func sqlIdent(name string) string {
	if name == "" {
		return ""
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c == '_' {
			continue
		}
		if i > 0 && c >= '0' && c <= '9' {
			continue
		}
		return ""
	}
	return name
}

// Production alias pairs. Callers bind user values with placeholders; these
// identifiers are compile-time table aliases, never request input.
var (
	OwnershipSQLRuntimeS       = RuntimeSessionOwnershipSQL("runtime", "s")
	OwnershipSQLRuntimeSession = RuntimeSessionOwnershipSQL("runtime", "session")
	OwnershipSQLRuntimeHarness = RuntimeSessionOwnershipSQL("runtime", "harness")
)
