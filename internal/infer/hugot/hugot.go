//go:build cgo && ORT

package hugot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"

	upstream "github.com/knights-analytics/hugot"
	"github.com/knights-analytics/hugot/backends"
	"github.com/knights-analytics/hugot/options"
	ort "github.com/yalue/onnxruntime_go"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/infer"
)

// maxSeqLen caps tokens per input. Both default models take 512 positions;
// RoBERTa-family configs report 514 but two of those are reserved, so 512 is
// also the most they accept.
const maxSeqLen = 512

// Config says where the native library and the two models live.
type Config struct {
	// OnnxLibPath is the path to libonnxruntime.so / .dylib, or the directory
	// holding it. Empty means Hugot's default (/usr/lib on Linux,
	// /usr/local/lib on macOS).
	OnnxLibPath string
	// EmbedderDir and RerankerDir each hold an ONNX file, tokenizer.json,
	// config.json and special_tokens_map.json.
	EmbedderDir string
	RerankerDir string
	// OnnxFilename picks the file inside each directory, e.g.
	// "model_int8.onnx". Empty only works when a directory holds exactly one
	// .onnx file.
	OnnxFilename string
	// Pooling is "cls" (bge, the default) or "mean" (all-MiniLM-L6-v2).
	Pooling string
	// IntraOpThreads and InterOpThreads default to 1 each: throughput comes
	// from K batcher workers calling Run concurrently, not from ONNX Runtime
	// fanning one call out.
	IntraOpThreads, InterOpThreads int
}

// Models is the one ONNX Runtime session of the process with both models
// loaded into it.
type Models struct {
	// mu guards Close against Runs in flight, not Runs against each other:
	// Run takes the read side, so K workers still run concurrently. Without
	// it a Run racing shutdown would use freed native memory.
	mu       sync.RWMutex
	session  *upstream.Session // nil once closed
	embedder model
	reranker model
	pooling  pooling
}

// model is one loaded ONNX graph and its tokenizer. Nothing in it is written
// after Open.
type model struct {
	hf  *backends.Model
	out int // index of the output tensor we read
	// sep is the token every sequence must end with; truncation cuts it off
	// and forward puts it back.
	sep    uint32
	hasSep bool
}

// Open starts ONNX Runtime and loads both models. Only one Models can be open
// in a process at a time; a second Open fails until the first is closed.
func Open(cfg Config) (_ *Models, err error) {
	pool, err := parsePooling(cfg.Pooling)
	if err != nil {
		return nil, err
	}
	embedderDir, err := filepath.Abs(cfg.EmbedderDir)
	if err != nil {
		return nil, fmt.Errorf("hugot: embedder dir: %w", err)
	}
	rerankerDir, err := filepath.Abs(cfg.RerankerDir)
	if err != nil {
		return nil, fmt.Errorf("hugot: reranker dir: %w", err)
	}

	// Hugot chdirs into a model directory to load it and only changes back
	// on success.
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("hugot: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.Chdir(cwd))
		}
	}()

	opts := []options.WithOption{
		options.WithIntraOpNumThreads(max(cfg.IntraOpThreads, 1)),
		options.WithInterOpNumThreads(max(cfg.InterOpThreads, 1)),
	}
	if cfg.OnnxLibPath != "" {
		opts = append(opts, withLibrary(cfg.OnnxLibPath))
	}
	session, err := upstream.NewORTSession(opts...)
	if err != nil {
		return nil, fmt.Errorf("hugot: start onnxruntime: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, session.Destroy())
		}
	}()

	// The Hugot pipelines are created for their loading and validation; Run
	// goes to the models underneath them (see the package comment).
	fe, err := upstream.NewPipeline(session, upstream.FeatureExtractionConfig{
		ModelPath:    embedderDir,
		Name:         "embedder",
		OnnxFilename: cfg.OnnxFilename,
	})
	if err != nil {
		return nil, fmt.Errorf("hugot: load embedder from %s: %w", embedderDir, err)
	}
	ce, err := upstream.NewPipeline(session, upstream.CrossEncoderConfig{
		ModelPath:    rerankerDir,
		Name:         "reranker",
		OnnxFilename: cfg.OnnxFilename,
	})
	if err != nil {
		return nil, fmt.Errorf("hugot: load reranker from %s: %w", rerankerDir, err)
	}

	m := &Models{session: session, pooling: pool}
	// The embedder reads token embeddings, [batch, seq, dim].
	if m.embedder, err = newModel(fe.Model, 3, core.VectorDim); err != nil {
		return nil, fmt.Errorf("hugot: embedder: %w", err)
	}
	// The reranker reads one logit per pair, [batch, 1].
	if m.reranker, err = newModel(ce.Model, 2, 1); err != nil {
		return nil, fmt.Errorf("hugot: reranker: %w", err)
	}
	return m, nil
}

// Embedder returns the bi-encoder: one unit-length core.VectorDim vector per
// text.
func (m *Models) Embedder() infer.Pipeline[string, []float32] { return embedder{m} }

// Reranker returns the cross-encoder: one raw relevance logit per pair.
func (m *Models) Reranker() infer.Pipeline[infer.Pair, float32] { return reranker{m} }

