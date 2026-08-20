// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public
// License along with this program. If not, see <https://www.gnu.org/licenses/>.

package deliverytrust

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"
	"math"
	"sort"
	"time"
)

type revisionWriter struct{ hash.Hash }

func newRevisionWriter() revisionWriter { return revisionWriter{Hash: sha256.New()} }

func (w revisionWriter) text(value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = w.Write(length[:])
	_, _ = w.Write([]byte(value))
}

func (w revisionWriter) boolean(value bool) {
	if value {
		w.text("1")
	} else {
		w.text("0")
	}
}

func (w revisionWriter) uint64(value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	_, _ = w.Write(encoded[:])
}

func (w revisionWriter) int64(value int64) { w.uint64(uint64(value)) }

func (w revisionWriter) integer(value int) { w.int64(int64(value)) }

func (w revisionWriter) instant(value time.Time) {
	if value.IsZero() {
		w.text("")
		return
	}
	w.text(value.UTC().Format(time.RFC3339Nano))
}

func (w revisionWriter) float(value float64) {
	switch {
	case math.IsNaN(value):
		w.text("nan")
	case math.IsInf(value, 1):
		w.text("+inf")
	case math.IsInf(value, -1):
		w.text("-inf")
	default:
		if value == 0 { // canonicalize negative zero
			value = 0
		}
		w.uint64(math.Float64bits(value))
	}
}

func trustRevision(
	input Input,
	estimates []estimateAnalysis,
	histories []HistoryResult,
) string {
	w := newRevisionWriter()
	w.text("paimos-delivery-trust-v1")
	w.integer(SchemaVersion)
	w.integer(input.PolicyVersion)
	w.text(input.DeliveryIdentity)
	w.text(input.ProjectIdentity)
	w.boolean(input.Instrumented)

	for _, item := range input.Policy {
		w.text("policy")
		w.text(string(item.Stage))
		w.boolean(item.Required)
		w.integer(item.Weight)
		w.text(item.Identity)
	}
	for i, stage := range input.Stages {
		w.text("stage")
		w.text(string(stage.Stage))
		writeScope(w, stage.Scope)
		w.text(string(stage.Reporter))
		if stage.ExecutionStartedAt == nil {
			w.boolean(false)
		} else {
			w.boolean(true)
			w.instant(*stage.ExecutionStartedAt)
		}
		writeCompletion(w, stage.Completion)
		w.text(stage.Signals.SemanticIdentity)
		w.boolean(stage.Signals.WaitingOnHuman)
		w.boolean(stage.Signals.Blocked)

		if i < len(estimates) {
			writeEstimate(w, "latest", estimates[i].latest)
			writeEstimate(w, "latest_progress", estimates[i].latestProgressFact)
			writeEstimate(w, "max_progress", estimates[i].maxProgressFact)
		}
		if i < len(histories) {
			for _, sample := range histories[i].immutableSamples {
				writeDurationSample(w, sample)
			}
		}
	}
	return "tr1_" + hex.EncodeToString(w.Sum(nil))
}

func writeScope(w revisionWriter, scope Scope) {
	w.text(scope.AttemptID)
	w.text(scope.PlanID)
	w.text(scope.ExecutionID)
	w.text(scope.AuthorityID)
	w.text(scope.ResetID)
	w.text(scope.ReporterID)
	w.text(scope.RunLinkID)
}

func writeCompletion(w revisionWriter, completion CompletionInput) {
	w.text(string(completion.Status))
	w.boolean(completion.Eligible)
	w.text(completion.SemanticIdentity)
	evidence := append([]string(nil), completion.EvidenceIdentities...)
	sort.Strings(evidence)
	w.integer(len(evidence))
	for _, identity := range evidence {
		w.text(identity)
	}
}

func writeEstimate(w revisionWriter, label string, fact *EstimateFact) {
	w.text(label)
	if fact == nil {
		w.boolean(false)
		return
	}
	w.boolean(true)
	w.text(fact.Identity)
	w.text(string(fact.Reporter))
	writeScope(w, fact.Scope)
	w.uint64(fact.Revision)
	w.uint64(fact.Sequence)
	w.text(string(fact.Source))
	w.instant(fact.ServerReceivedAt)
	w.float(fact.Confidence)
	w.text(fact.Basis)
	if fact.ProgressPercent == nil {
		w.boolean(false)
	} else {
		w.boolean(true)
		w.float(*fact.ProgressPercent)
	}
	if fact.ETA == nil {
		w.boolean(false)
		return
	}
	w.boolean(true)
	w.int64(fact.ETA.MinimumSeconds)
	w.int64(fact.ETA.MaximumSeconds)
	if fact.ETA.PointSeconds == nil {
		w.boolean(false)
	} else {
		w.boolean(true)
		w.int64(*fact.ETA.PointSeconds)
	}
}

func writeDurationSample(w revisionWriter, sample DurationSample) {
	w.text("history")
	w.text(sample.Identity)
	w.uint64(sample.StageExecutionID)
	w.text(sample.ProjectIdentity)
	w.text(string(sample.Stage))
	w.integer(sample.PolicyVersion)
	w.instant(sample.CompletedAt)
	w.int64(sample.FullLeadSeconds)
	w.int64(sample.ActiveSeconds)
	w.int64(sample.BlockedSeconds)
	w.int64(sample.HumanWaitSeconds)
}
