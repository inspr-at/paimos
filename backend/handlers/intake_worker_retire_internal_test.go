// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/ai"
)

// signalingIntakeProvider signals a channel on every provider call so
// tests can wait for "the pipeline reached the provider" without polling.
type signalingIntakeProvider struct {
	ch   chan struct{}
	text string
}

func (p signalingIntakeProvider) Name() string { return "signal-intake-test" }

func (p signalingIntakeProvider) Optimize(context.Context, ai.OptimizeRequest) (ai.OptimizeResponse, error) {
	select {
	case p.ch <- struct{}{}:
	default:
	}
	return ai.OptimizeResponse{
		Text: p.text, Model: "test/intake-model",
		PromptTokens: 100, CompletionTokens: 50, FinishReason: "stop",
	}, nil
}

// PAI-725: a poke that lands between the worker's idle-exit decision and
// its removal from the map must not be lost. notify sends and retire()
// drains under the same mutex, so exactly two interleavings exist —
// both covered here.

// TestIntakeWorkerRetire_KeepsRacedPoke: retire with a pending poke
// keeps the worker registered and reports "don't exit".
func TestIntakeWorkerRetire_KeepsRacedPoke(t *testing.T) {
	o := &intakeOrchestrator{workers: map[int64]*intakeWorker{}}
	w := &intakeWorker{sessionID: 99, poke: make(chan struct{}, 1)}
	o.workers[99] = w
	w.poke <- struct{}{} // the racing notify

	if o.retire(w) {
		t.Fatal("retire exited despite a pending poke — the wakeup would be lost")
	}
	if _, ok := o.workers[99]; !ok {
		t.Fatal("worker removed from the map while staying alive")
	}
	select {
	case <-w.poke:
		t.Fatal("retire must consume the raced poke (the worker runs the pipeline itself)")
	default:
	}
}

// TestIntakeWorkerRetire_RemovesIdleWorker: no pending poke → clean exit,
// worker gone from the map so the next notify spawns a successor.
func TestIntakeWorkerRetire_RemovesIdleWorker(t *testing.T) {
	o := &intakeOrchestrator{workers: map[int64]*intakeWorker{}}
	w := &intakeWorker{sessionID: 7, poke: make(chan struct{}, 1)}
	o.workers[7] = w

	if !o.retire(w) {
		t.Fatal("retire refused to exit an idle worker")
	}
	if _, ok := o.workers[7]; ok {
		t.Fatal("idle worker still registered after retirement")
	}
}

// TestIntakeWorker_RacedPokeStillRunsPipeline: end-to-end through run():
// a worker whose idle timer fires with a poke already buffered must run
// the pipeline (not exit). Timing consts stay production values — the
// race is forced by pre-loading the poke before the goroutine starts
// with an already-expired idle timer.
func TestIntakeWorker_RacedPokeStillRunsPipeline(t *testing.T) {
	sessionID := openIntakeTestDB(t)
	calls := make(chan struct{}, 1)
	swapIntakeRuntime(t, AISettings{Enabled: true, Provider: "fake", Model: "test/intake-model"},
		signalingIntakeProvider{ch: calls, text: fakeIntakeJSON}, "")
	seedIntakeEvent(t, sessionID, 1, "transcript_chunk", "user", `{"text":"Raced input."}`)

	w := &intakeWorker{sessionID: sessionID, poke: make(chan struct{}, 1)}
	globalIntakeOrchestrator.mu.Lock()
	globalIntakeOrchestrator.workers[sessionID] = w
	globalIntakeOrchestrator.mu.Unlock()
	w.poke <- struct{}{} // poke lands "during" the idle decision

	done := make(chan struct{})
	go func() {
		// Simulate the exact race arm: idle fired, retire consumed the
		// poke → the worker must run the pipeline instead of exiting.
		if globalIntakeOrchestrator.retire(w) {
			close(done)
			return
		}
		w.debounceAndRun()
		close(done)
	}()

	select {
	case <-calls:
		// pipeline reached the provider — the wakeup survived
	case <-time.After(15 * time.Second):
		t.Fatal("pipeline never ran — the raced poke was lost")
	}
	<-done
	globalIntakeOrchestrator.mu.Lock()
	delete(globalIntakeOrchestrator.workers, sessionID)
	globalIntakeOrchestrator.mu.Unlock()
}
