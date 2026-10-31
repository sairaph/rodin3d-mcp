// Writes the cube fixtures of modelfile_test.go: a unit cube (24 corners,
// each face mapped onto the whole texture) with 16 x 16 textures, in every
// variant the reader decodes. Run it from a folder where npm installed
// @gltf-transform/core, extensions and functions 4.5.1, draco3dgltf 1.5.7,
// meshoptimizer 1.3.0, ktx2-encoder 0.6.0 and sharp 0.35:
//
//   node gen.mjs <outdir>
import { Document, NodeIO } from '@gltf-transform/core';
import { ALL_EXTENSIONS, KHRTextureTransform, KHRMaterialsEmissiveStrength, KHRMaterialsClearcoat } from '@gltf-transform/extensions';
import { draco, meshopt, textureCompress } from '@gltf-transform/functions';
import { ktx2 } from 'ktx2-encoder/gltf-transform';
import draco3d from 'draco3dgltf';
import { MeshoptEncoder } from 'meshoptimizer';
import sharp from 'sharp';
import path from 'node:path';

const out = process.argv[2];
await MeshoptEncoder.ready;
const io = new NodeIO().registerExtensions(ALL_EXTENSIONS).registerDependencies({
  'draco3d.encoder': await draco3d.createEncoderModule(),
  'draco3d.decoder': await draco3d.createDecoderModule(),
  'meshopt.encoder': MeshoptEncoder,
});

// png16 is a 16 x 16 PNG of f(x, y) -> [r, g, b].
const png16 = async (f) => {
  const px = Buffer.alloc(16 * 16 * 3);
  for (let y = 0; y < 16; y++) for (let x = 0; x < 16; x++) px.set(f(x, y), (y * 16 + x) * 3);
  return sharp(px, { raw: { width: 16, height: 16, channels: 3 } }).png().toBuffer();
};
// Quadrants: red top left, green top right, blue bottom left, yellow
// bottom right (glTF images have v down).
const quadrants = (x, y) => (y < 8 ? (x < 8 ? [220, 40, 40] : [40, 200, 60]) : (x < 8 ? [40, 60, 220] : [230, 210, 40]));
const base = await png16(quadrants);
const orm = await png16(() => [200, 150, 40]); // occlusion R, roughness G, metalness B
const flat = await png16(() => [128, 128, 255]);

// cube builds the document; faces split [0, 3) and [3, 6) when two
// materials are given.
function cube(doc, materials) {
  const faces = [
    [[1, 0, 0], [0, 0, -1], [0, -1, 0]], [[-1, 0, 0], [0, 0, 1], [0, -1, 0]],
    [[0, 1, 0], [1, 0, 0], [0, 0, 1]], [[0, -1, 0], [1, 0, 0], [0, 0, -1]],
    [[0, 0, 1], [1, 0, 0], [0, -1, 0]], [[0, 0, -1], [-1, 0, 0], [0, -1, 0]],
  ];
  const buf = doc.createBuffer();
  const mesh = doc.createMesh('cube');
  const groups = materials.length === 1 ? [[0, 6]] : [[0, 3], [3, 6]];
  groups.forEach(([lo, hi], gi) => {
    const pos = [], nrm = [], uv = [], idx = [];
    for (let f = lo; f < hi; f++) {
      const [n, u, v] = faces[f];
      const b = pos.length / 3;
      for (const [su, sv] of [[0, 0], [1, 0], [1, 1], [0, 1]]) {
        for (let k = 0; k < 3; k++) pos.push(0.5 * n[k] + (su - 0.5) * u[k] + (sv - 0.5) * v[k]);
        nrm.push(...n);
        uv.push(su, sv);
      }
      idx.push(b, b + 2, b + 1, b, b + 3, b + 2);
    }
    const acc = (type, arr) => doc.createAccessor().setType(type).setArray(arr).setBuffer(buf);
    const prim = doc.createPrimitive()
      .setAttribute('POSITION', acc('VEC3', new Float32Array(pos)))
      .setAttribute('NORMAL', acc('VEC3', new Float32Array(nrm)))
      .setAttribute('TEXCOORD_0', acc('VEC2', new Float32Array(uv)))
      .setIndices(acc('SCALAR', new Uint16Array(idx)))
      .setMaterial(materials[gi]);
    mesh.addPrimitive(prim);
  });
  doc.createScene().addChild(doc.createNode('cube').setMesh(mesh));
  return doc;
}