// Close waits for Runs in flight, then frees both models and the ONNX Runtime
// environment. Later Runs return core.ErrClosed. Closing twice is a no-op.
func (m *Models) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.session == nil {
		return nil
	}
	err := m.session.Destroy()
	m.session = nil
	if err != nil {
		return fmt.Errorf("hugot: close: %w", err)
	}
	return nil
}

// chunks calls fn for consecutive ranges of at most infer.MaxBatch inputs, so
// a caller that bypasses the batcher cannot build an unbounded tensor.
func (m *Models) chunks(ctx context.Context, n int, fn func(lo, hi int) error) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.session == nil {
		return core.ErrClosed
	}
	for lo := 0; lo < n; lo += infer.MaxBatch {
		// A model pass cannot be interrupted; at least do not start one
		// nobody is waiting for.
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("before model pass: %w", err)
		}
		if err := fn(lo, min(lo+infer.MaxBatch, n)); err != nil {
			return err
		}
	}
	return nil
}

// withLibrary points ONNX Runtime at path. Hugot's own option only takes a
// directory and appends the platform's default file name.
func withLibrary(path string) options.WithOption {
	return func(o *options.Options) error {
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("onnxruntime library: %w", err)
		}
		if info.IsDir() {
			return options.WithOnnxLibraryPath(path)(o)
		}
		dir := filepath.Dir(path)
		o.ORTOptions.LibraryPath = &path
		o.ORTOptions.LibraryDir = &dir
		return nil
	}
}

// newModel checks at startup everything forward relies on, so a bad export
// fails Open instead of the first turn.
func newModel(hf *backends.Model, rank int, width int64) (model, error) {
	names := backends.GetNames(hf.InputsMeta)
	for _, want := range []string{"input_ids", "attention_mask"} {
		if !slices.Contains(names, want) {
			return model{}, fmt.Errorf("model has no %s input (inputs: %v)", want, names)
		}
	}
	out := slices.IndexFunc(hf.OutputsMeta, func(o backends.InputOutputInfo) bool {
		last := len(o.Dimensions) - 1
		// -1 is a dynamic axis; forward checks the real shape.
		return last == rank-1 && (o.Dimensions[last] == width || o.Dimensions[last] == -1)
	})
	if out < 0 {
		return model{}, fmt.Errorf("no rank-%d output of width %d (outputs: %v)", rank, width, hf.OutputsMeta)
	}
	tk := hf.Tokenizer
	if tk == nil || tk.RustTokenizer == nil {
		return model{}, errors.New("tokenizer.json is missing")
	}
	// Hugot truncates to max_position_embeddings and does not truncate at all
	// without it; ONNX Runtime then rejects a long input.
	if tk.MaxAllowedTokens <= 0 {
		return model{}, errors.New("config.json is missing or has no max_position_embeddings")
	}
	tk.MaxAllowedTokens = min(tk.MaxAllowedTokens, maxSeqLen)

	m := model{hf: hf, out: out}
	if hf.SeparatorToken != "" {
		if ids, _ := tk.RustTokenizer.Tokenizer.Encode(hf.SeparatorToken, false); len(ids) == 1 {
			m.sep, m.hasSep = ids[0], true
		}
	}
	return m, nil
}

// forward runs one tokenized batch through the graph and hands the output
// tensor, still flat, to read. The data is only valid during read.
//
// It is safe for concurrent use: the batch is the caller's own, the model is
// read-only, and ONNX Runtime allows concurrent Run calls on one session.
func (m *model) forward(batch *backends.PipelineBatch, read func(data []float32, shape []int64) error) (err error) {
	limit := m.hf.Tokenizer.MaxAllowedTokens
	for i := range batch.Input {
		in := &batch.Input[i]
		if len(in.TokenIDs) == 0 {
			return fmt.Errorf("tokenizer produced no tokens for input %d", i)
		}
		// Hugot truncates by slicing, which drops the closing separator the
		// model was trained to see. Put it back in the last position.
		if last := len(in.TokenIDs) - 1; m.hasSep && last == limit-1 && in.TokenIDs[last] != m.sep {
			in.TokenIDs[last] = m.sep
		}
	}

	if err := backends.CreateInputTensors(batch, m.hf, "ORT"); err != nil {
		return fmt.Errorf("input tensors: %w", err)
	}
	defer func() {
		if dErr := batch.Destroy(); dErr != nil {
			err = errors.Join(err, fmt.Errorf("free input tensors: %w", dErr))
		}
	}()
	inputs, ok := batch.InputValues.([]ort.Value)
	if !ok {
		return fmt.Errorf("input tensors are %T, want []ort.Value", batch.InputValues)
	}

	// Nil outputs are allocated by ONNX Runtime and are ours to destroy.
	outputs := make([]ort.Value, len(m.hf.OutputsMeta))
	defer func() {
		for _, o := range outputs {
			if o == nil {
				continue
			}
			if dErr := o.Destroy(); dErr != nil {
				err = errors.Join(err, fmt.Errorf("free output tensor: %w", dErr))
			}
		}
	}()
	if err := m.hf.ORTModel.Session.Run(inputs, outputs); err != nil {
		return fmt.Errorf("onnxruntime: %w", err)
	}
	t, ok := outputs[m.out].(*ort.Tensor[float32])
	if !ok {
		return fmt.Errorf("output %q is %T, want a float32 tensor", m.hf.OutputsMeta[m.out].Name, outputs[m.out])
	}
	return read(t.GetData(), t.GetShape())
}
