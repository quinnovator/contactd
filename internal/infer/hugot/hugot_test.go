//go:build cgo && ORT

package hugot

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/infer"
)

const (
	onnxFP32 = "model.onnx"
	onnxInt8 = "model_int8.onnx"
)

// Sentences the assertions below are about.
const (
	askReset   = "How do I reset my password?"
	askForgot  = "I forgot my password, how can I change it?"
	queryReset = "how do I reset my password"
	queryBake  = "what temperature should I bake sourdough at"
	aboutReset = "To reset your password, open Settings, choose Security and click Reset password."
	aboutBake  = "Bake the sourdough loaf at 230 degrees Celsius for about forty minutes."
	aboutFlour = "The recipe needs two cups of flour and a pinch of salt."
)

// testConfig locates the native library and the models, or skips the test.
// mise exports ONNXRUNTIME_LIB_DIR; a bare `go test` falls back to the
// repo's third_party/lib and models directories. CONTACTD_MODELS_DIR points
// at another export, and CONTACTD_INTRA_OP_THREADS lets the benchmarks
// compare one multi-threaded call against the default of one thread per call.
func testConfig(tb testing.TB, onnx string) Config {
	tb.Helper()
	root := filepath.Join("..", "..", "..")
	libDir := os.Getenv("ONNXRUNTIME_LIB_DIR")
	if libDir == "" {
		libDir = filepath.Join(root, "third_party", "lib")
	}
	lib := filepath.Join(libDir, "libonnxruntime.so")
	if runtime.GOOS == "darwin" {
		lib = filepath.Join(libDir, "libonnxruntime.dylib")
	}
	if _, err := os.Stat(lib); err != nil {
		tb.Skipf("ONNX Runtime not found at %s: run scripts/fetch-native-deps.sh (mise run deps:native)", lib)
	}
	models := os.Getenv("CONTACTD_MODELS_DIR")
	if models == "" {
		models = filepath.Join(root, "models")
	}
	cfg := Config{
		OnnxLibPath:  lib,
		EmbedderDir:  filepath.Join(models, "embedder"),
		RerankerDir:  filepath.Join(models, "reranker"),
		OnnxFilename: onnx,
	}
	if s := os.Getenv("CONTACTD_INTRA_OP_THREADS"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			tb.Fatalf("CONTACTD_INTRA_OP_THREADS: %v", err)
		}
		cfg.IntraOpThreads = n
	}
	for _, dir := range []string{cfg.EmbedderDir, cfg.RerankerDir} {
		if _, err := os.Stat(filepath.Join(dir, onnx)); err != nil {
			tb.Skipf("model %s not found in %s: run `uv run scripts/export_onnx.py --out models/` (mise run models:fetch)", onnx, dir)
		}
	}
	return cfg
}

func open(tb testing.TB, cfg Config) *Models {
	tb.Helper()
	m, err := Open(cfg)
	if err != nil {
		tb.Fatalf("Open: %v", err)
	}
	tb.Cleanup(func() {
		if err := m.Close(); err != nil {
			tb.Errorf("Close: %v", err)
		}
	})
	return m
}

func embed(tb testing.TB, m *Models, texts ...string) [][]float32 {
	tb.Helper()
	vecs, err := m.Embedder().Run(tb.Context(), texts)
	if err != nil {
		tb.Fatalf("embed: %v", err)
	}
	if len(vecs) != len(texts) {
		tb.Fatalf("embed returned %d vectors for %d texts", len(vecs), len(texts))
	}
	for i, v := range vecs {
		if len(v) != core.VectorDim {
			tb.Fatalf("vector %d has %d dims, want %d", i, len(v), core.VectorDim)
		}
		if n := norm(v); math.Abs(n-1) > 1e-5 {
			tb.Fatalf("vector %d has norm %v, want 1", i, n)
		}
	}
	return vecs
}

func rerank(tb testing.TB, m *Models, pairs ...infer.Pair) []float32 {
	tb.Helper()
	scores, err := m.Reranker().Run(tb.Context(), pairs)
	if err != nil {
		tb.Fatalf("rerank: %v", err)
	}
	if len(scores) != len(pairs) {
		tb.Fatalf("rerank returned %d scores for %d pairs", len(scores), len(pairs))
	}
	for i, s := range scores {
		if math.IsNaN(float64(s)) || math.IsInf(float64(s), 0) {
			tb.Fatalf("score %d is %v", i, s)
		}
	}
	return scores
}

func norm(v []float32) float64 {
	return math.Sqrt(dot(v, v))
}

func dot(a, b []float32) float64 {
	var sum float64
	for i := range a {
		sum += float64(a[i]) * float64(b[i])
	}
	return sum
}

