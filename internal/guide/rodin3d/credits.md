# Credits: quotes, expected_credits, timeouts, frozen credits

## Quote before every paid call

The paid tools: generate_model, redo_geometry, confirm_geometry, generate_texture, redo_material, confirm_material, export_model, fork_task, generate_agentic, split_parts, rig_model, animate_model, texture_model.

1. Call quote_cost with operation (the tool's name), task_id when the operation acts on a task, and settings that match the call you will make.
2. Show the user the price unless they already agreed to it.
3. Pass the quote's credits as expected_credits.

```json
{"operation": "confirm_geometry", "task_id": "<task id>", "settings": {"mesh": "raw"}}
```

- The call runs only at the quoted price. A different price now answers conflict with the new quote: quote again and show the user the new price.
- known false means the price is not known (basis says why). Tell the user so, and pass -1 as expected_credits only when they accept that.
- redo_geometry and redo_material need expected_credits only when the quote is not a known 0; the other paid tools always need it. select_candidate takes none: it is free.
- A quote carries credits, known, measured (a recorded wallet change confirms it; otherwise it rests on the web's label), charged_at (when the amount is taken), free_reason, basis and frozen_at_submit. It also returns the wallet, whether the account can afford it, and the operation's permission key.
- quote_cost answers forbidden when the plan does not allow the operation or an option: choose one get_generation_options shows as allowed.

## Never resend a paid call

- A request that got no answer answers unavailable and says it may have started on Hyper3D. Do not send it again. Call get_task with the task id, or list_tasks (newest first) for a call that creates a task, to see whether it started.
- A reply with timed_out true is not a failure: the job runs on. Call wait_task with the task id.
- get_task lists under spend every paid or changing call this tool made on the task.

## Free operations

- Reading costs nothing: get_account, get_generation_options, quote_cost, list_tasks, get_task, wait_task, get_preview, list_animation_presets, get_guide.
- download_model saves the files of a finished pack; the pack itself is made by export_model.
- The viewer tools (open_viewer, show_in_viewer, control_viewer, get_viewer_state, close_viewer) change only the local window.
- set_private is free and has no permission key.
- select_candidate is free. Geometry redos are free below the extreme-high tier; the plan limits how many (get_task redo_left).
- A quote's free_reason says why an operation costs nothing.

## Frozen credits

- Hyper3D freezes the amount of some jobs at submit, then takes it when the job finishes, or releases it. get_account and every paid reply show frozen next to available.
- A draft labelled Free Preview can still freeze credits at submit; the quote shows them as frozen_at_submit. confirm_geometry takes them. A draft that is never confirmed keeps them frozen (seen for over an hour), so it lowers the available credits.
- insufficient_credits: call get_account and tell the user. They top up, or frozen credits are released.
