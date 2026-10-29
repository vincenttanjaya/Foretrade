package research

import (
	"slices"
	"testing"
)

func TestCryptoQuestionStockHeadlineBoundary(t *testing.T) {
	for _, text := range []string{"$META shares rally", "AAPL stock price above$100", "SP500 above7000", "Meta earnings lift shares"} {
		if len(QuestionSymbols(text)) != 0 {
			t.Errorf("forecast question namesstock%q: %v", text, QuestionSymbols(text))
		}
	}
	for _, text := range []string{"meta analysis improves", "Apple pie recipe", "Amazon rainforest report", "Alphabet learning curriculum", "TRUMP campaign speech", "ACE pilot returns", "AR display technology"} {
		if got := tagSymbols(text); len(got) != 0 {
			t.Errorf("ordinary words tagged%q:%v", text, got)
		}
	}
	for text, symbol := range map[string]string{"$META shares rally": "META", "Meta shares rise after earnings": "META", "Apple stock earnings beat": "AAPL", "Amazon shares rally": "AMZN", "Alphabet earnings beat": "GOOGL", "Arweave token rally": "AR", "LayerZero token update": "ZRO", "$ACE token rises": "ACE", "Official Trump token volume rises": "TRUMP"} {
		if !slices.Contains(tagSymbols(text), symbol) {
			t.Errorf("missingheadline%s in%q:%v", symbol, text, tagSymbols(text))
		}
	}
	if !slices.Contains(QuestionSymbols("Arweave token price above$10"), "AR") {
		t.Fatal("newcrypto alias missing")
	}
}

// QuestionStocks names the listed stocks and indices a forecast question
// names: by cashtag, "(TSM)", an exchange prefix, the ticker word (perps) or
// the company name beside a price or financial word; never crypto, never an
// ordinary word, and every instrument a relative question names.
func TestQuestionStocks(t *testing.T) {
	for text, want := range map[string][]string{
		"What will be the NYSE closing price of TSM ADR on 2031-10-06?":   {"TSM"},
		"Where will $tsm trade at the end of 2030?":                       {"TSM"},
		"Taiwan Semiconductor (TSM) close on 2031-10-06":                  {"TSM"},
		"Closing price of NYSE: TSM on 2031-10-06":                        {"TSM"},
		"Where will Taiwan Semiconductor's shares close in 2031?":         {"TSM"},
		"What will Nvidia's market capitalization be on 2027-12-31?":      {"NVDA"},
		"Will NVDA or AMD have the larger market cap at the end of 2027?": {"AMD", "NVDA"},
		"Where will the Sea Limited share price be in 2028?":              {"SE"},
		"Where will the S&P 500 close on 2027-12-31?":                     {"SP500"},
		"Where will BTC trade on 2026-10-14?":                             {},
		"How many rate cuts will the Fed make in 2027?":                   {},
		"Will Apple pie sales rise?":                                      {},
		"Will the SE region see more rain?":                               {},
	} {
		if got := QuestionStocks(text); !slices.Equal(got, want) {
			t.Errorf("QuestionStocks(%q) = %v, want %v", text, got, want)
		}
	}
}
