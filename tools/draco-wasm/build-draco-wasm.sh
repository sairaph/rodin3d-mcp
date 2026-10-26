#!/usr/bin/env bash
# build-draco-wasm.sh - builds draco_decoder.wasm (wasm32-wasip1 reactor) from
# pinned, checksummed sources: Google Draco 1.5.7 and wasi-sdk 34. The result
# is the decoder embedded in internal/draco.
#
# Needs bash, curl, tar, sha256sum and python3 on x86_64 Linux (WSL Ubuntu
# works); downloads about 250 MB once into <workdir>.
#
# Usage: build-draco-wasm.sh <workdir> <outdir>
# The wrapper draco_wasi.cc must sit next to this script. The build is
# reproducible: the output must equal WASM_SHA below, the checksum of
# internal/draco/draco_decoder.wasm. After a change to the wrapper or the
# pinned versions, copy the new file there and update WASM_SHA.
set -euo pipefail

WORK=${1:?usage: build-draco-wasm.sh <workdir> <outdir>}
OUT=${2:?usage: build-draco-wasm.sh <workdir> <outdir>}
HERE=$(cd "$(dirname "$0")" && pwd)

WASI_VER=34
WASI_TAR=wasi-sdk-${WASI_VER}.0-x86_64-linux.tar.gz
WASI_URL=https://github.com/WebAssembly/wasi-sdk/releases/download/wasi-sdk-${WASI_VER}/${WASI_TAR}
WASI_SHA=b761e3a0721dbae9c09a0059e5fdb2bf917d1b4a8a7b430fb3b5aafb0984b2c4
DRACO_VER=1.5.7
DRACO_TAR=draco-${DRACO_VER}.tar.gz
DRACO_URL=https://github.com/google/draco/archive/refs/tags/${DRACO_VER}.tar.gz
DRACO_SHA=bf6b105b79223eab2b86795363dfe5e5356050006a96521477973aba8f036fe1
WASM_SHA=6008ae742ef8b033078004212182601ac36a15632e84a1e302986340d0c7429e

mkdir -p "$WORK" "$OUT"
OUT=$(cd "$OUT" && pwd)
cd "$WORK"
WORK=$(pwd)
fetch() { # url file sha
  [ -f "$2" ] || curl -fsSL -o "$2" "$1"
  echo "$3  $2" | sha256sum -c -
}
fetch "$WASI_URL" "$WASI_TAR" "$WASI_SHA"
fetch "$DRACO_URL" "$DRACO_TAR" "$DRACO_SHA"
[ -d wasi-sdk-${WASI_VER}.0-x86_64-linux ] || tar xzf "$WASI_TAR"
[ -d draco-${DRACO_VER} ] || tar xzf "$DRACO_TAR"
SDK=$WORK/wasi-sdk-${WASI_VER}.0-x86_64-linux
SRC=$WORK/draco-${DRACO_VER}

# draco_features.h as Draco's CMake writes it for a full decoder.
GEN=$WORK/gen/draco
mkdir -p "$GEN"
cat > "$GEN/draco_features.h" <<'EOF'
#ifndef DRACO_FEATURES_H_
#define DRACO_FEATURES_H_
#define DRACO_MESH_COMPRESSION_SUPPORTED
#define DRACO_NORMAL_ENCODING_SUPPORTED
#define DRACO_STANDARD_EDGEBREAKER_SUPPORTED
#define DRACO_PREDICTIVE_EDGEBREAKER_SUPPORTED
#define DRACO_POINT_CLOUD_COMPRESSION_SUPPORTED
#define DRACO_BACKWARDS_COMPATIBILITY_SUPPORTED
#define DRACO_ATTRIBUTE_INDICES_DEDUPLICATION_SUPPORTED
#define DRACO_ATTRIBUTE_VALUES_DEDUPLICATION_SUPPORTED
#endif
EOF

# The decoder source set: the same CMake lists Draco's own Emscripten decoder
# uses, minus the Emscripten glue.
python3 - "$SRC/CMakeLists.txt" > "$WORK/gen/sources.txt" <<'EOF'
import re, sys
text = open(sys.argv[1]).read()
want = """draco_attributes_sources draco_compression_attributes_dec_sources
draco_compression_attributes_pred_schemes_dec_sources draco_compression_bit_coders_sources
draco_compression_decode_sources draco_compression_entropy_sources
draco_compression_mesh_traverser_sources draco_compression_mesh_dec_sources
draco_compression_options_sources draco_compression_point_cloud_dec_sources
draco_core_sources draco_dec_config_sources draco_mesh_sources draco_metadata_dec_sources
draco_metadata_sources draco_point_cloud_sources draco_points_dec_sources""".split()
out = []
for v in want:
    blocks = re.findall(r'list\(\s*APPEND\s+' + v + r'\s(.*?)\)', text, re.S)
    if not blocks:
        sys.exit('missing list ' + v)
    for b in blocks:
        out += [f for f in re.findall(r'"\$\{draco_src_root\}/([^"]+\.cc)"', b)]
for f in sorted(set(out)):
    print(f)
EOF
echo "sources: $(wc -l < "$WORK/gen/sources.txt")"

CXX="$SDK/bin/clang++ --sysroot=$SDK/share/wasi-sysroot --target=wasm32-wasip1"
FLAGS="-O2 -flto -fno-exceptions -fno-rtti -DNDEBUG -I$SRC/src -I$WORK/gen"
OBJ=$WORK/obj
rm -rf "$OBJ" && mkdir -p "$OBJ"
pids=()
while read -r f; do
  o=$OBJ/$(echo "$f" | tr / _).o
  $CXX $FLAGS -c "$SRC/src/draco/$f" -o "$o" &
  pids+=($!)
  if [ ${#pids[@]} -ge "$(nproc)" ]; then wait "${pids[0]}"; pids=("${pids[@]:1}"); fi
done < "$WORK/gen/sources.txt"
for p in "${pids[@]}"; do wait "$p"; done
$CXX $FLAGS -c "$HERE/draco_wasi.cc" -o "$OBJ/draco_wasi.o"

$CXX $FLAGS -mexec-model=reactor -Wl,--strip-all -Wl,--gc-sections \
  -o "$OUT/draco_decoder.wasm" "$OBJ"/*.o
ls -l "$OUT/draco_decoder.wasm"
if echo "$WASM_SHA  $OUT/draco_decoder.wasm" | sha256sum -c -; then
  echo "identical to internal/draco/draco_decoder.wasm"
else
  echo "differs from internal/draco/draco_decoder.wasm (sha256 $WASM_SHA)" >&2
  exit 1
fi