// words returns a text of about n tokens: every word here is one WordPiece.
func words(n int) string {
	vocab := strings.Fields("the user asked about a slow query in the orders table and we " +
		"looked at the plan then added an index on the customer column which fixed it")
	var sb strings.Builder
	for i := range n {
		sb.WriteString(vocab[i%len(vocab)])
		sb.WriteByte(' ')
	}
	return sb.String()
}

// mixedPairs interleaves two queries so that the expected scores alternate
// in a pattern no regrouping by query would reproduce.
func mixedPairs() (pairs []infer.Pair, relevant []bool) {
	return []infer.Pair{
		{Query: queryReset, Passage: aboutReset},
		{Query: queryBake, Passage: aboutReset},
		{Query: queryReset, Passage: aboutFlour},
		{Query: queryBake, Passage: aboutBake},
		{Query: queryBake, Passage: aboutFlour},
		{Query: queryReset, Passage: aboutBake},
	}, []bool{true, false, false, true, false, false}
}

// TestModels runs every behavioural check against the fp32 and the int8
// files. ONNX Runtime allows one environment per process, so the variants
// take turns, each with its own Models; the checks inside one variant share
// it and run in parallel.
//
//nolint:paralleltest // one ONNX Runtime environment per process; see above
func TestModels(t *testing.T) {
	for _, tc := range []struct {
		onnx string
		// scoreTol and cosTol bound how far a logit or a cosine may move
		// when the same input is run alone instead of in a batch. Dynamic
		// quantization derives activation ranges from the whole batch
		// tensor, so int8 results depend on their batch-mates; fp32 only
		// differs by rounding.
		scoreTol, cosTol float64
	}{
		{onnx: onnxFP32, scoreTol: 1e-3, cosTol: 1e-5},
		{onnx: onnxInt8, scoreTol: 0.5, cosTol: 0.01},
	} {
		t.Run(tc.onnx, func(t *testing.T) {
			m := open(t, testConfig(t, tc.onnx))

			// The group returns once its parallel subtests are done, which
			// is when the lifecycle checks after it may close m.
			t.Run("live", func(t *testing.T) {
				t.Run("paraphrase closer than unrelated", func(t *testing.T) {
					t.Parallel()
					v := embed(t, m, askReset, askForgot, aboutFlour)
					near, far := dot(v[0], v[1]), dot(v[0], v[2])
					if near <= far {
						t.Errorf("cos(paraphrase) = %.3f, cos(unrelated) = %.3f", near, far)
					}
				})

				t.Run("embed batch of 32 matches single runs", func(t *testing.T) {
					t.Parallel()
					texts := make([]string, infer.MaxBatch)
					for i := range texts {
						texts[i] = fmt.Sprintf("note %d: %s", i, words(3+i*5))
					}
					batch := embed(t, m, texts...)
					for i, text := range texts {
						if cos := dot(batch[i], embed(t, m, text)[0]); cos < 1-tc.cosTol {
							t.Errorf("text %d: cos(batched, alone) = %.5f", i, cos)
						}
					}
				})

				t.Run("embed more than one batch", func(t *testing.T) {
					t.Parallel()
					texts := make([]string, 2*infer.MaxBatch+3)
					for i := range texts {
						texts[i] = fmt.Sprintf("message number %d", i)
					}
					batch := embed(t, m, texts...)
					last := len(texts) - 1
					if cos := dot(batch[last], embed(t, m, texts[last])[0]); cos < 1-tc.cosTol {
						t.Errorf("last text: cos(batched, alone) = %.5f", cos)
					}
				})

				t.Run("relevant above irrelevant, as logits", func(t *testing.T) {
					t.Parallel()
					s := rerank(t, m,
						infer.Pair{Query: queryReset, Passage: aboutReset},
						infer.Pair{Query: queryReset, Passage: aboutFlour})
					if s[0] <= s[1] {
						t.Errorf("relevant %.3f <= irrelevant %.3f", s[0], s[1])
					}
					// A sigmoid would have squashed both into (0, 1).
					if s[0] <= 1 || s[1] >= 0 {
						t.Errorf("scores %.3f, %.3f look like probabilities, want raw logits", s[0], s[1])
					}
				})

				t.Run("mixed queries keep input order", func(t *testing.T) {
					t.Parallel()
					pairs, relevant := mixedPairs()
					scores := rerank(t, m, pairs...)
					for i, p := range pairs {
						if (scores[i] > 0) != relevant[i] {
							t.Errorf("pair %d: score %.3f, relevant = %v", i, scores[i], relevant[i])
						}
						alone := rerank(t, m, p)[0]
						if d := math.Abs(float64(scores[i] - alone)); d > tc.scoreTol {
							t.Errorf("pair %d: batched %.3f, alone %.3f", i, scores[i], alone)
						}
					}
				})

				t.Run("rerank batch of 32", func(t *testing.T) {
					t.Parallel()
					pairs := make([]infer.Pair, infer.MaxBatch)
					for i := range pairs {
						pairs[i] = infer.Pair{Query: queryReset, Passage: fmt.Sprintf("%d %s", i, words(4*i))}
					}
					pairs[17].Passage = aboutReset
					scores := rerank(t, m, pairs...)
					for i, s := range scores {
						if i != 17 && s >= scores[17] {
							t.Errorf("filler %d scored %.3f, the relevant passage %.3f", i, s, scores[17])
						}
					}
				})

				t.Run("long input is truncated", func(t *testing.T) {
					t.Parallel()
					// Both are far past the 512-token window and share
					// their first 600 tokens, so they must embed alike.
					long, longer := words(600), words(600)+words(5000)
					v := embed(t, m, long, longer)
					if cos := dot(v[0], v[1]); cos < 1-tc.cosTol {
						t.Errorf("cos(long, longer) = %.5f, want the same truncated input", cos)
					}
					s := rerank(t, m,
						infer.Pair{Query: queryReset, Passage: aboutReset + " " + longer},
						infer.Pair{Query: longer, Passage: longer},
						infer.Pair{Query: queryReset, Passage: ""})
					if s[0] <= 0 {
						t.Errorf("relevant passage with a long tail scored %.3f", s[0])
					}
				})

				t.Run("empty and odd input", func(t *testing.T) {
					t.Parallel()
					embed(t, m, "", " ", "\xff\xfe not utf-8", "emoji 🙂 and 日本語")
					if out, err := m.Embedder().Run(t.Context(), nil); err != nil || len(out) != 0 {
						t.Errorf("Run(nil) = %v, %v", out, err)
					}
				})

				t.Run("concurrent runs", func(t *testing.T) {
					t.Parallel()
					texts := []string{askReset, askForgot, aboutFlour, words(40)}
					pairs, _ := mixedPairs()
					wantVecs, wantScores := embed(t, m, texts...), rerank(t, m, pairs...)

					var wg sync.WaitGroup
					for range 8 {
						wg.Go(func() {
							for range 4 {
								vecs, err := m.Embedder().Run(t.Context(), texts)
								if err != nil {
									t.Errorf("embed: %v", err)
									return
								}
								for i := range vecs {
									if cos := dot(vecs[i], wantVecs[i]); cos < 1-1e-5 {
										t.Errorf("text %d: cos = %.6f under concurrency", i, cos)
									}
								}
								scores, err := m.Reranker().Run(t.Context(), pairs)
								if err != nil {
									t.Errorf("rerank: %v", err)
									return
								}
								for i := range scores {
									if d := math.Abs(float64(scores[i] - wantScores[i])); d > 1e-3 {
										t.Errorf("pair %d: %.4f, want %.4f under concurrency", i, scores[i], wantScores[i])
									}
								}
							}
						})
					}
					wg.Wait()
				})

				t.Run("cancelled context", func(t *testing.T) {
					t.Parallel()
					ctx, cancel := context.WithCancel(t.Context())
					cancel()
					if _, err := m.Embedder().Run(ctx, []string{askReset}); !errors.Is(err, context.Canceled) {
						t.Errorf("embed with cancelled ctx: %v", err)
					}
				})

				t.Run("second open fails", func(t *testing.T) {
					t.Parallel()
					if second, err := Open(testConfig(t, tc.onnx)); err == nil {
						_ = second.Close()
						t.Error("a second Open succeeded while the first is live")
					}
					// The failed Open must not have torn down the live one.
					embed(t, m, askReset)
				})
			})

			if err := m.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if _, err := m.Embedder().Run(t.Context(), []string{askReset}); !errors.Is(err, core.ErrClosed) {
				t.Errorf("embed after Close: %v, want core.ErrClosed", err)
			}
			if _, err := m.Reranker().Run(t.Context(), []infer.Pair{{Query: queryReset, Passage: aboutReset}}); !errors.Is(err, core.ErrClosed) {
				t.Errorf("rerank after Close: %v, want core.ErrClosed", err)
			}
		})
	}
}

