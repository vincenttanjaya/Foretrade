package strategy

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/vincenttanjaya/Foretrade/api/internal/market"
)

// Indicator is one series the rules compare. Only the fields its Kind uses
// are set; Validate clears the rest so equal indicators compare equal.
type Indicator struct {
	Kind   string   `json:"kind"`
	Period int      `json:"period,omitempty"`
	StdDev float64  `json:"stdDev,omitempty"`
	Fast   int      `json:"fast,omitempty"`
	Slow   int      `json:"slow,omitempty"`
	Signal int      `json:"signal,omitempty"`
	Value  *float64 `json:"value,omitempty"` // const; a pointer so 0 survives omitempty
}

type Condition struct {
	Left  Indicator `json:"left"`
	Op    string    `json:"op"` // > < crosses_above crosses_below
	Right Indicator `json:"right"`
}

// RuleSet fires when every All condition holds and, if Any is present, at
// least one Any condition holds.
type RuleSet struct {
	All []Condition `json:"all,omitempty"`
	Any []Condition `json:"any,omitempty"`
}

type Spec struct {
	Name          string   `json:"name"`
	Symbol        string   `json:"symbol"`
	Interval      string   `json:"interval"`
	Bars          int      `json:"bars"`
	LongEntry     *RuleSet `json:"longEntry,omitempty"`
	LongExit      *RuleSet `json:"longExit,omitempty"`
	ShortEntry    *RuleSet `json:"shortEntry,omitempty"`
	ShortExit     *RuleSet `json:"shortExit,omitempty"`
	StopLossPct   float64  `json:"stopLossPct,omitempty"` // 0 = none
	TakeProfitPct float64  `json:"takeProfitPct,omitempty"`
	PositionPct   float64  `json:"positionPct"`
	FeePct        float64  `json:"feePct"`

	// TrailingStopPct trails the stop this far behind the best price since
	// entry (0 = none); the tighter of it and StopLossPct applies.
	TrailingStopPct float64 `json:"trailingStopPct,omitempty"`
}

const (
	defaultBars   = 1000
	defaultFeePct = 0.05
	maxPeriod     = 400
	maxConditions = 8
)

// UnmarshalJSON applies the contract defaults for omitted bars and feePct,
// whatever the source (request body, model reply or stored row).
func (s *Spec) UnmarshalJSON(b []byte) error {
	type plain Spec
	p := plain{Bars: defaultBars, FeePct: defaultFeePct}
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*s = Spec(p)
	return nil
}

