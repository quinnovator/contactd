//go:build cgo && ORT

package main

import (
	"fmt"
	"path/filepath"

	"github.com/quinnovator/contactd/internal/infer"
	"github.com/quinnovator/contactd/internal/infer/hugot"
)

func openModels(o *options) (infer.Pipeline[string, []float32], infer.Pipeline[infer.Pair, float32], func(), error) {
	file := "model.onnx"
	if o.variant == "int8" {
		file = "model_int8.onnx"
	}
	m, err := hugot.Open(hugot.Config{
		OnnxLibPath:  o.onnxLib,
		EmbedderDir:  filepath.Join(o.modelsDir, "embedder"),
		RerankerDir:  filepath.Join(o.modelsDir, "reranker"),
		OnnxFilename: file,
		Pooling:      o.pooling,
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open ONNX models: %w", err)
	}
	// A close error at exit has no one to act on it.
	return m.Embedder(), m.Reranker(), func() { _ = m.Close() }, nil
}
