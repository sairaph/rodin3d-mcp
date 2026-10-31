# Other modes: Agentic, parts, rigging, animation, your own model

Each of these is paid: quote first, pass expected_credits (topic `credits`).

## generate_agentic: an Agentic build from images

- 1-5 absolute image paths of one object; no view tags or weights. budget is required: basic (about 3 minutes), standard (about 5, the web's default) or pro (about 10). Quote with settings.budget; the credits are frozen at submit and taken during the run.
- ngons true (default): the server picks the producer. A high-poly result is an ordinary Rodin task that ends as a geometry draft: confirm_geometry and the other stage tools continue it. An n-gons result is a RodinAgentic task: no paid or stage tool here acts on it; get_task, wait_task and get_preview read it and set_private changes its privacy. ngons false asks for the high-poly producer.
- The run takes minutes: follow it with wait_task. Never resend after a time-out.

## split_parts: split a geometry draft into parts (Bang)

- Only at stage geometry_draft. strength is how many parts the plan aims for, 1-12 (default 5); above 5 needs RodinBangGenerateAdvancedQuality.
- step plan lays out the parts and attaches the plan picture when it is ready within the wait: show it to the user. get_task shows that plan as bang_plan with its picture, free, until a split follows.
- step generate splits by the plan made before and takes its strength. step both (default) does plan and split in one call.
- The split is a Bang result of the draft version, one mesh per part. Finished within the wait, its parts file is saved and a render attached; otherwise get_task lists it under bang and download_model saves it.
- bang_version with part splits one part of an earlier result again.
- confirm_geometry bang_version confirms the parts; they keep their parts, in one UV group.
- Quote with task_id, settings.step, strength, version and part.

```json
{"task_id": "<task id>", "step": "plan", "strength": 5, "expected_credits": -1}
```

Pass -1 only when quote_cost said the price is not known and the user accepted that.

## rig_model: a skeleton and skin

- For a task with confirmed geometry (stages geometry, material_draft, material).
- One skeleton source: template human (the Mixamo skeleton, the default), quadruped, bird or kaiju (a four-legged skeleton with a tail), dragon (adds wings); or skeleton, your own bone hierarchy (one bone per line, depth by leading - characters); or description, the skeleton in words; or fbx_skeleton_path, the skeleton of an FBX file (two calls: the rig, then the reskin; reskin true sends only the second).
- skinning core (human template only, its default) or shell (the default otherwise).
- get_task shows the rig; a rig call while one still runs is refused before any paid request.
- Quote with task_id, settings.template (custom for skeleton, description and fbx_skeleton_path) and skinning.

## animate_model: export the rigged model with animations

- For a rigged task. Up to 10 animations: presets as <category>/<name> from list_animation_presets (free; template names the category the web shows first for a rig template), and animation_files, your own FBX animations (one clip each, made for this skeleton, at most 25 MiB). More than 5 per export needs GatewayContentGenerationForFree.
- format glb (default), fbx or usdz; resolution, material, height and align_ground as in export_model.
- Finished within the wait, it returns the file names; otherwise get_task lists them once done. download_model saves them.
- Quote with task_id, settings.format, resolution, material, animations and animation_files.

```json
{"operation": "animate_model", "task_id": "<task id>", "settings": {"format": "glb", "animations": 1}}
```

## texture_model: texture a model file of your own

- model_path: an absolute path of a glb, gltf, fbx, obj, dae, stl or ply file, at most 150 MB. images: 1-5 pictures the texture follows, the first showing the model's front; default a render of the model's front (glb, gltf, obj and stl only).
- The task runs the ordinary stages: confirm_geometry, generate_texture and export_model follow.
- Quote with settings.model and images. The quote can show frozen_at_submit: taken when the geometry is confirmed, or released.
- The new task is private unless public is true. Never resend after a time-out: list_tasks shows whether it started.
