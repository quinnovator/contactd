# /// script
# requires-python = "==3.12.*"
# dependencies = [
#   "huggingface-hub==0.36.2",
#   "numpy==2.5.4",
#   "onnx==1.23.2",
#   "onnxruntime==1.24.1",
#   "optimum[onnxruntime]==2.1.0",
#   "tokenizers==0.22.2",
#   "torch==2.14.1",
#   "transformers==4.57.6",
# ]
#
# # PyPI's linux/amd64 torch wheel drags in the CUDA stack; the export only
# # needs CPU.
# [tool.uv.sources]
# torch = [{ index = "pytorch-cpu", marker = "sys_platform == 'linux'" }]
#
# [[tool.uv.index]]
# name = "pytorch-cpu"
# url = "https://download.pytorch.org/whl/cpu"
# explicit = true
# ///
"""Download, export and quantize the embedder and the reranker.

    uv run scripts/export_onnx.py --out models/

Writes one directory per model, laid out the way Hugot loads it:

    models/embedder/   BAAI/bge-small-en-v1.5               (384-d, CLS pooling)
    models/reranker/   cross-encoder/ms-marco-MiniLM-L6-v2  (one logit per pair)

each holding model.onnx (fp32), model_int8.onnx (dynamic int8, see
quantize.py), tokenizer.json, config.json (max_position_embeddings bounds
truncation) and special_tokens_map.json (the cross-encoder's separator).

The embedder is exported as a plain transformers model, so its output is
last_hidden_state and pooling is done by the Go adapter: CLS for bge, mean for
all-MiniLM-L6-v2 (`--embedder sentence-transformers/all-MiniLM-L6-v2`, then
set Pooling to "mean").

--prebuilt skips the torch export and copies the onnx/model.onnx each Hub repo
already publishes; quantization still runs locally.
"""

from __future__ import annotations

import argparse
import json
import shutil
import sys
from dataclasses import dataclass
from pathlib import Path

from quantize import FP32_NAME, INT8_NAME, quantize_int8

# Must match core.VectorDim.
VECTOR_DIM = 384

# What Hugot reads besides the .onnx file. vocab.txt is not needed by the Rust
# tokenizer but keeps the directory loadable by transformers.
SIDE_FILES = (
    "config.json",
    "tokenizer.json",
    "tokenizer_config.json",
    "special_tokens_map.json",
    "vocab.txt",
)
STAMP = "source.json"


@dataclass(frozen=True)
class Spec:
    role: str
    repo: str
    revision: str  # a commit sha, so a re-run cannot silently change weights
    task: str


DEFAULTS = {
    "embedder": Spec(
        "embedder",
        "BAAI/bge-small-en-v1.5",
        "5c38ec7c405ec4b44b94cc5a9bb96e735b38267a",
        "feature-extraction",
    ),
    "reranker": Spec(
        "reranker",
        "cross-encoder/ms-marco-MiniLM-L6-v2",
        "233902d25c440f23af6f7d6e94d2946bac0bee0a",
        "text-classification",
    ),
}


def parse_override(role: str, value: str | None) -> Spec:
    """Turn `repo` or `repo@revision` into a Spec; None keeps the pinned default."""
    spec = DEFAULTS[role]
    if value is None:
        return spec
    repo, _, revision = value.partition("@")
    return Spec(role, repo, revision or "main", spec.task)


def export_with_optimum(spec: Spec, out: Path) -> None:
    from optimum.exporters.onnx import main_export

    main_export(
        model_name_or_path=spec.repo,
        output=out,
        task=spec.task,
        revision=spec.revision,
        # Both repos are tagged sentence-transformers; that exporter would bake
        # pooling into the graph. We want the bare encoder.
        library_name="transformers",
        device="cpu",
    )


def fetch_prebuilt(spec: Spec, out: Path) -> None:
    from huggingface_hub import hf_hub_download

    for name in (f"onnx/{FP32_NAME}", *SIDE_FILES):
        cached = hf_hub_download(spec.repo, name, revision=spec.revision)
        shutil.copyfile(cached, out / Path(name).name)


