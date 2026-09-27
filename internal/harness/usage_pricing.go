// SPDX-License-Identifier: AGPL-3.0-only

package harness

import (
	"context"
	"errors"
	"math/big"
	"net/http"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
)

// ModelPrice is an immutable tenant-owned USD list-price version, not a bill.
type ModelPrice struct {
	Model                    string    `json:"model"`
	Version                  int64     `json:"version"`
	InputUSDPerMillion       string    `json:"input_usd_per_million"`
	OutputUSDPerMillion      string    `json:"output_usd_per_million"`
	CachedInputUSDPerMillion string    `json:"cached_input_usd_per_million"`
	Currency                 string    `json:"currency"`
	CreatedAt                time.Time `json:"created_at"`
}

type usagePriceWrite struct {
	Model                    string `json:"model"`
	Version                  int64  `json:"version"`
	InputUSDPerMillion       string `json:"input_usd_per_million"`
	OutputUSDPerMillion      string `json:"output_usd_per_million"`
	CachedInputUSDPerMillion string `json:"cached_input_usd_per_million"`
}

var usageRateRE = regexp.MustCompile(`^(0|[1-9][0-9]{0,6})(\.[0-9]{1,6})?$`)

func normalizeUsageRate(raw string) (string, error) {
	if !usageRateRE.MatchString(raw) {
		return "", workorders.Fail(400, "rate must be a nonnegative USD decimal string with at most six fractional places")
	}
	rate, ok := new(big.Rat).SetString(raw)
	if !ok || rate.Cmp(big.NewRat(1_000_000, 1)) > 0 {
		return "", workorders.Fail(400, "rate exceeds 1000000 USD per million tokens")
	}
	return rate.FloatString(6), nil
}

// Rates have six decimal places and tokens are integers, so twelve fractional
// USD places represent the quotient by one million exactly, including extremes.
func estimateUsageCost(u SessionModelUsage, price ModelPrice) *string {
	if u.InputTokens == nil || u.OutputTokens == nil || u.CachedInputTokens == nil {
		return nil
	}
	total := new(big.Rat)
	for _, part := range []struct {
		tokens int64
		rate   string
	}{
		{*u.InputTokens - *u.CachedInputTokens, price.InputUSDPerMillion},
		{*u.OutputTokens, price.OutputUSDPerMillion},
		{*u.CachedInputTokens, price.CachedInputUSDPerMillion},
	} {
		rate, ok := new(big.Rat).SetString(part.rate)
		if !ok {
			return nil
		} // Database constraints and write validation prevent this.
		total.Add(total, rate.Mul(rate, big.NewRat(part.tokens, 1)))
	}
	result := total.Quo(total, big.NewRat(1_000_000, 1)).FloatString(12)
	return &result
}

const usagePriceColumns = `model,version,input_usd_per_million::text,output_usd_per_million::text,cached_input_usd_per_million::text,created_at`

func scanUsagePrice(row pgx.Row) (ModelPrice, error) {
	out := ModelPrice{Currency: "USD"}
	err := row.Scan(&out.Model, &out.Version, &out.InputUSDPerMillion, &out.OutputUSDPerMillion, &out.CachedInputUSDPerMillion, &out.CreatedAt)
	return out, err
}

func loadUsagePrice(ctx context.Context, tx pgx.Tx, model string, version *int64) (ModelPrice, error) {
	return scanUsagePrice(tx.QueryRow(ctx, `SELECT `+usagePriceColumns+` FROM model_prices WHERE model=$1 AND ($2::bigint IS NULL OR version=$2) ORDER BY version DESC LIMIT 1`, model, version))
}

func (m *Module) createUsagePrice(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	if p.Kind != tenant.Person {
		return nil, workorders.Fail(403, "models.manage person permission required")
	}
	if err := authz.RequireTx(r.Context(), tx, p, "models.manage", authz.Scope{}); err != nil {
		return nil, workorders.Fail(403, "models.manage permission required")
	}
	var in usagePriceWrite
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if !validUsageModel(in.Model) || in.Version < 1 || in.Version > maxUsageCount {
		return nil, workorders.Fail(400, "invalid model or price version")
	}
	for _, rate := range []*string{&in.InputUSDPerMillion, &in.OutputUSDPerMillion, &in.CachedInputUSDPerMillion} {
		value, err := normalizeUsageRate(*rate)
		if err != nil {
			return nil, err
		}
		*rate = value
	}
	// Stable tenant/model lock serializes competing version inserts, including
	// first insert where there is no row to lock. No cross-tenant shared key.
	if _, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "aeon.model-price:"+p.TenantID+":"+in.Model); err != nil {
		return nil, err
	}
	old, err := loadUsagePrice(r.Context(), tx, in.Model, &in.Version)
	if err == nil {
		if old.InputUSDPerMillion != in.InputUSDPerMillion || old.OutputUSDPerMillion != in.OutputUSDPerMillion || old.CachedInputUSDPerMillion != in.CachedInputUSDPerMillion {
			return nil, workorders.Fail(409, "price version is immutable")
		}
		return old, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	latest, err := loadUsagePrice(r.Context(), tx, in.Model, nil)
	if err == nil && in.Version <= latest.Version {
		return nil, workorders.Fail(409, "new price version must increase")
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	out, err := scanUsagePrice(tx.QueryRow(r.Context(), `INSERT INTO model_prices
        (tenant_id,model,version,input_usd_per_million,output_usd_per_million,cached_input_usd_per_million)
        VALUES($1,$2,$3,$4::numeric,$5::numeric,$6::numeric) RETURNING `+usagePriceColumns,
		p.TenantID, in.Model, in.Version, in.InputUSDPerMillion, in.OutputUSDPerMillion, in.CachedInputUSDPerMillion))
	if err != nil {
		return nil, err
	}
	_, err = events.Append(r.Context(), tx, p, events.Change{Type: "harness.model_price_created", After: out})
	return out, err
}

func (m *Module) listUsagePrices(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	model := r.URL.Query().Get("model")
	if model != "" && !validUsageModel(model) {
		return nil, workorders.Fail(400, "invalid model")
	}
	rows, err := tx.Query(r.Context(), `SELECT DISTINCT ON (model) `+usagePriceColumns+` FROM model_prices WHERE ($1='' OR model=$1) ORDER BY model,version DESC`, model)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []ModelPrice{}
	for rows.Next() {
		item, err := scanUsagePrice(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return struct {
		Items []ModelPrice `json:"items"`
	}{items}, rows.Err()
}