// Validate checks every contract bound and canonicalizes the spec (symbol
// case, unused indicator fields, empty rule sets). Its error text is written
// for both the user and the model's retry.
func (s *Spec) Validate() error {
	s.Name = strings.TrimSpace(s.Name)
	if s.Name == "" {
		s.Name = "Custom strategy"
	}
	info, ok := market.Lookup(s.Symbol)
	switch {
	case utf8.RuneCountInString(s.Name) > 80:
		return errors.New("name must be at most 80 characters")
	case !ok:
		return fmt.Errorf("symbol %q is not in the universe (%s)", s.Symbol, universe())
	case !market.ValidInterval(s.Interval):
		return fmt.Errorf("interval %q must be one of 15m, 1h, 4h, 1d", s.Interval)
	case s.Bars < 200 || s.Bars > 3000:
		return errors.New("bars must be 200..3000")
	case !(s.PositionPct >= 1 && s.PositionPct <= 100):
		return errors.New("positionPct must be 1..100")
	case !(s.FeePct >= 0 && s.FeePct <= 1):
		return errors.New("feePct must be 0..1")
	case s.StopLossPct != 0 && !(s.StopLossPct >= 0.1 && s.StopLossPct <= 50):
		return errors.New("stopLossPct must be 0.1..50 (or omitted)")
	case s.TakeProfitPct != 0 && !(s.TakeProfitPct >= 0.1 && s.TakeProfitPct <= 50):
		return errors.New("takeProfitPct must be 0.1..50 (or omitted)")
	case s.TrailingStopPct != 0 && !(s.TrailingStopPct >= 0.5 && s.TrailingStopPct <= 50):
		return errors.New("trailingStopPct must be 0.5 to 50")
	}
	s.Symbol = info.Symbol
	for _, set := range []struct {
		name string
		rs   **RuleSet
	}{{"longEntry", &s.LongEntry}, {"longExit", &s.LongExit}, {"shortEntry", &s.ShortEntry}, {"shortExit", &s.ShortExit}} {
		rs := *set.rs
		if rs == nil {
			continue
		}
		if len(rs.All)+len(rs.Any) == 0 {
			*set.rs = nil
			continue
		}
		if len(rs.All)+len(rs.Any) > maxConditions {
			return fmt.Errorf("%s has more than %d conditions", set.name, maxConditions)
		}
		for i := range rs.All {
			if err := rs.All[i].validate(); err != nil {
				return fmt.Errorf("%s.all[%d]: %w", set.name, i, err)
			}
		}
		for i := range rs.Any {
			if err := rs.Any[i].validate(); err != nil {
				return fmt.Errorf("%s.any[%d]: %w", set.name, i, err)
			}
		}
	}
	if s.LongEntry == nil && s.ShortEntry == nil {
		return errors.New("at least one of longEntry or shortEntry needs a condition")
	}
	return nil
}

func (c *Condition) validate() error {
	switch c.Op {
	case ">", "<", "crosses_above", "crosses_below":
	default:
		return fmt.Errorf("op %q must be one of >, <, crosses_above, crosses_below", c.Op)
	}
	if err := c.Left.validate(); err != nil {
		return fmt.Errorf("left: %w", err)
	}
	if err := c.Right.validate(); err != nil {
		return fmt.Errorf("right: %w", err)
	}
	if c.Left.Kind == "const" && c.Right.Kind == "const" {
		return errors.New("a condition cannot compare two constants")
	}
	return nil
}

func (in *Indicator) validate() error {
	inRange := func(name string, v int) error {
		if v < 1 || v > maxPeriod {
			return fmt.Errorf("%s %s must be 1..%d", in.Kind, name, maxPeriod)
		}
		return nil
	}
	switch in.Kind {
	case "price", "volume":
		*in = Indicator{Kind: in.Kind}
	case "sma", "ema", "rsi", "roc", "atr", "highest", "lowest", "volume_sma":
		if err := inRange("period", in.Period); err != nil {
			return err
		}
		*in = Indicator{Kind: in.Kind, Period: in.Period}
	case "bb_upper", "bb_lower":
		if err := inRange("period", in.Period); err != nil {
			return err
		}
		if !(in.StdDev > 0 && in.StdDev <= 10) {
			return fmt.Errorf("%s stdDev must be > 0 and <= 10", in.Kind)
		}
		*in = Indicator{Kind: in.Kind, Period: in.Period, StdDev: in.StdDev}
	case "macd", "macd_signal":
		if err := cmp.Or(inRange("fast", in.Fast), inRange("slow", in.Slow), inRange("signal", in.Signal)); err != nil {
			return err
		}
		if in.Fast >= in.Slow {
			return fmt.Errorf("%s fast must be smaller than slow", in.Kind)
		}
		*in = Indicator{Kind: in.Kind, Fast: in.Fast, Slow: in.Slow, Signal: in.Signal}
	case "const":
		if in.Value == nil {
			return errors.New("const needs a value")
		}
		*in = Indicator{Kind: in.Kind, Value: in.Value}
	default:
		return fmt.Errorf("unknown indicator kind %q", in.Kind)
	}
	return nil
}

func universe() string {
	syms := make([]string, len(market.Universe))
	for i, a := range market.Universe {
		syms[i] = a.Symbol
	}
	return strings.Join(syms, ", ")
}
