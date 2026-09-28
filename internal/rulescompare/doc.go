// SPDX-License-Identifier: AGPL-3.0-only

// Package rulescompare reports one offline comparison of explicit instruction
// files, an AR1 merged-rules document and AEON-219 receipt hashes.
//
// Files the operator names are expected inputs. Their raw SHA-256 identifies
// those bytes. A provenance content hash proves the same bytes were recorded
// as received only when it equals that raw digest. A shared logical name does
// not. A normalized line hash does not. Supply and receipt are both silent
// about whether a model loaded or obeyed the text.
//
// Compare does not wait, publish, read directories, or replace active
// instruction files. A missing published layer or locked floor blocks rollout
// and still leaves the supplied hashes and the next invocation in the report.
package rulescompare
