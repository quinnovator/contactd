//go:build !cgo || !ORT

package main

import (
	"errors"

	"github.com/quinnovator/contactd/internal/infer"
)

func openModels(*options) (infer.Pipeline[string, []float32], infer.Pipeline[infer.Pair, float32], func(), error) {
	return nil, nil, nil, errors.New("-infer=ort needs a binary built with -tags ORT")
}
