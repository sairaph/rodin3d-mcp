# Start: account, sign-in and permissions

## 1. Read the account

Call get_account first. It shows the username, the plan and its renewal date, the wallet (available, regular, subscription and frozen credits), the [permission] values of config.toml, the session and the background syncer.

- `login.expires_at` is when the current access token expires. It renews itself: it is not a sign-in deadline.
- list_tasks reads the local index the syncer keeps (every 15 minutes, every 5 seconds for running tasks); right after the first sign-in it waits for the first sync. get_task reads one task live.

## 2. Sign in when get_account answers authentication

1. Call login with no arguments. It returns `authorize_url` and attaches a QR code of it, and by default opens the link in this computer's browser too (open_browser false skips that).
2. Give the user the link and the QR code. They open it on any device, sign in to Hyper3D if asked and click Authorize.
3. A browser on this computer finishes the sign-in by itself. On another device the browser ends on a page that cannot connect to 127.0.0.1: ask the user for that page's whole address and call login with it as redirect_url.
4. Call get_account again.

```json
{"redirect_url": "http://127.0.0.1:5368x/callback?code=..."}
```

- A link works for 10 minutes. A new sign-in replaces the current session.
- An invalid_input answer to a pasted address says what to do: ask for the whole address again, or start a new sign-in when the link expired.
- Call logout only when the user asks: it revokes the grant and deletes the tokens; the task index and downloaded files stay.

## 3. Permissions

Each operation that spends credits or changes a task has one [permission] key in config.toml. Every key is allow until the user sets it to "deny".

| Key | Tools |
| --- | --- |
| generate | generate_model |
| redo | redo_geometry, redo_material, select_candidate |
| confirm_geometry | confirm_geometry |
| generate_texture | generate_texture |
| confirm_material | confirm_material |
| export | export_model |
| fork | fork_task |
| agentic | generate_agentic |
| split_parts | split_parts |
| rig | rig_model |
| animate | animate_model |
| texture_model | texture_model |

- The other tools need no key: reading, quoting, downloading, previews, the viewer and set_private.
- A denied call answers forbidden and names the key and the file. Ask the user whether to allow it; they set the key to "allow" (or remove the line) and the next call reads it. Do not retry before that.
- get_account shows every value; quote_cost shows the key and value of one operation.
