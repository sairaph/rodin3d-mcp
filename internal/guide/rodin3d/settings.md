# Settings: models, tiers, modes, recipes

## Read the options first

get_generation_options lists the models, every setting of each stage with its values, default and plan gate, the view tags and default weights, the preview modes per stage, the privileges and whether this account has each, the operations quote_cost prices with their permission keys, and the owner's recipes. Pass model to narrow it to gen-2.5 or gen-1.5.

## Models

- gen-2.5 (the default) needs tier: extreme-low, low, medium, high or extreme-high, thinking about 4, 9, 20, 40 and 80 s. extreme-high needs the RodinUseAdditionalImage privilege (Creator plan or higher).
- gen-1.5 takes mode instead of tier: default, focal, zero or speedy (the web's Default, Detail, Smooth and Fast).
- The web labels gen-2.5 extreme-low, low and medium and every gen-1.5 mode Free Preview: the draft is free and confirming the geometry costs credits. gen-2.5 high and extreme-high are charged at submit. quote_cost gives the numbers.
- A gen-1.5 geometry draft has no mesh: get_preview shows it through the server's turntable frames (topic `viewing`).

## Choosing

- Start from what the user asked for. When they named nothing, offer the owner's recipes: setting combinations found in the account's history, most used first, each with example task ids (get_task shows an example's full settings).
- copy_settings_from with a task id fills model, tier, mode and presets for the fields left out; the reply's recipe lists that task's later-stage settings.
- Offer only settings the plan allows: a gated setting answers forbidden.
- Quote each option you offer, so the user chooses with the price in view.
- batch (drafts per submit, more than 1) needs the Business plan; select_candidate keeps one draft.

## generate_model

- presets: micro (fine surface detail) or clean, gen-2.5 extreme-high only; instruct creative (corrects proportions) or faithful (keeps the input perspective), gen-2.5 medium and above; symmetric or asymmetric, with symmetric_strength 1-3; detailed 1-3 (surface relief); soft (smooth edges). Default none: Hyper3D's recommendation.
- advanced: seed 0-65535, negative_prompt, cfg 1-15 (web default 10), steps 10-100 (web default 50), detail -5 to 5. The web sends them only with a redo; redo_geometry takes them too.
- face_enhance (default what Hyper3D suggests for the image), bbox [width, height, length] each 1-300, image_mode recon (default) or rest.

## confirm_geometry

- mesh raw (Triangle, detailed) or quad; default raw when the plan allows it (RodinMeshAdditionalFeatures), else quad.
- faces: raw simplifies to it (none: the full mesh); quad uses it instead of quality. gen-2.5 high and extreme-high 20000-2000000, other gen-2.5 500-1000000, gen-1.5 500-200000.
- quality (quad): extra-low 4000, low 8000, medium 18000 (default), high 50000 faces.
- smart_low_poly: its own price, needs RodinRemeshAdditionalFeatures; quality and mesh apply.
- bake_normal (raw: only with faces of 20000 or less), symmetric (default true), hyper (default true), smooth (quad).

## generate_texture and redo_material

- texture_mode: extreme-low, low, medium, high (default) or extreme-high; gen-1.5 tasks use their legacy texture.
- hd_texture (HD), sharp (the web's 8K chip) and face_enhance (face restore) do not apply at extreme-high; HD and 8K need RodinTextureAdditionalFeatures.
- delight removes baked-in lighting; escore is the PBR temperature 0-10 in steps of 0.5 (default 7); reference_scale (0-1) is gen-1.5 only.
- redo_material keeps the draft's settings except what you pass; seed new (default) or keep.

## export_model

- type mesh (confirmed geometry) or material (textured); default follows the stage.
- format obj (default), fbx, glb, usdz or stl.
- resolution of a material pack: 1k, 2k (default), 6k (Basic) or 4k, 12k (High, Business plan); 6k and 12k only for extreme-high textures. 8k is not a pack size: it is generate_texture sharp.
- geometry base (default), lod or high_poly (Business plan); material all (default), pbr or shaded; align_ground (default true); height scales the model to a real-world height.
