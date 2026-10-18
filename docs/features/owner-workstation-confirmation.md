# Owner workstation confirmation (AEON-580)

An Owner person can designate one agent key for a paired workstation. The mark
does not grant roles or scopes. High-risk governance calls return HTTP 428 with
only `code: step_up_required`, `challenge_id` and `expires_at`. The companion
AEON-581 client forwards only the ID to agentd, which retrieves the server's
summary, nonce and action digest through its authenticated pairing channel.
The server checks the bound computer, exact paired runtime key and device proof;
an agent key alone cannot retrieve the challenge. The confirmation is single use,
expires after two minutes and binds the exact request and pinned signer.

The prompt is `Allow the owner workstation agent to <action>? <METHOD> <escaped path>`.
For routes identifying an existing member, key or role, it appends
` Target: "<stored name>"`. Names come from stored records, never the submitted
request body. Labels exclude control/format characters and long labels end in
an ellipsis; the entire summary fits 256 UTF-8 bytes.

`aeon-agentd status --state-root /absolute/pairing/root` reports
`Touch ID confirmation: ready (pairing key pinned)` or `needs pairing upgrade`
with the exact command using that pairing's saved values:
`aeon-agentd pair --url '<saved origin>' --state-root '<current root>-touch-id' --workspace '<saved workspace>'`.
Values are shell-quoted. Keep the old pairing until the new one is verified;
the existing service owner controls changing its state root. The API's additive
`local_auth_pinned` field reports the stored confirmation pin, separately from
live Touch ID availability or connectivity. An older server that omits the field
shows `unknown (server pin not reported)`.
