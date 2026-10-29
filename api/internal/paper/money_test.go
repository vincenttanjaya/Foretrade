package paper

import "testing"

func TestMoney(t *testing.T) {
	for x, want := range map[float64]string{2640.5: "$2,640.50", 84650: "$84,650", 1234567.891: "$1,234,567.89", 142.314: "$142.31", 0.21543: "$0.2154"} {
		if got := Money(x); got != want {
			t.Errorf("Money(%v) = %q, want %q", x, got, want)
		}
	}
}
