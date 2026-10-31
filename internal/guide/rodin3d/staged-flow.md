# Staged flow: from images to model files

A task moves through stages: geometry_draft, geometry (confirmed), material_draft, material (confirmed). Its state is running, ready or packed. get_task lists the actions its stage allows now; follow them.

## 1. Draft: generate_model

- Quote (operation generate_model, settings model, tier and batch), then call with expected_credits. Images and tags: topic `inputs`.
- The reply has the new task at geometry_draft with its poster attached, the quote and the wallet before and after.
- Never resend after a time-out: list_tasks shows whether it started.
- Stop and show the user the draft. Next: redo_geometry, select_candidate, split_parts or confirm_geometry.

## 2. Redo or pick: redo_geometry, select_candidate

- redo_geometry makes a new version with the draft's settings, optionally a new prompt and advanced options. Free below extreme-high; the plan limits the number (get_task redo_left).
- select_candidate keeps one draft of a batch: candidate is its version (v1, v2, ...). Free.

## 3. Confirm the geometry: confirm_geometry

```json
{"operation": "confirm_geometry", "task_id": "<task id>", "settings": {"mesh": "quad"}}
```

- Confirms a draft version (default the newest; get_task lists versions) as the model's mesh. Quote with task_id, mesh, smart_low_poly and bang_version when used.
- A Free Preview draft is paid for here.
- Next: generate_texture, export_model for the untextured mesh, or rig_model.

## 4. Texture: generate_texture

- Quote with task_id, texture_mode and the options. The reply has the task at material_draft. Show it to the user.

## 5. Redo or confirm the material: redo_material, confirm_material

- redo_material makes a new version, changing only what you pass. The web shows a price only at texture_mode extreme-high; below it the quote may say not known.
- confirm_material confirms a version (default the newest). Quote with task_id.

## 6. Pack: export_model

- Packs the confirmed mesh (stage geometry) or the textured model with its maps (stage material) in one format, texture size, geometry level and material set. Quote with task_id, format, resolution, geometry and material.
- The reply has the task, packed once done, and the packed file names.

## 7. Download: download_model

- Saves the pack to output_dir, or downloads/<id8>-<prompt> in the data directory. files limits it to names from get_task files.pack; stage videos and Bang parts files can be named too.
- Answers conflict while the task has no finished pack: export_model first.

## Branch: fork_task

- A new task from the geometry (back to a geometry draft) or the material (back to a material draft); the original stays as it is. Only for tasks younger than 30 days.
- Quote with task_id and settings.stage. The new task is private unless public is true.

## Waiting

- Every tool that starts a job waits up to 45 s (wait true). When the reply says timed_out true or running, call wait_task with the task id: it reads the task every 5 seconds until the stage or state changes or timeout_seconds (1-50, default 45) pass. Call it again after a time-out.
- wait false returns at once: follow it with wait_task.
- generate_agentic runs take minutes: expect several wait_task calls.

## Where credits go

- Free Preview drafts: confirm_geometry takes the credits (some drafts freeze credits at submit until then: topic `credits`).
- gen-2.5 high and extreme-high drafts: charged at submit.
- export_model: the web's pack tip says downloads of gen-2.5 extreme-low and low models are free.
- Everything else: ask quote_cost; its charged_at says when the amount is taken.

## When to stop and show the user

- After every draft, geometry or material: show it and ask redo or confirm.
- Before every paid step whose price the user has not agreed to, and when a price is not known.
- When get_task redo_left runs low, before spending the last redos.
