# syntax=docker/dockerfile:1
#
# One glibc-based distroless image holds the binary, libonnxruntime.so, the
# two quantized models and the tier-0 WASM runtimes, with a single volume at
# /data for SQLite and the tier-0 filesystems. Glibc rather than static or
# musl is required because ONNX Runtime ships as a shared library.
#
# Build from the repo root after `mise run models:fetch`:
#   docker build -f deploy/Dockerfile -t contactd .

FROM golang:1.26-bookworm AS build
RUN apt-get update \
    && apt-get install -y --no-install-recommends unzip \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /src
ENV CGO_ENABLED=1 GOTOOLCHAIN=local

# The same scripts local builds run, so the image links identical, checksum-
# verified libraries. Copied first so the layer is reused until they change.
COPY scripts/fetch-native-deps.sh scripts/fetch-wasm-runtimes.sh scripts/
RUN ./scripts/fetch-native-deps.sh && ./scripts/fetch-wasm-runtimes.sh

COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_LDFLAGS="-L/src/third_party/lib" \
    go build -tags ORT -trimpath -ldflags='-s -w' -o /out/contactd ./cmd/contactd
# The volume's mount point, created here so it can be owned by the runtime user.
RUN mkdir /out/data

FROM gcr.io/distroless/cc-debian12:nonroot
COPY --from=build /src/third_party/lib/libonnxruntime.so /usr/lib/
COPY --from=build /src/third_party/wasm/ /wasm/
COPY --from=build /out/contactd /contactd
# bge-small int8 + ms-marco-MiniLM int8 ONNX (.dockerignore drops the fp32 files)
COPY models/ /models/
COPY --from=build --chown=nonroot:nonroot /out/data /data

ENV CONTACTD_DATA_DIR=/data \
    CONTACTD_MODELS_DIR=/models \
    CONTACTD_WASM_DIR=/wasm \
    CONTACTD_ONNX_LIB=/usr/lib/libonnxruntime.so
# GOMAXPROCS is left unset: Go 1.25+ reads the cgroup CPU limit itself.
# GOMEMLIMIT is NOT cgroup-aware. Set it at run time to the container memory
# limit minus room for ONNX Runtime's native memory, e.g. 4 GiB -> 3GiB.

# SQLite needs a real filesystem with working locks: a local volume, not an
# overlay or a network mount.
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/contactd"]
