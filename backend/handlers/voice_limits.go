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

// PAI-724. The intake voice endpoints (STT + TTS) call paid provider
// APIs — ElevenLabs bills STT per audio-hour and TTS per character.
// Before this gate any member could loop 12 MiB audio posts and bill
// the account invisibly (the paper trail recorded zero tokens and zero
// cost). voiceAdmit runs four gates before any provider call:
//
//  1. PAI-161 daily AI cap — the same CheckUsageCap gate every other
//     AI surface runs (admins bypass with the X-AI-Over-Cap header).
//  2. Per-user concurrency — a browser posts one utterance at a time;
//     more than voiceMaxInflightPerUser parallel calls is not a person.
//  3. Per-minute burst — sliding window per (user, endpoint), sized
//     well above honest continuous-listening rates.
//  4. Daily unit budget — audio-seconds for STT, characters for TTS,
//     summed from today's ai_calls rows so restarts don't reset it.
//     Soft cap: admins pass (PAI-161 doctrine), env-overridable.
//
// Metering: successful calls record provider-specific units in the
// prompt_tokens column (STT: estimated audio seconds, TTS: characters)
// plus an estimated cost_micro_usd, so spend is visible on the AI
// papertrail and the budgets above have durable state to sum.

package handlers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/models"
)

const (
	voiceActionIntakeSTT    = "intake_stt"
	voiceActionIntakeTTS    = "intake_tts"
	voiceActionAgentModeSTT = "agent_mode_stt"
	voiceActionAgentModeTTS = "agent_mode_tts"
	voiceClassSTT           = "stt"
	voiceClassTTS           = "tts"

	// A browser records one utterance at a time; 2 tolerates an upload
	// still in flight when the next utterance closes.
	voiceMaxInflightPerUser = 2

	// Sliding one-minute burst caps per user. Continuous listening
	// posts an utterance every few seconds at worst (~12/min); TTS
	// fires once per summary update or level switch.
	voiceLimitWindow  = time.Minute
	voiceSTTPerMinute = 20
	voiceTTSPerMinute = 10

	// Daily per-user unit budgets (soft caps — admins pass). Two
	// audio-hours of dictation and ~30 full-length spoken summaries
	// comfortably cover honest daily use.
	voiceSTTDailySecondsDefault = 7200
	voiceTTSDailyCharsDefault   = 60_000
	envVoiceSTTDailySeconds     = "PAIMOS_VOICE_STT_DAILY_SECONDS"
	envVoiceTTSDailyChars       = "PAIMOS_VOICE_TTS_DAILY_CHARS"

	// Cost estimates, not invoices: ElevenLabs Scribe ≈ $0.40 per
	// audio-hour (111 µUSD/s), multilingual TTS ≈ $0.10 per 1k
	// characters (100 µUSD/char). Plans vary; the point is a non-zero,
	// right-order-of-magnitude number on the papertrail.
	voiceSTTMicroUSDPerSecond = 111
	voiceTTSMicroUSDPerChar   = 100

	// MediaRecorder opus runs ≈ 32 kbit/s, so 4000 bytes ≈ 1 s. Low-
	// bitrate uploads overestimate duration, which errs on the safe
	// side for a budget.
	voiceSTTBytesPerSecond = 4000
)

// voiceEstimateSeconds converts an uploaded blob size into the audio
// seconds the provider will roughly bill for.
func voiceEstimateSeconds(byteLen int) int64 {
	return max(int64((byteLen+voiceSTTBytesPerSecond-1)/voiceSTTBytesPerSecond), 1)
}

var voiceInflight = struct {
	mu     sync.Mutex
	byUser map[int64]int
}{byUser: map[int64]int{}}

// voiceBudgetReservations closes the gap between the durable daily-total
// lookup and the ai_calls insert performed after a provider attempt. Without
// an in-memory reservation, two concurrent Intake/Agent Mode calls can both
// observe the same durable total and jointly overspend the shared modality
// budget. The reservation remains held until metadata accounting completes.
var voiceBudgetReservations = struct {
	mu    sync.Mutex
	units map[string]int64
}{units: map[string]int64{}}

