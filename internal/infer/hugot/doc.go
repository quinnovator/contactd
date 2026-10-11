// Package hugot runs the embedder and the cross-encoder reranker on ONNX
// Runtime through Hugot, and exposes each as an infer.Pipeline for the
// micro-batchers.
//
// Everything but this file is behind the build tags "cgo && ORT" (ORT is
// Hugot's own tag for its ONNX Runtime backend), so an untagged build sees an
// empty package and needs no native libraries. A tagged build links
// libtokenizers.a statically and dlopens libonnxruntime at Open; both are
// fetched at pinned versions by scripts/fetch-native-deps.sh, and the model
// directories are produced by scripts/export_onnx.py.
//
// Hugot owns the ONNX Runtime environment, model loading, the Rust tokenizer
// and input tensors. The forward pass and what follows it are done here,
// because Hugot's own pipelines do not fit: its feature-extraction pipeline
// can only mean-pool (bge wants the CLS vector), its cross-encoder pipeline
// returns sigmoid probabilities for one query at a time (recall thresholds
// are calibrated on logits, and a batch mixes queries), neither destroys its
// output tensors, and the cross-encoder updates shared state without
// synchronisation on every call.
package hugot
