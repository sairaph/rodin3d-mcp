# Viewing: previews and the viewer window

## get_preview: a picture in the reply

Use it to show a stage to the user; it needs no window. Pass task_id, stage (current by default, geometry_draft, geometry or material), mode, view and size (256-1024, default 512). The image is attached and saved to save_to or the previews folder.

- Server images: poster (default, the stage's own picture), geometry_image and material_image, and the texture maps texture_diffuse, texture_pbr, texture_normal and texture_shaded, which are flat UV atlases, not views of the model.
- Local renders of the stage's mesh: normal, raw, classic, glossy, metal, ao, wireframe and depth on every stage with a mesh; copper, geometry and uv_checker from the confirmed geometry on; pbr, shaded, toon and tracer at the material stage. tracer is path traced: about 5 s at 512 px, 20 s at 1024.
- view: three_quarter (default), front, back, left, right, top, web (the web viewer's start camera) or grid (four views in one picture).
- A gen-1.5 geometry draft has no mesh. Use turntable_dark, turntable_copper, turntable_light or turntable_normal: frames of the server's turntable, 16 round the model, picked by view (no top).
- The first render of a stage decodes its mesh (a few seconds, once); a repeated call is instant.
- A mode the stage does not have answers conflict with the available stage and mode pairs; get_task lists them as previews.
- model_file instead of task_id renders a local glb, gltf, obj or stl file (render modes only, no stage). A glTF file with materials adds pbr (its default), shaded, toon and tracer, drawn with its own textures, Draco, meshopt, WebP and KTX2 too; the reply lists its modes and what it is drawn without.

```json
{"task_id": "<task id>", "stage": "geometry", "mode": "wireframe", "view": "grid"}
```

## The viewer window

A native window of this program on this computer, which the user controls too.

- Open it with open_viewer only when the user asks to see something; call close_viewer when they are done. It answers unavailable on a computer without a display.
- show_in_viewer needs the open window and shows exactly one of: task (a stage in 3D with its images strip), compare (two stages side by side), input_set, task_inputs, textures, model_file (a local glb, gltf, obj or stl) or images.
- control_viewer changes layout, stage, version, mode, view, orbit, zoom, pan, turntable, wireframe_overlay, background, select, the tags, weights and order of a shown input set (set_tag, reorder), highlight and message (a banner for the user). Every field is checked before any is applied. On a gen-1.5 draft view and orbit pick a turntable frame; top, elevation, pan, wireframe overlay and background answer conflict.
- The user orbits, zooms, switches modes and stages and retags images at any time. Call get_viewer_state before relying on what is shown: content, stage, mode, view, camera, revision, user_actions since since_revision, and a screenshot as the user sees it.
- Pass expected_revision from the last state to show_in_viewer and control_viewer, so a change the user just made is not overwritten. A stale revision answers conflict with the current state: read it, then decide with the user.
- Use highlight and message to point the user at an image, a view tag or a detail.