// TestMeanPooling checks the pooling all-MiniLM-L6-v2 needs. The bge weights
// are not trained for it, so this is about mechanics: the attention mask must
// keep batch padding out of the mean.
//
//nolint:paralleltest // one ONNX Runtime environment per process
func TestMeanPooling(t *testing.T) {
	cfg := testConfig(t, onnxFP32)
	cfg.Pooling = "mean"
	m := open(t, cfg)

	short, long := askReset, words(200)
	alone := embed(t, m, short)[0]
	padded := embed(t, m, long, short)[1] // short is padded to long's length
	if cos := dot(alone, padded); cos < 1-1e-5 {
		t.Errorf("cos(alone, padded) = %.6f: padding leaked into the mean", cos)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	cfg.Pooling = ""
	cls := embed(t, open(t, cfg), short)[0]
	if cos := dot(alone, cls); cos > 1-1e-3 {
		t.Errorf("cos(mean, cls) = %.6f: Pooling had no effect", cos)
	}
}

//nolint:paralleltest // one ONNX Runtime environment per process
func TestOpenErrors(t *testing.T) {
	good := testConfig(t, onnxFP32)
	for name, mutate := range map[string]func(*Config){
		"unknown pooling":  func(c *Config) { c.Pooling = "max" },
		"missing library":  func(c *Config) { c.OnnxLibPath = filepath.Join(t.TempDir(), "libonnxruntime.so") },
		"missing embedder": func(c *Config) { c.EmbedderDir = t.TempDir() },
		"missing reranker": func(c *Config) { c.RerankerDir = t.TempDir() },
		"missing file":     func(c *Config) { c.OnnxFilename = "nope.onnx" },
		// The reranker's [batch, 1] output is not token embeddings.
		"swapped models": func(c *Config) { c.EmbedderDir, c.RerankerDir = c.RerankerDir, c.EmbedderDir },
	} {
		t.Run(name, func(t *testing.T) {
			cwd, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			cfg := good
			mutate(&cfg)
			if m, err := Open(cfg); err == nil {
				_ = m.Close()
				t.Fatal("Open succeeded")
			}
			if after, _ := os.Getwd(); after != cwd {
				t.Errorf("working directory left at %s", after)
			}
			// A failed Open must release the environment for the next one.
			open(t, good)
		})
	}
}

func TestParsePooling(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]pooling{"": poolCLS, "cls": poolCLS, "mean": poolMean} {
		if got, err := parsePooling(in); err != nil || got != want {
			t.Errorf("parsePooling(%q) = %v, %v", in, got, err)
		}
	}
	if _, err := parsePooling("CLS"); err == nil {
		t.Error(`parsePooling("CLS") succeeded`)
	}
}

