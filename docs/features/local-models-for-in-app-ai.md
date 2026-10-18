# Local models for in-app AI

Workspace AI is off by default. A person with `settings.manage` can open
**Settings → Workspace → In-app AI**, enter an OpenAI-compatible API base URL
and chat model, and select the features allowed to send content to that server.
For [Ollama on the Aeon server](https://docs.ollama.com/api/openai-compatibility),
use `http://localhost:11434/v1` and the name of
an installed chat model. Save, then use **Test connection**: it sends only a
small synthetic prompt, including while AI is off. Saving never contacts a model.
The endpoint is resolved from the Aeon server; in Docker, `localhost` means the
Aeon container. Use an address reachable from that container for a separate
model service. Public, loopback and LAN endpoints are supported; redirects,
link-local/metadata addresses, CGNAT, NAT64 and other special-use ranges are
refused.

The first connected feature is **Rewrite customer notes with AI**. Also enable
the `business_crm` plugin with its `tools.invoke` permission. A person with
`crm.manage` can request a rewrite; the selected server receives the customer
name and notes. The result is a revision-bound draft: a person reviews and
applies it separately. A changed customer or provider configuration refuses the
stale generation. Delegated agent runs and Aithema intake retain their existing
harness, plugin and approval controls; this setting does not select their models.

**Generate parent benefits from leaves** is a separate opt-in (AEON-654), behind
`work-parent-status` (AEON-429). Completing a parent queues an asynchronous English
and German pill/benefit summary; its Done never waits for the model. The parent
shows pending/running, generated provenance, or a safe failure with **Retry
generation**. **Edit benefit** preserves human text and cancels stale attempts.
The worker uses the saved chat model; it never launches a harness or falls back to
a vendor. The person who saved the provider must still have `settings.manage`,
`nodes.read` and `nodes.write` for the current parent when a result is applied.
It summarises up to 200 leaves / 64 KB of benefit data, excludes cancelled/archived
leaves, refuses incomplete or cross-project sources, and never silently truncates.
Provider changes, moved/reopened parents, changed leaves and edited benefits fence
in-flight results. Leases recover after restart. Leaves keep the bilingual Done
gate; published snapshots remain immutable. Migration `1239` stores only job and
provenance metadata on existing nodes. `GET /api/nodes/{id}/benefit-generation`
reports status; the person-only retry POST requires the displayed generation and
node revision. Retry bodies have a 4 KiB limit and a five-second HTTP read deadline
before decoding and mutation locks. A generated write emits
`node.benefits_generated` for live refresh; it has no generic node Undo, which
could overwrite unrelated fields.
Leaf completion checks use the same tenant state categories as parent derivation,
including custom Done states. Cancellation and archival do not require benefits,
and edits to already-completed historical records remain available.

API keys are optional and encrypted through the existing tenant-bound AES-GCM
vault, separate from JSON settings and event data. Reads reveal only whether a
key is set. A blank replacement clears the key; omitting it preserves it.
Changing the base URL clears a retained key unless a replacement is supplied.
Keep the existing `AEON_SESSION_KEY_FILE` stable across restarts; development
hosts that store a provider key also need this persistent host secret.

Semantic search and background indexing can use the same provider and key with
a separate embedding model. Aeon's existing vector storage requires **1536
dimensions**. Enabling embeddings or changing their endpoint/model queues a
fresh index; vector identities include both, so search never mixes vector spaces.
Without a configured, enabled workspace provider and feature, CRM generation
makes no model request, indexing leaves its queue untouched, and search stays
lexical. The server now uses these workspace settings for embeddings; migrate
older `AEON_EMBEDDING_*` server configuration here.
