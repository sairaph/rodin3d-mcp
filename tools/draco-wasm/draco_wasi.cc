// draco_wasi.cc - a small C ABI over the Google Draco decoder for wasm32-wasi.
// The host (Go, wazero) copies an encoded buffer into linear memory, calls
// dw_decode, reads the fixed-layout result tables, then calls dw_release.
//
// Result layout (all little-endian uint32, offsets in bytes):
//   0 status        0 = ok, 1 = decode error, 2 = unsupported geometry
//   4 geometry      0 = point cloud, 1 = triangular mesh
//   8 num_points
//  12 num_faces
//  16 indices_ptr   uint32[num_faces*3] point indices (0 when no faces)
//  20 num_attrs
//  24 attrs_ptr     num_attrs records of 16 bytes:
//                   type (draco::GeometryAttribute::Type), unique_id,
//                   num_components, data_ptr (float32[num_points*num_components],
//                   values converted with normalisation applied)
//  28 error_ptr     NUL-terminated message (0 when ok)
#include <cstdint>
#include <cstdlib>
#include <cstring>
#include <memory>

#include "draco/compression/decode.h"
#include "draco/core/decoder_buffer.h"
#include "draco/mesh/mesh.h"
#include "draco/point_cloud/point_cloud.h"

namespace {

struct Attr {
  uint32_t type, unique_id, num_components, data_ptr;
};

struct Result {
  uint32_t status, geometry, num_points, num_faces, indices_ptr, num_attrs,
      attrs_ptr, error_ptr;
};

uint32_t ptr32(const void* p) {
  return static_cast<uint32_t>(reinterpret_cast<uintptr_t>(p));
}

Result* fail(Result* r, uint32_t status, const std::string& msg) {
  r->status = status;
  char* m = static_cast<char*>(malloc(msg.size() + 1));
  memcpy(m, msg.c_str(), msg.size() + 1);
  r->error_ptr = ptr32(m);
  return r;
}

}  // namespace

extern "C" {

__attribute__((export_name("dw_alloc"))) void* dw_alloc(uint32_t n) {
  return malloc(n);
}

__attribute__((export_name("dw_free"))) void dw_free(void* p) { free(p); }

__attribute__((export_name("dw_decode"))) Result* dw_decode(const char* buf,
                                                           uint32_t len) {
  Result* r = static_cast<Result*>(calloc(1, sizeof(Result)));
  draco::DecoderBuffer in;
  in.Init(buf, len);
  auto type = draco::Decoder::GetEncodedGeometryType(&in);
  if (!type.ok()) return fail(r, 1, type.status().error_msg_string());
  draco::Decoder dec;
  std::unique_ptr<draco::PointCloud> pc;
  if (type.value() == draco::TRIANGULAR_MESH) {
    auto m = dec.DecodeMeshFromBuffer(&in);
    if (!m.ok()) return fail(r, 1, m.status().error_msg_string());
    std::unique_ptr<draco::Mesh> mesh = std::move(m).value();
    r->geometry = 1;
    r->num_faces = mesh->num_faces();
    uint32_t* idx =
        static_cast<uint32_t*>(malloc(sizeof(uint32_t) * 3 * r->num_faces + 4));
    for (draco::FaceIndex f(0); f < mesh->num_faces(); ++f) {
      const auto& face = mesh->face(f);
      for (int k = 0; k < 3; ++k) idx[f.value() * 3 + k] = face[k].value();
    }
    r->indices_ptr = ptr32(idx);
    pc = std::move(mesh);
  } else if (type.value() == draco::POINT_CLOUD) {
    auto p = dec.DecodePointCloudFromBuffer(&in);
    if (!p.ok()) return fail(r, 1, p.status().error_msg_string());
    pc = std::move(p).value();
  } else {
    return fail(r, 2, "unsupported encoded geometry type");
  }
  r->num_points = pc->num_points();
  r->num_attrs = pc->num_attributes();
  Attr* attrs = static_cast<Attr*>(calloc(r->num_attrs + 1, sizeof(Attr)));
  for (int i = 0; i < pc->num_attributes(); ++i) {
    const draco::PointAttribute* a = pc->attribute(i);
    const int nc = a->num_components();
    float* data = static_cast<float*>(
        malloc(sizeof(float) * static_cast<size_t>(nc) * r->num_points + 4));
    for (draco::PointIndex p(0); p < pc->num_points(); ++p) {
      a->ConvertValue<float>(a->mapped_index(p), nc, data + p.value() * nc);
    }
    attrs[i] = Attr{static_cast<uint32_t>(a->attribute_type()),
                    a->unique_id(), static_cast<uint32_t>(nc), ptr32(data)};
  }
  r->attrs_ptr = ptr32(attrs);
  return r;
}

__attribute__((export_name("dw_release"))) void dw_release(Result* r) {
  if (!r) return;
  free(reinterpret_cast<void*>(static_cast<uintptr_t>(r->indices_ptr)));
  Attr* attrs = reinterpret_cast<Attr*>(static_cast<uintptr_t>(r->attrs_ptr));
  if (attrs) {
    for (uint32_t i = 0; i < r->num_attrs; ++i) {
      free(reinterpret_cast<void*>(static_cast<uintptr_t>(attrs[i].data_ptr)));
    }
    free(attrs);
  }
  free(reinterpret_cast<void*>(static_cast<uintptr_t>(r->error_ptr)));
  free(r);
}

}  // extern "C"
