// Command replay is the cache simulator and recall benchmark. It replays
// transcript fixtures through the same recall and planning code the server
// runs, against a fake provider with a real prefix cache, and reports:
//
//   - the cached share of input tokens for step vs sliding compaction, with
//     recall hysteresis on and off (target: at least 80% at steady state);
//   - recall@4 on labelled callbacks for the bi-encoder alone vs with the
//     cross-encoder rerank, optionally swept over tau, the gap and theta.
//
// It spends no provider tokens, so Pieces 4 and 5 can be tuned for free.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/infer"
	fakeinfer "github.com/quinnovator/contactd/internal/infer/fake"
	fakellm "github.com/quinnovator/contactd/internal/llm/fake"
	"github.com/quinnovator/contactd/internal/plan"
	"github.com/quinnovator/contactd/internal/recall"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "replay:", err)
		os.Exit(1)
	}
}

type options struct {
	fixtures   string
	generate   int
	seed       uint64
	backend    string
	modelsDir  string
	variant    string
	onnxLib    string
	pooling    string
	cacheMode  string
	tailBudget int
	sweep      bool
	out        string
	minWarm    float64
}

// report is the machine-readable output, stored nightly for trend.
type report struct {
	Backend     string   `json:"backend"`
	CacheMode   string   `json:"cache_mode"`
	Transcripts int      `json:"transcripts"`
	Variants    []result `json:"variants"`
	Sweep       []sweep  `json:"sweep,omitempty"`
}

type sweep struct {
	Tau         float32 `json:"tau"`
	GapMinutes  int64   `json:"gap_minutes"`
	Theta       float32 `json:"theta"`
	Episodes    int     `json:"episodes"`
	RecallAt4S1 float64 `json:"recall_at_4_stage1"`
	RecallAt4RR float64 `json:"recall_at_4_rerank"`
	QuietRecall float64 `json:"quiet_recalled_per_turn"`
}

func run(args []string, stdout io.Writer) error {
	var o options
	fs := flag.NewFlagSet("replay", flag.ContinueOnError)
	fs.StringVar(&o.fixtures, "fixtures", "testdata/transcripts", "directory of *.jsonl transcripts")
	fs.IntVar(&o.generate, "generate", 0, "write this many synthetic transcripts into -fixtures and exit")
	fs.Uint64Var(&o.seed, "seed", 1, "seed for -generate")
	fs.StringVar(&o.backend, "infer", "fake", "inference backend: fake, or ort (needs a build with -tags ORT)")
	fs.StringVar(&o.modelsDir, "models-dir", "models", "directory holding embedder/ and reranker/ (ort backend)")
	fs.StringVar(&o.variant, "model-variant", "int8", "int8 or fp32 (ort backend)")
	fs.StringVar(&o.onnxLib, "onnx-lib", os.Getenv("ONNXRUNTIME_LIB_DIR"), "ONNX Runtime library path or directory (ort backend)")
	fs.StringVar(&o.pooling, "pooling", "cls", "embedder pooling: cls or mean (ort backend)")
	fs.StringVar(&o.cacheMode, "cache", "breakpoints", "provider cache model: breakpoints (Anthropic) or prefix (OpenAI, vLLM)")
	fs.IntVar(&o.tailBudget, "tail-budget", 1500, "tail token budget; small so short fixtures still compact")
	fs.BoolVar(&o.sweep, "sweep", false, "also sweep tau, gap and theta for recall quality")
	fs.StringVar(&o.out, "out", "", "also write the results as JSON to this file")
	fs.Float64Var(&o.minWarm, "min-warm-cached", 0, "exit non-zero if the default planner's warm cached share is below this (e.g. 0.8)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}
	if o.generate > 0 {
		return generate(o.fixtures, o.generate, o.seed)
	}

	transcripts, err := load(o.fixtures)
	if err != nil {
		return err
	}
	m, closeModels, err := backend(&o)
	if err != nil {
		return err
	}
	defer closeModels()

	base := params{
		recall: recall.DefaultParams(),
		plan:   plan.DefaultParams(),
		agent:  core.AgentConfig{System: systemPrompt},
	}
	base.plan.TailBudget = o.tailBudget
	if o.cacheMode == "prefix" {
		base.mode = fakellm.CachePrefix
	}
	if o.backend == "fake" {
		// The bag-of-words embedder separates topics far more sharply than a
		// real model does, so its drift threshold sits lower.
		base.recall.Tau = 0.2
	}

	ctx := context.Background()
	rep := report{Backend: o.backend, CacheMode: o.cacheMode, Transcripts: len(transcripts)}
	for _, v := range []variant{
		{Name: "step + hysteresis (default)"},
		{Name: "step, no hysteresis", NoHysteresis: true},
		{Name: "slide + hysteresis", Slide: true},
		{Name: "slide, no hysteresis", Slide: true, NoHysteresis: true},
	} {
		res := result{Variant: v}
		for _, t := range transcripts {
			if err := simulate(ctx, t, m, base, v, &res); err != nil {
				return err
			}
		}
		res.finish()
		rep.Variants = append(rep.Variants, res)
	}
	if o.sweep {
		if rep.Sweep, err = runSweep(ctx, transcripts, m, base); err != nil {
			return err
		}
	}

	if err := printReport(stdout, &rep); err != nil {
		return err
	}
	if o.out != "" {
		raw, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			return fmt.Errorf("encode report: %w", err)
		}
		if err := os.WriteFile(o.out, append(raw, '\n'), 0o600); err != nil {
			return fmt.Errorf("write %s: %w", o.out, err)
		}
	}
	if got := rep.Variants[0].WarmCachedShare; got < o.minWarm {
		return fmt.Errorf("warm cached share is %.1f%%, below the required %.1f%%", 100*got, 100*o.minWarm)
	}
	return nil
}