func voiceAcquireInflight(userID int64) bool {
	voiceInflight.mu.Lock()
	defer voiceInflight.mu.Unlock()
	if voiceInflight.byUser[userID] >= voiceMaxInflightPerUser {
		return false
	}
	voiceInflight.byUser[userID]++
	return true
}

func voiceReleaseInflight(userID int64) {
	voiceInflight.mu.Lock()
	defer voiceInflight.mu.Unlock()
	if n := voiceInflight.byUser[userID]; n <= 1 {
		delete(voiceInflight.byUser, userID)
	} else {
		voiceInflight.byUser[userID] = n - 1
	}
}

// voiceRateStore is the sliding-window burst counter, same shape as the
// propose limiter (memory_propose.go). Keys are "<action_key>:<userID>".
type voiceRateStore struct {
	mu      sync.Mutex
	entries map[string][]time.Time
}

var voiceRates = &voiceRateStore{entries: map[string][]time.Time{}}

// checkAndRecord admits and records the attempt when under the limit
// (returns 0), or returns how long until the oldest attempt leaves the
// window. `now` is parameterised so tests can pin time.
func (s *voiceRateStore) checkAndRecord(key string, now time.Time, limit int) time.Duration {
	cutoff := now.Add(-voiceLimitWindow)
	s.mu.Lock()
	defer s.mu.Unlock()
	stamps := s.entries[key]
	live := stamps[:0]
	for _, t := range stamps {
		if t.After(cutoff) {
			live = append(live, t)
		}
	}
	if len(live) >= limit {
		s.entries[key] = live
		return live[0].Sub(cutoff)
	}
	live = append(live, now)
	s.entries[key] = live
	return 0
}

// voiceDailyBudget resolves the per-user daily unit budget for one
// endpoint. Non-positive env values fall back to the default rather
// than disabling the budget (same foot-gun guard as the propose
// limiter).
func voiceDailyBudget(actionKey string) int64 {
	if voiceClassForAction(actionKey) == voiceClassTTS {
		return envInt64Or(envVoiceTTSDailyChars, voiceTTSDailyCharsDefault)
	}
	return envInt64Or(envVoiceSTTDailySeconds, voiceSTTDailySecondsDefault)
}

func voiceClassForAction(actionKey string) string {
	switch actionKey {
	case voiceActionIntakeSTT, voiceActionAgentModeSTT:
		return voiceClassSTT
	case voiceActionIntakeTTS, voiceActionAgentModeTTS:
		return voiceClassTTS
	default:
		return ""
	}
}

func voiceActionsForClass(class string) (string, string) {
	if class == voiceClassTTS {
		return voiceActionIntakeTTS, voiceActionAgentModeTTS
	}
	return voiceActionIntakeSTT, voiceActionAgentModeSTT
}

func envInt64Or(name string, def int64) int64 {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v <= 0 {
		return def
	}
	return v
}

// voiceUnitsUsedToday sums today's (UTC) billed units for one user +
// endpoint from the papertrail — durable across restarts. No outcome
// filter: failed calls record zero units and don't move the sum.
func voiceUnitsUsedToday(ctx context.Context, userID int64, actionKey string) (int64, error) {
	class := voiceClassForAction(actionKey)
	firstAction, secondAction := voiceActionsForClass(class)
	var n sql.NullInt64
	err := db.DB.QueryRowContext(ctx,
		`SELECT SUM(prompt_tokens) FROM ai_calls
		  WHERE user_id = ? AND action_key IN (?,?)
		    AND created_at >= date('now')`, userID, firstAction, secondAction).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return n.Int64, err
}

func timeToUTCMidnight() time.Duration {
	now := time.Now().UTC()
	next := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).Add(24 * time.Hour)
	return next.Sub(now)
}

