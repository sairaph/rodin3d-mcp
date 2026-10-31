# Privacy and errors

## Privacy

- Every task this tool creates is made private right after the request: generate_model, generate_agentic, texture_model and fork_task. public true keeps the new task public: pass it only when the user asks.
- set_private (free, no permission key) makes an existing task private (the default) or, with private false, public. A task already in that state is left as it is.
- When a create's reply says the new task could not be made private, call set_private as its note says.
- set_private sends Hyper3D's toggle. When it answers that the card still reads the old privacy, call get_task in a few seconds and call set_private again only if it still shows the old privacy; otherwise the toggle flips the task back.

## Errors

Every error carries a code, a message and a hint that names the next call. Follow the hint.

| Code | Means | Do |
| --- | --- | --- |
| authentication | Not signed in, or the session ended | login, give the user the link, then get_account (topic `start`) |
| forbidden | A [permission] key is deny (fields permission, value, file), or the plan does not allow the operation or option | Ask the user to allow the key; for a plan gate, choose an option the plan allows (get_account, get_generation_options) |
| conflict | The stage does not allow the tool now, expected_credits is missing or differs from the price, a viewer revision is stale, or a stage or mode does not exist | Read the hint: wait_task for a running task, the allowed actions otherwise; quote again and show the new price; get_viewer_state |
| insufficient_credits | Not enough available credits | get_account; the user tops up, or frozen credits are released |
| invalid_input | An argument is wrong, or Hyper3D refused an image | Fix what the message names; get_generation_options lists the values |
| not_found | No such task or version | list_tasks for the ids |
| ambiguous | An id prefix matches several tasks | Pass more characters or the full id |
| rate_limited | Too many requests | Wait a minute, then retry |
| unavailable | Hyper3D could not be reached or failed, a file address expired, or no viewer window is open | Retry later; call the same tool again for fresh file addresses; open_viewer only when the user asks |
| service_error | Hyper3D answered an error this build does not map (field service_code) | Check the arguments against get_generation_options, then retry |
| internal_error | A bug, not caused by the arguments | Run `rodin3d-mcp doctor`, then retry |

- An unavailable answer that says the request was sent but no answer came back is the exception: never send it again. Call get_task with the task id, or list_tasks for a call that creates a task.
- A conflict on a RodinAgentic task (an n-gons result of generate_agentic): only get_task, wait_task, get_preview and set_private act on it.
- A download_model conflict means no finished pack yet: export_model first.
