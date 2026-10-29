package chat

import "github.com/vincenttanjaya/Foretrade/api/internal/profile"

// validLens: the request's optional lens. "" and "auto" infer it from the
// question (the prompt's Lens section); investor and trader fix it.
func validLens(lens string) bool {
	switch lens {
	case "", "auto", "investor", "trader":
		return true
	}
	return false
}

// lensRule is the message's "Lens for this message" line under the prompt's
// Lens section: the user's choice, or for auto the hint of a saved trading
// style; "" leaves the inference as the prompt describes it. Style is a
// validated enum, so the hint is trusted text, unlike the profile block.
func lensRule(lens string, p profile.Profile) string {
	switch lens {
	case "investor":
		return "\n\nLens for this message: investor, chosen by the user. Frame the answer as an investor: the long-term view, valuation and labelled scenario odds, " +
			"with the memo for investment questions; a trading question still gets its levels, set inside the long-term view."
	case "trader":
		return "\n\nLens for this message: trader, chosen by the user. Frame the answer as a trader: the setup, levels, reward:risk and a short horizon; " +
			"an investment or possibility question still gets its direct answer and labelled odds, briefly, before the tradable setup."
	}
	if h := profile.StyleHorizon(p); h != "" {
		return "\n\nLens for this message: inferred from the question. The user's saved trading style is " + *p.Style + " (" + h + "): when the question gives " +
			"no signal, lean trader on that horizon; a clear investor or hybrid signal in the question still wins."
	}
	return ""
}
