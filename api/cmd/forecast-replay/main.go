// Command forecast-replay writes the forecast desk's historical replay seed
// (internal/forecast/replay_seed.json, embedded and inserted at API boot): the
// three-specialist panel re-run as of 2026-07-15, 08-12 and 09-09 on 24
// two-week crypto threshold questions, with only the Binance daily bars and
// dated news published before each date. Each question costs about five
// model calls and up to four Brave News calls: run it by hand, once.
//
//	AI_PROVIDER=… AI_MODEL=… AI_API_KEY=… go run ./cmd/forecast-replay [-out file] [-n 24] [-concurrency 2]
//
// It reads the API's environment: the AI_* variables (env mode only: the
// admin console's stored settings need the database), MODEL_KNOWLEDGE_CUTOFF,
// BRAVE_API_KEY and BINANCE_BASE. Failed
// questions are logged and skipped; fewer than 20 successes exits non-zero
// and leaves the seed file untouched. It ends with a score summary: the
// desk's Brier against the volatility-only formula and the coin flip, the
// paired 90% range, and how many answers the shrink guard moved.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/config"
	"github.com/vincenttanjaya/Foretrade/api/internal/forecast"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/research"
)

const minSeed = 20

func main() {
	out := flag.String("out", "internal/forecast/replay_seed.json", "seed file to write")
	n := flag.Int("n", 24, "questions to replay (at most 24)")
	concurrency := flag.Int("concurrency", 2, "questions replayed at once")
	flag.Parse()
	time.Local = time.UTC
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, *out, min(max(*n, 1), 24), max(*concurrency, 1)); err != nil {
		slog.Error("forecast replay", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out string, n, concurrency int) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.AI.Provider == "" {
		return fmt.Errorf("set AI_PROVIDER (and AI_MODEL, AI_API_KEY) to the model the replay should run")
	}
	ai, err := llm.NewFromSettings(cfg.AI)
	if err != nil {
		return err
	}
	// research.New tolerates a nil pool: SearchNews never touches it, and replays read no pages.
	d := &app.Deps{Market: market.NewClient(cfg.BinanceBase), LLM: ai,
		Research: research.New(research.Config{BraveAPIKey: cfg.BraveAPIKey, BraveBase: cfg.BraveBase}, nil, nil)}
	if !d.Research.Enabled() {
		slog.Warn("BRAVE_API_KEY is not set: the replay runs without news")
	}
	qs, err := forecast.ReplayQuestions(ctx, d.Market, ai.KnowledgeCutoff(), time.Now())
	if err != nil {
		return err
	}
	qs = qs[:min(n, len(qs))]

	rows := make([]*forecast.SeedRow, len(qs))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i, q := range qs {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			if q.Outcome == nil {
				slog.Warn("replay question not decidable yet; skipped", "key", q.Key)
				return
			}
			start := time.Now()
			res, err := forecast.Replay(ctx, d, q)
			if err != nil {
				slog.Warn("replay failed; skipped", "key", q.Key, "err", err)
				return
			}
			rows[i] = &forecast.SeedRow{Key: q.Key, AsOf: q.AsOf.Format(time.DateOnly), Draft: q.Draft, PriceRule: *q.Draft.PriceRule,
				Result: res, Outcome: q.Outcome, ResolvedAt: q.ResolvedAt, AnchorP: q.AnchorP}
			slog.Info("replayed", "key", q.Key, "pYes", res.Probabilities[0].P, "anchor", q.AnchorP, "guard", res.Guard, "outcome", *q.Outcome,
				"sources", len(res.Sources), "took", time.Since(start).Round(time.Second))
		})
	}
	wg.Wait()

	lines, done := []string{}, []forecast.SeedRow{}
	for _, r := range rows {
		if r == nil {
			continue
		}
		done = append(done, *r)
		b, err := json.Marshal(r) // Source's prompt text is unexported, so sources carry only id/title/url/source/publishedAt/read
		if err != nil {
			return err
		}
		lines = append(lines, string(b))
	}
	if len(lines) < min(minSeed, n) {
		return fmt.Errorf("only %d of %d questions replayed (need %d); %s left unchanged", len(lines), len(qs), min(minSeed, n), out)
	}
	seed := "[\n" + strings.Join(lines, ",\n") + "\n]\n" // one row per line: reviewable diffs
	if err := os.WriteFile(out, []byte(seed), 0o644); err != nil {
		return err
	}
	slog.Info("replay seed written", "file", out, "rows", len(lines), "bytes", len(seed))
	fmt.Println(forecast.SeedSummary(done))
	return nil
}
