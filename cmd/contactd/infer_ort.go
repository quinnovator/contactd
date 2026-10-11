//go:build cgo && ORT

package main

import (
	"fmt"
	"path/filepath"

	"github.com/quinnovator/contactd/internal/config"
	"github.com/quinnovator/contactd/internal/infer"
	"github.com/quinnovator/contactd/internal/infer/hugot"
)

// openModels loads the embedder and the reranker into ONNX Runtime.
func openModels(cfg *config.Config) (infer.Pipeline[string, []float32], infer.Pipeline[infer.Pair, float32], func() error, error) {
	m, err := hugot.Open(hugot.Config{
		OnnxLibPath:  cfg.OnnxLib,
		EmbedderDir:  filepath.Join(cfg.ModelsDir, "embedder"),
		RerankerDir:  filepath.Join(cfg.ModelsDir, "reranker"),
		OnnxFilename: cfg.OnnxFile(),
		Pooling:      cfg.Pooling,
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open ONNX models: %w", err)
	}
	return m.Embedder(), m.Reranker(), m.Close, nil
}
