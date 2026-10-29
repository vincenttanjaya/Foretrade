package market

import "testing"

// A median time to target past twice a scenario view's horizon says it is
// extrapolated, as its rows there do (gap round G19); a drift view never is.
func TestMedianExtrapolated(t *testing.T) {
	five := NewMixture(100, 5, 0.4, []ScenarioLevel{{Name: "bear", Kind: "bear", P: 0.25, Level: 70}, {Name: "base", Kind: "base", P: 0.5, Level: 180},
		{Name: "bull", Kind: "bull", P: 0.25, Level: 350}})
	tt := NewTimeToTarget(five, ZeroDrift(100, 5, 0.4), 10, nil, nil)
	if tt.MedianYears == nil || *tt.MedianYears != 20 || !tt.MedianExtrapolated || !tt.Rows[4].Extrapolated || tt.Rows[3].Extrapolated {
		t.Fatalf("5-year scenarios, 10×: %v %v %+v", tt.MedianYears, tt.MedianExtrapolated, tt.Rows)
	}
	ten := NewMixture(100, 10, 0.3, []ScenarioLevel{{Name: "bear", Kind: "bear", P: 0.25, Level: 150}, {Name: "base", Kind: "base", P: 0.5, Level: 300},
		{Name: "bull", Kind: "bull", P: 0.25, Level: 500}})
	tt = NewTimeToTarget(ten, ZeroDrift(100, 10, 0.3), 2, nil, nil)
	if tt.MedianYears == nil || *tt.MedianYears < 4 || *tt.MedianYears > 8 || tt.MedianExtrapolated {
		t.Fatalf("10-year scenarios, 2×: %v %v", tt.MedianYears, tt.MedianExtrapolated)
	}
	drift := DriftMixture(100, 5, 0.4, 0.2)
	if tt = NewTimeToTarget(drift, ZeroDrift(100, 5, 0.4), 10, nil, nil); tt.MedianYears == nil || tt.MedianExtrapolated {
		t.Fatalf("drift view: %v %v", tt.MedianYears, tt.MedianExtrapolated)
	}
}
