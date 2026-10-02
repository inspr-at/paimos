// SPDX-License-Identifier: AGPL-3.0-only

package ciproof

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

type Record struct {
	Schema         string   `json:"schema"`
	Sequence       int64    `json:"sequence"`
	PreviousDigest string   `json:"previous_digest"`
	Digest         string   `json:"digest"`
	Plan           *Plan    `json:"plan,omitempty"`
	Receipt        *Receipt `json:"receipt,omitempty"`
}

func recordDigest(r Record) string { r.Digest = ""; return digest("shadow-record", r) }

// WithLedger serializes readers/writers of a controller-owned local ledger.
// O_NOFOLLOW rejects a candidate-controlled final symlink. The directory must
// also be controller-owned. A hash chain detects accidental corruption, not an
// attacker rewriting the whole store. Authenticated durable storage is E's scope.
func WithLedger(ctx context.Context, r *Repository, path string, plan Plan, receipt *Receipt) (Record, error) {
	if err := VerifyPlan(ctx, r, plan); err != nil {
		return Record{}, err
	}
	if receipt != nil {
		if err := ValidateReceipt(plan, *receipt); err != nil {
			return Record{}, err
		}
	}
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_APPEND|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0600)
	if err != nil {
		return Record{}, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return Record{}, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return Record{}, fmt.Errorf("ledger must be a private regular file")
	}
	if err := unix.Flock(fd, unix.LOCK_EX); err != nil {
		return Record{}, err
	}
	defer unix.Flock(fd, unix.LOCK_UN)
	last, plans, receipts, err := readLedger(f)
	if err != nil {
		return Record{}, err
	}
	next := Record{Schema: LedgerSchema, Sequence: last.Sequence + 1, PreviousDigest: last.Digest}
	if receipt == nil {
		if plans[plan.ID].ID != "" {
			return Record{}, fmt.Errorf("duplicate plan record")
		}
		next.Plan = &plan
	} else {
		stored, ok := plans[plan.ID]
		if !ok || stored.ID != plan.ID {
			return Record{}, fmt.Errorf("receipt has no ledger plan")
		}
		if receipts[receiptKey(*receipt)] {
			return Record{}, fmt.Errorf("duplicate receipt record")
		}
		next.Receipt = receipt
	}
	next.Digest = recordDigest(next)
	b, err := json.Marshal(next)
	if err != nil {
		return Record{}, err
	}
	b = append(b, '\n')
	if _, err := f.Write(b); err != nil {
		return Record{}, err
	}
	if err := f.Sync(); err != nil {
		return Record{}, err
	}
	return next, nil
}

func receiptKey(r Receipt) string {
	return digest("receipt-identity", struct {
		Plan, Unit   string
		Run, Attempt int64
		Job          string
	}{r.PlanID, r.ObligationID, r.RunID, r.Attempt, r.JobID})
}

func readLedger(f *os.File) (Record, map[string]Plan, map[string]bool, error) {
	plans := map[string]Plan{}
	receipts := map[string]bool{}
	last := Record{}
	if _, err := f.Seek(0, 0); err != nil {
		return last, nil, nil, err
	}
	reader := bufio.NewReader(f)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) == 0 && err == io.EOF {
			break
		}
		if err != nil || len(line) > 16<<20 {
			return last, nil, nil, fmt.Errorf("truncated or oversized ledger record")
		}
		var r Record
		d := json.NewDecoder(bytes.NewReader(line))
		d.DisallowUnknownFields()
		check := json.NewDecoder(bytes.NewReader(line))
		if err := uniqueJSON(check); err != nil {
			return last, nil, nil, err
		}
		if _, err := check.Token(); err != io.EOF {
			return last, nil, nil, fmt.Errorf("trailing ledger data")
		}
		if err := d.Decode(&r); err != nil {
			return last, nil, nil, err
		}
		if r.Schema != LedgerSchema || r.Sequence != last.Sequence+1 || r.PreviousDigest != last.Digest || r.Digest != recordDigest(r) || (r.Plan == nil) == (r.Receipt == nil) {
			return last, nil, nil, fmt.Errorf("corrupt shadow ledger chain")
		}
		if r.Plan != nil {
			if err := ValidatePlan(*r.Plan); err != nil {
				return last, nil, nil, err
			}
			if _, ok := plans[r.Plan.ID]; ok {
				return last, nil, nil, fmt.Errorf("duplicate ledger plan")
			}
			plans[r.Plan.ID] = *r.Plan
		} else {
			p, ok := plans[r.Receipt.PlanID]
			if !ok {
				return last, nil, nil, fmt.Errorf("orphan ledger receipt")
			}
			if err := ValidateReceipt(p, *r.Receipt); err != nil {
				return last, nil, nil, err
			}
			key := receiptKey(*r.Receipt)
			if receipts[key] {
				return last, nil, nil, fmt.Errorf("duplicate ledger receipt")
			}
			receipts[key] = true
		}
		last = r
	}
	return last, plans, receipts, nil
}