const tex = (doc, name, image) => doc.createTexture(name).setImage(image).setMimeType('image/png');

function pbrDoc() {
  const doc = new Document();
  const m = doc.createMaterial('cube')
    .setBaseColorTexture(tex(doc, 'base', base))
    .setMetallicRoughnessTexture(tex(doc, 'orm', orm))
    .setOcclusionTexture(doc.getRoot().listTextures()[1])
    .setNormalTexture(tex(doc, 'normal', flat))
    .setBaseColorFactor([1, 1, 1, 1]).setMetallicFactor(0.5).setRoughnessFactor(0.8);
  return cube(doc, [m]);
}

const write = async (name, doc) => {
  await io.write(path.join(out, name), doc);
  console.log('wrote', name);
};

await write('cube_png.glb', pbrDoc());

let d = pbrDoc();
await d.transform(textureCompress({ encoder: sharp, targetFormat: 'webp', quality: 90 }));
await write('cube_webp_lossy.glb', d);

d = pbrDoc();
await d.transform(textureCompress({ encoder: sharp, targetFormat: 'webp', lossless: true }));
await write('cube_webp_lossless.glb', d);

d = pbrDoc();
await d.transform(draco({ method: 'edgebreaker' }), textureCompress({ encoder: sharp, targetFormat: 'webp', lossless: true }));
await write('cube_draco_webp.glb', d);

d = pbrDoc();
await d.transform(meshopt({ encoder: MeshoptEncoder, level: 'medium' }));
await write('cube_meshopt.glb', d);

const imageDecoder = async (buffer) => {
  const { data, info } = await sharp(buffer).ensureAlpha().raw().toBuffer({ resolveWithObject: true });
  return { data: new Uint8Array(data), width: info.width, height: info.height };
};
for (const mode of ['etc1s', 'uastc']) {
  d = pbrDoc();
  await d.transform(ktx2({ isUASTC: mode === 'uastc', generateMipmap: true, imageDecoder, needSupercompression: mode === 'uastc' }));
  await write(`cube_${mode}.glb`, d);
}

// Two materials: the first as cube_png's; the second a red tint of the
// base texture through KHR_texture_transform (offset 0.5, 0; scale 0.5, 1:
// the right half), clamped and mirrored, with an emissive factor at
// KHR_materials_emissive_strength 2, and a clearcoat the renderer does not
// draw.
d = new Document();
{
  const baseTex = tex(d, 'base', base);
  const m1 = d.createMaterial('first').setBaseColorTexture(baseTex)
    .setMetallicRoughnessTexture(tex(d, 'orm', orm)).setMetallicFactor(0.5).setRoughnessFactor(0.8);
  const tt = d.createExtension(KHRTextureTransform);
  const es = d.createExtension(KHRMaterialsEmissiveStrength);
  const cc = d.createExtension(KHRMaterialsClearcoat);
  const m2 = d.createMaterial('second').setBaseColorTexture(baseTex).setBaseColorFactor([1, 0.5, 0.5, 1])
    .setMetallicFactor(0).setRoughnessFactor(0.3).setEmissiveFactor([0.25, 0.125, 0])
    .setExtension('KHR_materials_emissive_strength', es.createEmissiveStrength().setEmissiveStrength(2))
    .setExtension('KHR_materials_clearcoat', cc.createClearcoat().setClearcoatFactor(1));
  m2.getBaseColorTextureInfo()
    .setWrapS(33071).setWrapT(33648)
    .setExtension('KHR_texture_transform', tt.createTransform().setOffset([0.5, 0]).setScale([0.5, 1]));
  cube(d, [m1, m2]);
}
await write('cube_multi.glb', d);

// The shaded pack's layout: black base colour, no metalness, the picture
// as the emissive texture.
d = new Document();
{
  const m = d.createMaterial('model').setBaseColorFactor([0, 0, 0, 1]).setMetallicFactor(0)
    .setEmissiveFactor([1, 1, 1]).setEmissiveTexture(tex(d, 'shaded', base)).setDoubleSided(true);
  cube(d, [m]);
}
await write('cube_shaded.glb', d);
