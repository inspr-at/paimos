// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package supervision

import (
	"bytes"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDigestLengthEncodingRejectsOverflowBoundaries(t *testing.T) {
	type testCase struct {
		name   string
		length int
		want   []byte
		ok     bool
	}
	tests := []testCase{
		{name: "zero", length: 0, want: []byte{0, 0, 0, 0}, ok: true},
		{name: "negative", length: -1},
	}
	if strconv.IntSize == 64 {
		maximum, overflow := int64(1<<32)-1, int64(1<<32)
		tests = append(tests,
			testCase{name: "maximum", length: int(maximum), want: []byte{0xff, 0xff, 0xff, 0xff}, ok: true},
			testCase{name: "overflow", length: int(overflow)},
		)
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := digestLength(tc.length)
			if ok != tc.ok || (ok && !bytes.Equal(got[:], tc.want)) {
				t.Fatalf("digestLength(%d)=%x,%v want %x,%v", tc.length, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestFrozenDurationsAndActionPolicy(t *testing.T) {
	if GrantTTL != 5*time.Minute || LeaseTTL != 90*time.Second || LeaseRenewAfter != 30*time.Second ||
		InputTTL != 60*time.Second || ChallengeTTL != 120*time.Second || PullPageSize != 100 || PullProbePageSize != 101 {
		t.Fatal("frozen supervision duration or page-size drift")
	}
	all := []Action{"run.cancel.running", "input.respond", "run.pause", "run.resume"}
	for _, status := range []string{"active", "frozen"} {
		got, err := runnerActions(status, all)
		if err != nil || len(got) != len(all) {
			t.Fatalf("%s runner policy got=%v err=%v", status, got, err)
		}
	}
	archived, err := runnerActions("archived", all)
	if err != nil || len(archived) != 1 || archived[0] != "run.cancel.running" {
		t.Fatalf("archived runner policy got=%v err=%v", archived, err)
	}
	if _, err := runnerActions("deleted", all); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted runner policy error=%v", err)
	}
	if _, err := runnerActions("active", []Action{"run.pause"}); !IsCode(err, CodeInvalidAction) {
		t.Fatalf("unpaired pause error=%v code=%s", err, ErrorCode(err))
	}
}

func TestExpiryBoundaryIsExact(t *testing.T) {
	boundary := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	if expiredAt(boundary.Add(-time.Millisecond), boundary) {
		t.Fatal("T-1ms is not expired")
	}
	if !expiredAt(boundary, boundary) {
		t.Fatal("T equality must be expired")
	}
	if !expiredAt(boundary.Add(time.Millisecond), boundary) {
		t.Fatal("T+1ms must be expired")
	}
}

func TestCanonicalDigestGoldenAndFraming(t *testing.T) {
	digest := canonicalDigest("command.create", stringField("action", "input.respond"),
		stringField("choice", "choice_2"), intField("revision", 7))
	const golden = "030e869d1a3b914e7adca709b08e429762fb13d84a12d088b2c180defcbd186d"
	if got := hex.EncodeToString(digest[:]); got != golden {
		t.Fatalf("canonical digest drift: got %s want %s", got, golden)
	}
	left := canonicalDigest("framing", stringField("a", "bc"))
	right := canonicalDigest("framing", stringField("ab", "c"))
	if left == right {
		t.Fatal("length framing admitted a boundary collision")
	}
	unicodeValue := canonicalDigest("unicode", stringField("value", "Grüße-東京"))
	if unicodeValue == ([32]byte{}) {
		t.Fatal("Unicode digest is zero")
	}
}

func TestOperationKeyDigestAcceptsOnlyNonZeroDigest(t *testing.T) {
	if _, err := operationKeyDigest([32]byte{}); !IsCode(err, CodeInvalidOperationKey) {
		t.Fatalf("zero digest error=%v code=%s", err, ErrorCode(err))
	}
	var digest [32]byte
	digest[31] = 1
	if got, err := operationKeyDigest(digest); err != nil || got != digest {
		t.Fatalf("valid digest got=%x err=%v", got, err)
	}
}

func TestSecretSentinelsAreRejectedFromRunnerSafeStrings(t *testing.T) {
	for _, sentinel := range []string{"sk_abcdefghijklmnopqrst", "sk-ant-abcdefghijklmnopqrst",
		"github_pat_12345678901234567890", "AKIA1234567890ABCDEF"} {
		if err := validateDevice(sentinel); !IsCode(err, CodeInvalidDevice) {
			t.Fatalf("secret-like device %q error=%v code=%s", sentinel, err, ErrorCode(err))
		}
		if err := validateDevice("runner\n" + sentinel); err == nil || strings.Contains(err.Error(), sentinel) {
			t.Fatalf("unsafe error for %q: %v", sentinel, err)
		}
	}
}

func FuzzCanonicalDigestFraming(f *testing.F) {
	f.Add("command", "field", "value")
	f.Add("input.respond", "emoji", "✅")
	f.Fuzz(func(t *testing.T, operation, name, value string) {
		first := canonicalDigest(operation, stringField(name, value))
		second := canonicalDigest(operation, stringField(name, value))
		if first != second {
			t.Fatal("canonical digest is nondeterministic")
		}
	})
}