func TestMeanPool(t *testing.T) {
	t.Parallel()
	// Three positions of width 2; the last is padding and must be ignored.
	tokens := []float32{1, 2, 3, 6, 100, 100}
	v := make([]float32, 2)
	meanPool(v, tokens, []uint32{1, 1, 0})
	if v[0] != 2 || v[1] != 4 {
		t.Errorf("meanPool = %v, want [2 4]", v)
	}
}

func TestNormalize(t *testing.T) {
	t.Parallel()
	v := []float32{3, 4}
	normalize(v)
	if math.Abs(float64(v[0])-0.6) > 1e-6 || math.Abs(float64(v[1])-0.8) > 1e-6 {
		t.Errorf("normalize = %v, want [0.6 0.8]", v)
	}
	zero := []float32{0, 0}
	normalize(zero) // must not divide by zero
	if zero[0] != 0 || zero[1] != 0 {
		t.Errorf("normalize(0) = %v", zero)
	}
}

// BenchmarkEmbed measures one text per call, the critical-path query
// embedding. 128 tokens is the bound the recall query is truncated to.
func BenchmarkEmbed(b *testing.B) {
	for _, onnx := range []string{onnxFP32, onnxInt8} {
		for _, tokens := range []int{16, 128} {
			b.Run(fmt.Sprintf("%s/tokens=%d", onnx, tokens), func(b *testing.B) {
				// open's Cleanup closes m when this sub-benchmark ends,
				// which frees the environment for the next one.
				m := open(b, testConfig(b, onnx))
				text := []string{words(tokens)}
				for b.Loop() {
					if _, err := m.Embedder().Run(b.Context(), text); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// BenchmarkRerank20 measures one shortlist of 20 pairs per call. 256 tokens
// per pair is the bound rerank passages are truncated to.
func BenchmarkRerank20(b *testing.B) {
	for _, onnx := range []string{onnxFP32, onnxInt8} {
		for _, tokens := range []int{64, 256} {
			b.Run(fmt.Sprintf("%s/tokens=%d", onnx, tokens), func(b *testing.B) {
				m := open(b, testConfig(b, onnx))
				pairs := make([]infer.Pair, 20)
				for i := range pairs {
					pairs[i] = infer.Pair{Query: words(16), Passage: words(tokens - 16)}
				}
				for b.Loop() {
					if _, err := m.Reranker().Run(b.Context(), pairs); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
