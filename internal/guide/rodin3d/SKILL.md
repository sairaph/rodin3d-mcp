---
name: rodin3d
description: Workflows and rules for the rodin3d MCP server, which turns images into 3D models (image-to-3D) with Rodin (Hyper3D) on the user's own account. Use when the user wants to make a 3D model from one or more photos or images, mentions Rodin or Hyper3D, wants to generate, redo, confirm, texture, export or download a generated model, split it into parts, rig or animate it, texture a model file of their own, check the credits or the price of a generation, or look at a generation in the viewer window.
metadata:
  generator: rodin3d-mcp
  version: "0.1.0"
---

# Rodin 3D guide

Call the tools by their bare names, such as get_account; your client may add a prefix. get_guide serves the same topics, so any client can read them.

## Start here

1. Call get_account. When it answers `authentication`, sign in first: topic `start`.
2. Call get_generation_options for the models, tiers, settings, plan gates and the owner's recipes: topic `settings`.
3. Before every paid call, call quote_cost with the same settings and pass its credits as expected_credits: topic `credits`.
4. Run the staged flow: generate_model, confirm_geometry, generate_texture, confirm_material, export_model, download_model. Topic `staged-flow`.

## Rules

- Show the user the price before a paid call unless they already agreed to it. The price comes from quote_cost, never from memory.
- Never resend a paid call that timed out or got no answer: it may have started. get_task or list_tasks shows whether it did.
- After each draft, show it (the attached poster or get_preview) and let the user choose redo or confirm before more credits go.
- Follow each reply's actions and Next line, and each error's hint: they name the next call.
- New tasks are private. Make one public only when the user asks: topic `privacy-and-errors`.
- Open the viewer window only when the user asks to see something: topic `viewing`.
- A [permission] key set to deny refuses its tool. Ask the user; they change config.toml.

## Topics

| Topic | What it covers |
| --- | --- |
| `start` | Account, sign-in with the link and QR code, permissions |
| `credits` | quote_cost, expected_credits, timeouts, free operations, frozen credits |
| `inputs` | Images, view tags, weights, input sets from the viewer |
| `settings` | Models, tiers, modes, recipes and every stage's settings |
| `staged-flow` | Draft, redo, confirm, texture, export, download, fork, waiting |
| `other-modes` | Agentic builds, parts, rigging, animation, texturing your own model |
| `viewing` | get_preview modes and the viewer window |
| `privacy-and-errors` | Private tasks and what to do for each error code |