def smoke_test(spec: Spec, out: Path) -> None:
    """Run both files once and fail if the fp32 model is not what the adapter expects."""
    import numpy as np
    import onnxruntime as ort
    from tokenizers import Tokenizer

    tokenizer = Tokenizer.from_file(str(out / "tokenizer.json"))
    tokenizer.no_padding()
    if spec.role == "embedder":
        encoding = tokenizer.encode("how do I reset my password?")
    else:
        encoding = tokenizer.encode(
            "how do I reset my password?",
            "Open Settings, choose Security, then click Reset password.",
        )

    def run(name: str) -> np.ndarray:
        session = ort.InferenceSession(str(out / name), providers=["CPUExecutionProvider"])
        feeds = {
            "input_ids": [encoding.ids],
            "attention_mask": [encoding.attention_mask],
            "token_type_ids": [encoding.type_ids],
        }
        wanted = {i.name for i in session.get_inputs()}
        feeds = {k: np.asarray(v, dtype=np.int64) for k, v in feeds.items() if k in wanted}
        return session.run(None, feeds)[0]

    fp32, int8 = run(FP32_NAME), run(INT8_NAME)
    if spec.role == "embedder":
        if fp32.ndim != 3 or fp32.shape[-1] != VECTOR_DIM:
            raise SystemExit(
                f"export: {spec.repo} output has shape {fp32.shape}, want [batch, seq, {VECTOR_DIM}]"
            )
        a, b = fp32[0, 0], int8[0, 0]  # CLS vectors
        cosine = float(a @ b / (np.linalg.norm(a) * np.linalg.norm(b)))
        print(f"export: {spec.role}: fp32 vs int8 CLS cosine {cosine:.4f}")
    else:
        if fp32.shape != (1, 1):
            raise SystemExit(f"export: {spec.repo} output has shape {fp32.shape}, want [batch, 1]")
        print(
            f"export: {spec.role}: logit fp32 {float(fp32[0, 0]):+.3f}, int8 {float(int8[0, 0]):+.3f}"
        )


def build(spec: Spec, root: Path, *, prebuilt: bool, force: bool) -> None:
    out = root / spec.role
    stamp = {
        "repo": spec.repo,
        "revision": spec.revision,
        "source": "prebuilt" if prebuilt else "optimum",
    }
    stamp_path = out / STAMP
    done = (out / FP32_NAME).is_file() and (out / INT8_NAME).is_file() and stamp_path.is_file()
    if done and not force and json.loads(stamp_path.read_text()) == stamp:
        print(f"export: {out} up to date ({spec.repo}@{spec.revision[:12]})")
        return

    # Start clean: Hugot walks the directory for .onnx files, so a leftover
    # from another model must not survive.
    shutil.rmtree(out, ignore_errors=True)
    out.mkdir(parents=True)
    print(f"export: {spec.repo}@{spec.revision[:12]} -> {out}")
    if prebuilt:
        fetch_prebuilt(spec, out)
    else:
        export_with_optimum(spec, out)
    quantize_int8(out / FP32_NAME, out / INT8_NAME)
    smoke_test(spec, out)
    # Written last, so an interrupted run is redone.
    stamp_path.write_text(json.dumps(stamp, indent=2) + "\n")
    for name in (FP32_NAME, INT8_NAME):
        print(f"export:   {name} {(out / name).stat().st_size / 1e6:.1f} MB")


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--out", type=Path, default=Path("models"), help="output root")
    parser.add_argument("--embedder", metavar="REPO[@REV]", help="override the embedder repo")
    parser.add_argument("--reranker", metavar="REPO[@REV]", help="override the reranker repo")
    parser.add_argument(
        "--prebuilt",
        action="store_true",
        help="copy the Hub repo's onnx/model.onnx instead of exporting with torch",
    )
    parser.add_argument("--force", action="store_true", help="rebuild even if up to date")
    args = parser.parse_args(argv)

    for spec in (
        parse_override("embedder", args.embedder),
        parse_override("reranker", args.reranker),
    ):
        build(spec, args.out, prebuilt=args.prebuilt, force=args.force)
    return 0


if __name__ == "__main__":
    sys.exit(main())
