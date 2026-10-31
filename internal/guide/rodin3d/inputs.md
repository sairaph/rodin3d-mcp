# Inputs: images, view tags, weights, input sets

## Images

- generate_model takes 1-5 images of one object: absolute paths of jpg, png or webp files, at most 20 MB each and 20 MB together. An image larger than 1024 px is scaled down like the web does.
- Two or more images need the RodinUseMultiImage privilege; get_generation_options shows whether the account has it.
- Hyper3D preprocesses each image first (free) and writes the caption of the first image as the prompt. Pass prompt (at most 1024 characters) to replace it.
- An image Hyper3D refuses (no object found on it, or its content) answers invalid_input with the reason; the error's service_code names it.

```json
{"images": [{"path": "C:/photos/mug-front.jpg", "view": "F"}, {"path": "C:/photos/mug-left.jpg", "view": "L"}], "model": "gen-2.5", "tier": "medium", "expected_credits": 0}
```

expected_credits is always the credits quote_cost returned for the same model, tier and batch.

## View tags

- Tag each image with the side it shows the object from: F front, B back, L left, R right, FL, FR, BL, BR (the corners), U up, D down, ? unknown (the default).
- Several images need view tags for a good result. Use ? only when the side is not known.

## Weights

- weight is an image's relative weight, above 0 and at most 1; the weights are normalised to sum 1. Give every image a weight, or none.
- With none, the default table applies (get_generation_options default_weights): Gen-2.5 weights the images equally, Gen-1.5 weights the first image most (two images: 0.6 and 0.4). For Gen-1.5, put the best image first.

## Input sets (viewer)

An input set is a list of images with view tags and weights that the user and you arrange in the viewer window.

1. Only when the user wants to arrange the images in the window: open_viewer, then show_in_viewer with content input_set (images, or the id of a stored set) or task_inputs (a task id: an editable copy of that task's inputs, a new set).
2. The user retags, reorders and reweights the images in the window; control_viewer set_tag and reorder do the same.
3. Call get_viewer_state, check the tags with the user, then pass the set's id as generate_model input_set instead of images.

The order, tags and weights are taken as stored when generate_model runs, so read get_viewer_state right before it.

## Other inputs

- texture_model starts from your own model file and 1-5 pictures, the first showing the model's front (topic `other-modes`).
- generate_texture reference_image picks the input the texture follows: an input name from get_task, ALL (every input), or the absolute path of a new image.
