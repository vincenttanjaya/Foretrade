package rtquote

import (
	"errors"
	"slices"
	"testing"
)

func TestParseSwitches(t *testing.T) {
	for _, c := range []struct {
		in            string
		venues, wrong []string
	}{
		{DefaultSwitches, []string{"krx", "hkex", "hose", "sgx", "twse"}, nil},
		{"", nil, nil},
		{" TWSE, krx ,krx,, nyse", []string{"krx", "twse"}, []string{"nyse"}},
		{"off", nil, []string{"off"}},
	} {
		v, u := ParseSwitches(c.in)
		if !slices.Equal(v, c.venues) || !slices.Equal(u, c.wrong) {
			t.Errorf("%q: %v %v, want %v %v", c.in, v, u, c.venues, c.wrong)
		}
	}
}

func TestEnabled(t *testing.T) {
	names := func(as []Adapter) (out []string) {
		for _, a := range as {
			out = append(out, a.Info().Name)
		}
		return out
	}
	if got := names(Enabled([]string{"krx"}, true)); !slices.Equal(got, []string{"naver"}) {
		t.Fatalf("krx: %v", got)
	}
	if got := Enabled(nil, true); len(got) != 0 {
		t.Fatalf("none: %v", names(got))
	}
	all := Enabled([]string{"krx", "hkex", "hose", "sgx", "twse"}, false)
	for _, a := range all {
		if a.Info().Gate == GateTWSEYahoo {
			t.Fatalf("%s enabled with its gate off", a.Info().Name)
		}
	}
	if len(Enabled([]string{"krx", "hkex", "hose", "sgx", "twse"}, true)) != len(All) {
		t.Fatal("every switch on leaves an adapter out")
	}
}

func TestRefusedAndSymbols(t *testing.T) {
	err := Refused("HTTP 403")
	if !errors.Is(err, ErrRefused) || err.Error() != "refused: HTTP 403" {
		t.Fatal(err)
	}
	w := Want{"b": {}, "a": {}, "c": {}}
	if got := w.Symbols(); !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Fatal(got)
	}
}
