// ktx2_wasi.cc - a small C ABI over the basis_universal KTX2 transcoder for
// wasm32-wasi. The host (Go, wazero) copies a KTX2 file into linear memory,
// calls kt_decode, reads the fixed-layout result, then calls kt_release.
// Level 0 of layer 0, face 0 is transcoded to RGBA8 (ETC1S/BasisLZ and
// UASTC, zstd supercompressed or not).
//
// Result layout (all little-endian uint32, offsets in bytes):
//   0 status        0 = ok, 1 = not a KTX2 file the transcoder reads,
//                   2 = transcode error, 3 = image too large
//   4 width
//   8 height
//  12 rgba_ptr      uint8[width*height*4], rows top to bottom (0 when not ok)
//  16 error_ptr     NUL-terminated message (0 when ok)
#include <cstdint>
#include <cstdlib>
#include <cstring>

#include "basisu_transcoder.h"

namespace {

struct Result {
  uint32_t status, width, height, rgba_ptr, error_ptr;
};

// maxPixels bounds the picture (8192 x 8192, 256 MiB as RGBA8), as the
// host bounds every decoded texture.
const uint64_t maxPixels = 8192ull * 8192ull;

uint32_t ptr32(const void* p) {
  return static_cast<uint32_t>(reinterpret_cast<uintptr_t>(p));
}

Result* fail(Result* r, uint32_t status, const char* msg) {
  r->status = status;
  size_t n = strlen(msg);
  char* m = static_cast<char*>(malloc(n + 1));
  memcpy(m, msg, n + 1);
  r->error_ptr = ptr32(m);
  return r;
}

}  // namespace

extern "C" {

__attribute__((export_name("kt_alloc"))) void* kt_alloc(uint32_t n) {
  return malloc(n);
}

__attribute__((export_name("kt_free"))) void kt_free(void* p) { free(p); }

__attribute__((export_name("kt_decode"))) Result* kt_decode(const uint8_t* buf,
                                                           uint32_t len) {
  Result* r = static_cast<Result*>(calloc(1, sizeof(Result)));
  basist::basisu_transcoder_init();
  basist::ktx2_transcoder t;
  if (!t.init(buf, len)) return fail(r, 1, "not a KTX2 file the transcoder reads");
  r->width = t.get_width();
  r->height = t.get_height();
  uint64_t px = static_cast<uint64_t>(r->width) * r->height;
  if (px == 0 || px > maxPixels) return fail(r, 3, "image too large");
  if (!t.start_transcoding()) return fail(r, 2, "cannot start transcoding");
  uint8_t* rgba = static_cast<uint8_t*>(malloc(px * 4));
  if (!rgba) return fail(r, 3, "out of memory");
  if (!t.transcode_image_level(0, 0, 0, rgba, static_cast<uint32_t>(px),
                               basist::transcoder_texture_format::cTFRGBA32, 0,
                               r->width, r->height)) {
    free(rgba);
    return fail(r, 2, "transcoding level 0 to RGBA8 failed");
  }
  r->rgba_ptr = ptr32(rgba);
  return r;
}

__attribute__((export_name("kt_release"))) void kt_release(Result* r) {
  if (!r) return;
  free(reinterpret_cast<void*>(static_cast<uintptr_t>(r->rgba_ptr)));
  free(reinterpret_cast<void*>(static_cast<uintptr_t>(r->error_ptr)));
  free(r);
}

}  // extern "C"
