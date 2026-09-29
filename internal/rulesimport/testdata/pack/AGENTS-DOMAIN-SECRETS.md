# Synthetic secrets pack

## Credentials

- Keep credentials out of the always-on file.
  Why: the pack is loaded on demand.
  Details: PACK-BODY-START synthetic credential handling stays in details. Read the secret from the tenant store, never from the rule line, and do not copy the value into a session file. PACK-BODY-END
