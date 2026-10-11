# /// script
# requires-python = "==3.12.*"
# dependencies = [
#   "onnx==1.23.2",
#   "onnxruntime==1.24.1",
# ]
# ///
"""Int8 dynamic quantization of an exported ONNX model.

Dynamic quantization stores MatMul/Gemm weights as int8 and quantizes
activations on the fly, so it needs no calibration data. It is a candidate,
not a default: keep model_int8.onnx only if the recall benchmark shows no
loss against model.onnx (docs/DESIGN.md, Piece 3).

    uv run scripts/quantize.py models/embedder models/reranker
    uv run scripts/quantize.py path/to/model.onnx --out path/to/model_int8.onnx

onnxruntime is pinned to the version of the shared library contactd loads
(scripts/fetch-native-deps.sh), so the quantized graph only uses operators
that runtime has.
"""

from __future__ import annotations

import argparse
import sys
from pathlib import Path

FP32_NAME = "model.onnx"
INT8_NAME = "model_int8.onnx"


def quantize_int8(
    src: Path,
    dst: Path,
    *,
    per_channel: bool = False,
    reduce_range: bool = False,
) -> None:
    """Write an int8 dynamically quantized copy of src to dst.

    The defaults (signed 8-bit weights, per-tensor scales, full range) are
    what Optimum uses for arm64 and AVX512-VNNI. On x86 CPUs without VNNI
    (AVX2, plain AVX512) the u8*s8 multiply can saturate; pass
    reduce_range=True for those targets and re-run the recall benchmark.
    """
    # Imported here so `--help` and importing this module stay cheap.
    from onnxruntime.quantization import QuantType, quantize_dynamic

    dst.parent.mkdir(parents=True, exist_ok=True)
    tmp = dst.with_suffix(".tmp.onnx")
    try:
        quantize_dynamic(
            model_input=str(src),
            model_output=str(tmp),
            weight_type=QuantType.QInt8,
            per_channel=per_channel,
            reduce_range=reduce_range,
        )
        tmp.replace(dst)  # never leave a half-written model where Hugot looks
    finally:
        tmp.unlink(missing_ok=True)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument(
        "paths",
        nargs="+",
        type=Path,
        help=f"model directories holding {FP32_NAME}, or .onnx files",
    )
    parser.add_argument(
        "--out",
        type=Path,
        help=f"output file (single input only; default: {INT8_NAME} next to the input)",
    )
    parser.add_argument("--per-channel", action="store_true", help="per-channel weight scales")
    parser.add_argument(
        "--reduce-range",
        action="store_true",
        help="7-bit weights; avoids saturation on x86 without VNNI",
    )
    args = parser.parse_args(argv)
    if args.out and len(args.paths) != 1:
        parser.error("--out needs exactly one input")

    for path in args.paths:
        src = path / FP32_NAME if path.is_dir() else path
        if not src.is_file():
            print(f"quantize: {src} not found", file=sys.stderr)
            return 1
        dst = args.out or src.with_name(INT8_NAME)
        quantize_int8(src, dst, per_channel=args.per_channel, reduce_range=args.reduce_range)
        print(
            f"quantize: {src} ({src.stat().st_size / 1e6:.1f} MB)"
            f" -> {dst} ({dst.stat().st_size / 1e6:.1f} MB)"
        )
    return 0


if __name__ == "__main__":
    sys.exit(main())
