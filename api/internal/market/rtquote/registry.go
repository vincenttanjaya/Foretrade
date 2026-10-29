package rtquote

import "slices"

// All is every real-time adapter, in chain order within a venue: the first
// enabled one of a venue is its preferred source, the next its fallback
// before Yahoo. Each adapter fills its own slot (replace the nil); the
// "// ---" lines keep parallel edits apart, so leave them in place.
var All = slices.DeleteFunc([]Adapter{
	Naver{}, // KRX
	// ---
	Tencent{}, // HKEX
	// ---
	Vietcap{}, // HOSE
	// ---
	SGX{}, // SGX
	// ---
	MIS{}, // TWSE: MIS (mis.twse.com.tw), first
	// ---
	YahooTW{}, // TWSE: Yahoo Taiwan (tw.stock.yahoo.com), second, Gate GateTWSEYahoo
}, func(a Adapter) bool { return a == nil })
