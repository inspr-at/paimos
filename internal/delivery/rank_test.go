// SPDX-License-Identifier: AGPL-3.0-only

package delivery

import (
	"encoding/json"
	"errors"
	"math/big"
	"math/rand/v2"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestRankGoldenVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/ranks.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct{ Op, A, B, Want, Error string }
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, v := range vectors {
		t.Run(v.Op+"/"+v.A+"/"+v.B, func(t *testing.T) {
			var got string
			var err error
			switch v.Op {
			case "between":
				got, err = Between(v.A, v.B)
			case "after":
				got, err = After(v.A)
			default:
				t.Fatalf("unknown vector operation %q", v.Op)
			}
			if v.Error != "" {
				var refusal *Conflict
				if !errors.As(err, &refusal) || refusal.Code != v.Error || got != "" {
					t.Fatalf("want empty key and %s, got %q / %v", v.Error, got, err)
				}
				return
			}
			if err != nil || got != v.Want {
				t.Fatalf("want %q, got %q / %v", v.Want, got, err)
			}
			assertBetween(t, v.A, v.B, got)
		})
	}
}

// Numeric fractions independently prove the equivalence with C string order.
func fraction(key string, upper bool) *big.Rat {
	if key == "" && upper {
		return big.NewRat(1, 1)
	}
	n := new(big.Int)
	for _, c := range []byte(key) {
		n.Mul(n, big.NewInt(62))
		n.Add(n, big.NewInt(int64(strings.IndexByte(rankDigits, c))))
	}
	d := new(big.Int).Exp(big.NewInt(62), big.NewInt(int64(len(key))), nil)
	return new(big.Rat).SetFrac(n, d)
}

func assertBetween(t *testing.T, lower, upper, got string) {
	t.Helper()
	if !ValidRank(got) || got <= lower || upper != "" && got >= upper {
		t.Fatalf("%q is not canonical inside (%q,%q)", got, lower, upper)
	}
	value := fraction(got, false)
	if value.Cmp(fraction(lower, false)) <= 0 || value.Cmp(fraction(upper, true)) >= 0 {
		t.Fatalf("fraction %s lies outside (%q,%q)", value, lower, upper)
	}
}

func TestInvalidRanksAndBounds(t *testing.T) {
	for _, bad := range []string{"0", "00", "A0", "V-", " V", "á", strings.Repeat("A", 33)} {
		if ValidRank(bad) {
			t.Errorf("accepted %q", bad)
		}
		for _, bounds := range [][2]string{{bad, ""}, {"", bad}} {
			if _, err := Between(bounds[0], bounds[1]); !errors.Is(err, ErrInvalidRank) {
				t.Errorf("bad bounds %v: %v", bounds, err)
			}
		}
		if _, err := After(bad); !errors.Is(err, ErrInvalidRank) {
			t.Errorf("After(%q): %v", bad, err)
		}
	}
	for _, bounds := range [][2]string{{"A", "A"}, {"B", "A"}, {"A1", "A"}} {
		if _, err := Between(bounds[0], bounds[1]); !errors.Is(err, ErrRankBounds) {
			t.Errorf("bounds %v: %v", bounds, err)
		}
	}
}

func TestRandomRankInsertions(t *testing.T) {
	rng := rand.New(rand.NewPCG(596, 2))
	keys := []string{}
	for range 1500 {
		i := rng.IntN(len(keys) + 1)
		lower, upper := "", ""
		if i > 0 {
			lower = keys[i-1]
		}
		if i < len(keys) {
			upper = keys[i]
		}
		key, err := Between(lower, upper)
		if err != nil {
			t.Fatalf("random insertion %d: %v", i, err)
		}
		assertBetween(t, lower, upper, key)
		keys = slices.Insert(keys, i, key)
	}
	if !slices.IsSorted(keys) {
		t.Fatal("insertion order lost")
	}
}

func TestRepeatedGapExhaustsWithoutReturningInvalidKey(t *testing.T) {
	upper := "1"
	for i := 0; i < 32*6+1; i++ {
		got, err := Between("", upper)
		if err != nil {
			if !errors.Is(err, ErrRankSpaceExhausted) || got != "" || len(upper) != 32 {
				t.Fatalf("gap did not exhaust at the limit: %q / %v", got, err)
			}
			return
		}
		assertBetween(t, "", upper, got)
		upper = got
	}
	t.Fatal("gap never reached the documented bound")
}

func TestSeedRanks(t *testing.T) {
	for _, n := range []int{0, 1, 2, 30, 31, 60, 61, 62, 63, 1000, 5000} {
		keys, err := SeedRanks(n)
		if err != nil || len(keys) != n {
			t.Fatalf("seed %d: %d / %v", n, len(keys), err)
		}
		width, capacity := 1, 1
		for capacity < n+1 {
			capacity *= 62
			width++
		}
		for i, key := range keys {
			if !ValidRank(key) || len(key) != width || key[len(key)-1] > 'U' {
				t.Fatalf("seed %d key %d has wrong grammar/length/headroom: %q", n, i, key)
			}
			if fraction(key, false).Cmp(big.NewRat(1, 2)) >= 0 {
				t.Fatalf("seed outside lower half: %q", key)
			}
			if i > 0 && key <= keys[i-1] {
				t.Fatalf("seed %d duplicate/order at %d", n, i)
			}
		}
	}
	for _, n := range []int{-1, 5001, int(^uint(0) >> 1)} {
		if keys, err := SeedRanks(n); !errors.Is(err, ErrSeedCount) || keys != nil {
			t.Fatalf("bad seed %d: %v / %v", n, keys, err)
		}
	}
}

func FuzzBetween(f *testing.F) {
	for _, bounds := range [][2]string{{"", ""}, {"", "01"}, {"V", "W"}, {"zz", "zz1"}, {"A", "A1"}} {
		f.Add(bounds[0], bounds[1])
	}
	f.Fuzz(func(t *testing.T, lower, upper string) {
		key, err := Between(lower, upper)
		if lower != "" && !ValidRank(lower) || upper != "" && !ValidRank(upper) {
			if !errors.Is(err, ErrInvalidRank) {
				t.Fatalf("invalid input: %v", err)
			}
			return
		}
		if upper != "" && lower >= upper {
			if !errors.Is(err, ErrRankBounds) {
				t.Fatalf("invalid bounds: %v", err)
			}
			return
		}
		if err != nil {
			if !errors.Is(err, ErrRankSpaceExhausted) || key != "" {
				t.Fatalf("unexpected refusal: %q / %v", key, err)
			}
			return
		}
		assertBetween(t, lower, upper, key)
	})
}
