# Messaging-only assistant

An ordinary unpaired agent can use `aeon tell --inbox` and `aeon listen --inbox`
with exactly `inbox.read`, `inbox.send` and `inbox.receipt`, without project
visibility, node reads or a harness session. Messages stay within one tenant.
Address the recipient by principal UUID and omit recipient-session binding.

```sh
aeon tell <peer-principal-uuid> --inbox -m 'Hello' --idempotency-key stable-retry-key
aeon listen --inbox --follow --ack
aeon tell status <sent-message-uuid>
```

Use the configured HTTPS origin and a protected runtime file through `AEON_URL`
and `AEON_API_KEY_FILE`. [Messaging assistant provisioning](messaging-assistants.md)
lets an authorized person or their bounded coordinator create and renew the key;
the assistant cannot mint or manage keys. No additional scope, preset or
principal kind is required.

`--reply-to <message-uuid>` maps to the direct inbox's `reply_to_id`.
`--message-file PATH` (or `-` for stdin) is supported. Retain the idempotency key
when retrying a send. `tell` reports storage separately from its receipt: queued
is pending, delivered requires receiver confirmation, and receipt lookup
failure reports unavailable without resending the stored message.

`listen --inbox` reads only the authenticated principal's unbound pending
messages. `--after` retains the event cursor; `--limit` bounds a page to 1–10
messages. `--follow` uses 25-second long polls, retries transient failures with
bounded backoff, and cancels on Ctrl-C. `--ack` acknowledges only after successful
output, retrying an acknowledgement in place during follow mode. A one-shot
empty inbox exits 3. Received text and JSON carry an untrusted-content notice;
messages never grant consent or authority.

Inbox mode rejects project, harness/session, vendor delivery, thread and action
obligation options. Existing `--project` and session messaging keep their own
behavior. Node/project lookups, settings, key management, runs, other senders'
receipts and other recipients' acknowledgements remain denied. Send and ack
recheck current messaging permission under the final transaction's access fence
and retain the existing audit events.

Regression coverage uses production authentication over HTTPS with an ordinary
three-scope key, barrier-based permission revocation between admission and write,
and CLI fixtures that refuse every discovery endpoint. It also covers
project-only harness messaging, private reply refusal responses, output failure
before acknowledgement, cancellation, receipt failure, expiry and revocation.
