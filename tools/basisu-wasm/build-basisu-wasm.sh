#!/usr/bin/env bash
# build-basisu-wasm.sh - builds ktx2_transcoder.wasm (wasm32-wasip1 reactor)
# from pinned, checksummed sources: Binomial's basis_universal v2.50 (its
# transcoder and the zstd decoder it ships) and wasi-sdk 34. The result is
# the transcoder embedded in internal/basisu.
#
# Needs bash, curl, tar and sha256sum on x86_64 Linux (WSL Ubuntu works);
# downloads about 300 MB once into <workdir>.
#
# Usage: build-basisu-wasm.sh <workdir> <outdir>
# The wrapper ktx2_wasi.cc must sit next to this script. The build is
# reproducible: the output must equal WASM_SHA below, the checksum of
# internal/basisu/ktx2_transcoder.wasm. After a change to the wrapper or the
# pinned versions, copy the new file there and update WASM_SHA.
set -euo pipefail

WORK=${1:?usage: build-basisu-wasm.sh <workdir> <outdir>}
OUT=${2:?usage: build-basisu-wasm.sh <workdir> <outdir>}
HERE=$(cd "$(dirname "$0")" && pwd)

WASI_VER=34
WASI_TAR=wasi-sdk-${WASI_VER}.0-x86_64-linux.tar.gz
WASI_URL=https://github.com/WebAssembly/wasi-sdk/releases/download/wasi-sdk-${WASI_VER}/${WASI_TAR}
WASI_SHA=b761e3a0721dbae9c09a0059e5fdb2bf917d1b4a8a7b430fb3b5aafb0984b2c4
BASISU_VER=2_50
BASISU_TAR=basis_universal-${BASISU_VER}.tar.gz
BASISU_URL=https://github.com/BinomialLLC/basis_universal/archive/refs/tags/v${BASISU_VER}.tar.gz
BASISU_SHA=216e49e1f4213d4bfa4afaa07527e16bac28533dddd444197d3aa19230ac130c
WASM_SHA=49dc2587db3cd432f6483491fc92ae11bfe6537aedbbc3b76a51a30ee95bb07d

mkdir -p "$WORK" "$OUT"
OUT=$(cd "$OUT" && pwd)
cd "$WORK"
WORK=$(pwd)
fetch() { # url file sha
  [ -f "$2" ] || curl -fsSL -o "$2" "$1"
  echo "$3  $2" | sha256sum -c -
}
fetch "$WASI_URL" "$WASI_TAR" "$WASI_SHA"
fetch "$BASISU_URL" "$BASISU_TAR" "$BASISU_SHA"
[ -d wasi-sdk-${WASI_VER}.0-x86_64-linux ] || tar xzf "$WASI_TAR"
# Only the transcoder, the bundled zstd and the licence are needed.
[ -d basis_universal-${BASISU_VER} ] || tar xzf "$BASISU_TAR" \
  basis_universal-${BASISU_VER}/transcoder basis_universal-${BASISU_VER}/zstd \
  basis_universal-${BASISU_VER}/LICENSE
SDK=$WORK/wasi-sdk-${WASI_VER}.0-x86_64-linux
SRC=$WORK/basis_universal-${BASISU_VER}

# KTX2 with zstd supercompression (UASTC files); every transcoder target
# format stays compiled in, and the link keeps only the RGBA32 path.
TARGET="--sysroot=$SDK/share/wasi-sysroot --target=wasm32-wasip1"
DEFS="-DNDEBUG -DBASISD_SUPPORT_KTX2=1 -DBASISD_SUPPORT_KTX2_ZSTD=1"
CXXFLAGS="-O2 -flto -fno-exceptions -fno-rtti $DEFS -I$SRC/transcoder -I$SRC/zstd"
OBJ=$WORK/obj
rm -rf "$OBJ" && mkdir -p "$OBJ"
"$SDK/bin/clang++" $TARGET $CXXFLAGS -c "$SRC/transcoder/basisu_transcoder.cpp" -o "$OBJ/transcoder.o"
"$SDK/bin/clang" $TARGET -O2 -flto -DNDEBUG -c "$SRC/zstd/zstddeclib.c" -o "$OBJ/zstd.o"
"$SDK/bin/clang++" $TARGET $CXXFLAGS -c "$HERE/ktx2_wasi.cc" -o "$OBJ/ktx2_wasi.o"

"$SDK/bin/clang++" $TARGET $CXXFLAGS -mexec-model=reactor -Wl,--strip-all -Wl,--gc-sections \
  -o "$OUT/ktx2_transcoder.wasm" "$OBJ"/*.o
ls -l "$OUT/ktx2_transcoder.wasm"
if echo "$WASM_SHA  $OUT/ktx2_transcoder.wasm" | sha256sum -c -; then
  echo "identical to internal/basisu/ktx2_transcoder.wasm"
else
  echo "differs from internal/basisu/ktx2_transcoder.wasm (sha256 $WASM_SHA)" >&2
  exit 1
fi
