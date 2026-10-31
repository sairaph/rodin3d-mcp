// Writes cases.json, the fixtures of meshopt_test.go: buffers encoded by the
// meshoptimizer 1.3.0 encoder (npm meshoptimizer@1.3.0) and their decoding
// by its meshopt_decoder_reference.js, which the Go package ports.
//
//   node gen.mjs <node_modules/meshoptimizer> > cases.json
import { pathToFileURL } from 'node:url';
import path from 'node:path';

const dir = process.argv[2];
const load = async (f) => import(pathToFileURL(path.join(dir, f)).href);
const { MeshoptEncoder: enc } = await load('meshopt_encoder.js');
const { MeshoptDecoder: ref } = await load('meshopt_decoder_reference.js');
const { MeshoptDecoder: wasm } = await load('meshopt_decoder.mjs');
await enc.ready;
await wasm.ready;

let seed = 1;
const rand = () => ((seed = (seed * 1103515245 + 12345) >>> 0) / 4294967296);
const b64 = (u8) => Buffer.from(u8.buffer, u8.byteOffset, u8.byteLength).toString('base64');
const bytes = (ta) => new Uint8Array(ta.buffer, ta.byteOffset, ta.byteLength);
const cases = [];
const add = (name, mode, filter, count, size, encoded) => {
  const want = new Uint8Array(count * size);
  ref.decodeGltfBuffer(want, count, size, encoded, mode, filter);
  const check = new Uint8Array(count * size);
  wasm.decodeGltfBuffer(check, count, size, encoded, mode, filter);
  // The reference rounds halves up where the wasm decoder rounds them away
  // from zero; the fixtures record how many bytes differ.
  let diff = 0;
  for (let i = 0; i < want.length; i++) if (want[i] !== check[i]) diff++;
  cases.push({ name, mode, filter, count, size, encoded: b64(encoded), want: b64(want), wasmDiff: diff });
};

// Vertex streams: smooth positions, a byte ramp, noise.
const count = 400;
const pos = new Float32Array(count * 3);
for (let i = 0; i < count; i++) {
  const a = i * 0.05;
  pos.set([Math.cos(a) * (1 + i / count), Math.sin(a), i / count], i * 3);
}
const uv = new Uint16Array(count * 2);
for (let i = 0; i < count; i++) uv.set([(i * 37) & 0xffff, (i * i) & 0xffff], i * 2);
const noise = new Uint8Array(count * 16);
for (let i = 0; i < noise.length; i++) noise[i] = (rand() * 256) | 0;
const streams = [['positions', bytes(pos), 12], ['uv16', bytes(uv), 4], ['noise', noise, 16]];
for (const [name, data, size] of streams) {
  for (const version of [0, 1]) {
    for (const level of version ? [0, 1, 2, 3] : [2]) {
      for (const n of [count, 17]) {
        const src = data.subarray(0, n * size);
        add(`${name} v${version} level ${level} n ${n}`, 'ATTRIBUTES', 'NONE', n, size,
          enc.encodeVertexBufferLevel(src, n, size, level, version));
      }
    }
  }
}

// Filters.
const nrm = new Float32Array(count * 4);
const quat = new Float32Array(count * 4);
const col = new Float32Array(count * 4);
for (let i = 0; i < count; i++) {
  let x = rand() * 2 - 1, y = rand() * 2 - 1, z = rand() * 2 - 1;
  let l = Math.hypot(x, y, z) || 1;
  nrm.set([x / l, y / l, z / l, 1], i * 4);
  let w = rand() * 2 - 1;
  l = Math.hypot(x, y, z, w) || 1;
  quat.set([x / l, y / l, z / l, w / l], i * 4);
  col.set([rand(), rand(), rand(), rand()], i * 4);
}
for (const version of [0, 1]) {
  for (const [stride, bits] of [[4, 8], [8, 16]]) {
    const f = enc.encodeFilterOct(nrm, count, stride, bits);
    add(`octahedral ${stride} v${version}`, 'ATTRIBUTES', 'OCTAHEDRAL', count, stride, enc.encodeVertexBufferLevel(f, count, stride, 2, version));
    const c = enc.encodeFilterColor(col, count, stride, bits);
    add(`color ${stride} v${version}`, 'ATTRIBUTES', 'COLOR', count, stride, enc.encodeVertexBufferLevel(c, count, stride, 2, version));
  }
  const q = enc.encodeFilterQuat(quat, count, 8, 12);
  add(`quaternion v${version}`, 'ATTRIBUTES', 'QUATERNION', count, 8, enc.encodeVertexBufferLevel(q, count, 8, 2, version));
  for (const mode of ['Separate', 'SharedVector', 'SharedComponent', 'Clamped']) {
    const e = enc.encodeFilterExp(pos, count, 12, 15, mode);
    add(`exponential ${mode} v${version}`, 'ATTRIBUTES', 'EXPONENTIAL', count, 12, enc.encodeVertexBufferLevel(e, count, 12, 2, version));
  }
}

// Triangles: a reordered grid and a random soup; index sequences.
const grid = [];
const g = 16;
for (let y = 0; y < g; y++) for (let x = 0; x < g; x++) {
  const a = y * (g + 1) + x, b = a + 1, c = a + g + 1, d = c + 1;
  grid.push(a, c, b, b, c, d);
}
const soup = [];
for (let i = 0; i < 600; i++) soup.push((rand() * 70000) | 0);
for (const [name, list] of [['grid', grid], ['soup', soup]]) {
  const reordered = new Uint32Array(list);
  if (name === 'grid') enc.reorderMesh(reordered, true, false); // in place
  for (const size of name === 'grid' ? [2, 4] : [4]) {
    const src = size === 2 ? new Uint16Array(reordered) : reordered;
    add(`triangles ${name} ${size}`, 'TRIANGLES', 'NONE', src.length, size, enc.encodeIndexBuffer(bytes(src), src.length, size));
    add(`indices ${name} ${size}`, 'INDICES', 'NONE', src.length, size, enc.encodeIndexSequence(bytes(src), src.length, size));
  }
}
console.log(JSON.stringify(cases, null, 1));