func voiceReserveDailyBudget(ctx context.Context, userID int64, actionKey string, requestUnits int64) (int64, func(), error) {
	class := voiceClassForAction(actionKey)
	key := class + ":" + strconv.FormatInt(userID, 10)
	voiceBudgetReservations.mu.Lock()
	defer voiceBudgetReservations.mu.Unlock()

	used, err := voiceUnitsUsedToday(ctx, userID, actionKey)
	if err != nil {
		return 0, func() {}, err
	}
	pending := voiceBudgetReservations.units[key]
	budget := voiceDailyBudget(actionKey)
	if used+pending+requestUnits > budget {
		return used + pending, nil, nil
	}
	voiceBudgetReservations.units[key] = pending + requestUnits
	var once sync.Once
	release := func() {
		once.Do(func() {
			voiceBudgetReservations.mu.Lock()
			defer voiceBudgetReservations.mu.Unlock()
			if remaining := voiceBudgetReservations.units[key] - requestUnits; remaining <= 0 {
				delete(voiceBudgetReservations.units, key)
			} else {
				voiceBudgetReservations.units[key] = remaining
			}
		})
	}
	return used + pending, release, nil
}

// voiceAdmit runs the PAI-724 gates for one paid voice call. When
// admitted it returns (release, true) and the caller MUST defer
// release(). On rejection the HTTP response is already written.
func voiceAdmit(w http.ResponseWriter, r *http.Request, user *models.User, actionKey string, requestUnits int64) (func(), bool) {
	isAdmin := auth.IsAdmin(user)
	class := voiceClassForAction(actionKey)
	if class == "" {
		log.Printf("voice limits: unclassified action key")
		jsonError(w, "internal error", http.StatusInternalServerError)
		return nil, false
	}

	if !voiceAcquireInflight(user.ID) {
		auth.SetRetryAfter(w, 2*time.Second)
		jsonError(w, "too many concurrent voice requests — retry in a moment", http.StatusTooManyRequests)
		return nil, false
	}
	var releaseBudget = func() {}
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			releaseBudget()
			voiceReleaseInflight(user.ID)
		})
	}

	limit := voiceSTTPerMinute
	if class == voiceClassTTS {
		limit = voiceTTSPerMinute
	}
	// Intake and Agent Mode share one modality pool. A caller cannot double
	// the allowance by alternating surfaces while spending the same provider
	// resource.
	key := class + ":" + strconv.FormatInt(user.ID, 10)
	if retryAfter := voiceRates.checkAndRecord(key, time.Now(), limit); retryAfter > 0 {
		release()
		auth.SetRetryAfter(w, retryAfter)
		jsonError(w, "voice rate limit reached — try again shortly", http.StatusTooManyRequests)
		return nil, false
	}

	// PAI-161 daily AI cap — the same gate as every other AI surface.
	if ok, _, _, bypass := CheckUsageCap(user.ID, isAdmin); !ok {
		release()
		jsonError(w, "Daily AI limit reached. Ask an admin to raise the cap.", http.StatusTooManyRequests)
		return nil, false
	} else if bypass {
		w.Header().Set("X-AI-Over-Cap", "true")
	}

	// Daily voice budget. Soft cap: admins pass (PAI-161 doctrine);
	// a DB hiccup fails open, same as CheckUsageCap.
	if !isAdmin {
		used, budgetRelease, err := voiceReserveDailyBudget(r.Context(), user.ID, actionKey, requestUnits)
		if err != nil {
			log.Printf("voice limits: units used today: %v", err)
			return release, true
		}
		if budgetRelease == nil {
			release()
			auth.SetRetryAfter(w, timeToUTCMidnight())
			jsonError(w, fmt.Sprintf("daily voice budget reached (%d of %d units) — try again tomorrow or ask an admin to raise it", used, voiceDailyBudget(actionKey)), http.StatusTooManyRequests)
			return nil, false
		}
		releaseBudget = budgetRelease
	}
	return release, true
}

func resetVoiceLimitsForTest() {
	voiceInflight.mu.Lock()
	voiceInflight.byUser = map[int64]int{}
	voiceInflight.mu.Unlock()
	voiceRates.mu.Lock()
	voiceRates.entries = map[string][]time.Time{}
	voiceRates.mu.Unlock()
	voiceBudgetReservations.mu.Lock()
	voiceBudgetReservations.units = map[string]int64{}
	voiceBudgetReservations.mu.Unlock()
}

// ResetVoiceLimitsForTest empties the in-memory limiter state so test
// cases stay independent (the daily budgets live in the per-test DB and
// need no reset). Production code never calls this.
func ResetVoiceLimitsForTest() {
	resetVoiceLimitsForTest()
}
