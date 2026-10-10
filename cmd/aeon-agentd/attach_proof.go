// SPDX-License-Identifier: AGPL-3.0-only
package main

import "context"

// Store and Keychain reads can block in non-cancellable OS calls. Allow callers
// to abandon a read, but retain its slot until it finishes: even a permanently
// stalled read can leave only one worker behind per daemon. Never reuse an
// abandoned result as authority for another registration.
type attachProofReader struct {
	load func() (string, string, error)
	slot chan struct{}
}

func newAttachProofReader(load func() (string, string, error)) *attachProofReader {
	return &attachProofReader{load: load, slot: make(chan struct{}, 1)}
}

func (r *attachProofReader) read(ctx context.Context) (string, string, error) {
	select {
	case r.slot <- struct{}{}:
	case <-ctx.Done():
		return "", "", ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		<-r.slot
		return "", "", err
	}
	type result struct {
		host, proof string
		err         error
	}
	results := make(chan result)
	go func() {
		defer func() { <-r.slot }()
		host, proof, err := r.load()
		select {
		case results <- result{host, proof, err}:
		case <-ctx.Done():
		}
	}()
	select {
	case <-ctx.Done():
		return "", "", ctx.Err()
	case got := <-results:
		if err := ctx.Err(); err != nil {
			return "", "", err
		}
		return got.host, got.proof, got.err
	}
}