// systemPrompt is sized like a real agent's, so tier 0 carries realistic
// weight in the cached share.
const systemPrompt = `You are Ada, a long-standing colleague of the person you are talking to.
You keep one continuous relationship with them: there are no separate threads, and you
remember earlier conversations when they are relevant. Answer plainly and briefly. When
you are unsure, say so. When an earlier conversation is shown to you as memory, use it
the way a person would: bring it up only if it bears on what is being asked now. Never
claim to have run code you did not run. Prefer one concrete recommendation to a survey
of options, and say what you would do first. If the person changes the subject, follow
them; do not steer back to the old topic unless they do.`

func load(dir string) ([][]message, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", dir, err)
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no *.jsonl transcripts in %s (create some with -generate)", dir)
	}
	sort.Strings(paths)
	out := make([][]message, 0, len(paths))
	for _, p := range paths {
		msgs, err := readTranscript(p)
		if err != nil {
			return nil, err
		}
		out = append(out, msgs)
	}
	return out, nil
}

func backend(o *options) (*models, func(), error) {
	if o.backend == "fake" {
		return &models{embed: fakeinfer.Embedder{}, rerank: fakeinfer.Reranker{}, memo: map[string][]float32{}}, func() {}, nil
	}
	if o.backend != "ort" {
		return nil, nil, fmt.Errorf("unknown -infer backend %q", o.backend)
	}
	if msg := infer.SignalHazard(os.Getenv("GODEBUG")); msg != "" {
		fmt.Fprintln(os.Stderr, "replay: warning:", msg)
	}
	embed, rerank, closeFn, err := openModels(o)
	if err != nil {
		return nil, nil, err
	}
	return &models{embed: embed, rerank: rerank, memo: map[string][]float32{}}, closeFn, nil
}

// runSweep varies the segmentation and threshold knobs and measures recall
// quality for each setting, with the default planner.
func runSweep(ctx context.Context, transcripts [][]message, m *models, base params) ([]sweep, error) {
	var out []sweep
	taus := []float32{base.recall.Tau * 0.5, base.recall.Tau, base.recall.Tau * 1.5, base.recall.Tau * 2}
	for _, tau := range taus {
		for _, gap := range []int64{10, 30, 120} {
			for _, theta := range []float32{-2, 0, 2} {
				pr := base
				pr.recall.Tau, pr.recall.GapMS, pr.recall.Theta = tau, gap*60_000, theta
				var res result
				for _, t := range transcripts {
					if err := simulate(ctx, t, m, pr, variant{}, &res); err != nil {
						return nil, err
					}
				}
				res.finish()
				out = append(out, sweep{
					Tau: tau, GapMinutes: gap, Theta: theta, Episodes: res.Episodes,
					RecallAt4S1: res.RecallAt4S1, RecallAt4RR: res.RecallAt4RR,
					QuietRecall: ratio(int64(res.QuietRecalled), int64(res.QuietTurns)),
				})
			}
		}
	}
	return out, nil
}

func printReport(w io.Writer, rep *report) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	// tabwriter buffers; a write error surfaces from Flush below.
	pf := func(format string, args ...any) { _, _ = fmt.Fprintf(tw, format, args...) }

	pf("replay: %d transcripts, inference=%s, provider cache=%s\n\n", rep.Transcripts, rep.Backend, rep.CacheMode)
	pf("CACHE SIMULATOR\tturns\tcached\twarm cached\tpredicted\tbreaks at tier 0/1/2/3/4\n")
	for i := range rep.Variants {
		r := &rep.Variants[i]
		pf("%s\t%d\t%.1f%%\t%.1f%%\t%.1f%%\t%d/%d/%d/%d/%d\n", r.Variant.Name, r.Turns,
			100*r.CachedShare, 100*r.WarmCachedShare, 100*r.PredictedShare,
			r.BreakTier[0], r.BreakTier[1], r.BreakTier[2], r.BreakTier[3], r.BreakTier[4])
	}
	d := &rep.Variants[0]
	pf("\nRECALL (default planner)\tcallbacks\trecall@4 stage 1\trecall@4 + rerank\trecalled per quiet turn\tepisodes\n")
	pf("\t%d\t%.1f%%\t%.1f%%\t%.2f\t%d\n", d.Callbacks, 100*d.RecallAt4S1, 100*d.RecallAt4RR,
		ratio(int64(d.QuietRecalled), int64(d.QuietTurns)), d.Episodes)

	if len(rep.Sweep) > 0 {
		pf("\nSWEEP\ttau\tgap\ttheta\tepisodes\trecall@4 stage 1\trecall@4 + rerank\trecalled per quiet turn\n")
		for _, s := range rep.Sweep {
			pf("\t%.2f\t%s\t%+.0f\t%d\t%.1f%%\t%.1f%%\t%.2f\n", s.Tau, time.Duration(s.GapMinutes)*time.Minute,
				s.Theta, s.Episodes, 100*s.RecallAt4S1, 100*s.RecallAt4RR, s.QuietRecall)
		}
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	return nil
}
