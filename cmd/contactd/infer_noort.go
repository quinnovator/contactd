//go:build !cgo || !ORT

package main

import (
	"errors"

	"github.com/quinnovator/contactd/internal/config"
	"github.com/quinnovator/contactd/internal/infer"
)

// openModels reports that this binary has no ONNX Runtime backend.
func openModels(*config.Config) (infer.Pipeline[string, []float32], infer.Pipeline[infer.Pair, float32], func() error, error) {
	return nil, nil, nil, errors.New("this binary was built without -tags ORT; rebuild with it or run with -infer=fake")
}
