# rodin3d-mcp

An MCP server, command line and terminal app for **Rodin**, the image-to-3D
service of [Hyper3D](https://hyper3d.ai). It lets an AI agent (and you) turn
one or more images into a 3D model through the same staged flow as the Rodin
web app: a geometry draft first, then confirm it, texture it, confirm the
material, and export the files. It works on your own Hyper3D account, with
the plan and credits you already have.

- **Every step of the web flow:** Gen-2.5 tiers and Gen-1.5 modes, 1-5 input
  images tagged by view (front, back, left, ...) with weights, redo and
  confirm of geometry and material, export and download, forks, batches.
- **More Rodin tools:** Agentic builds, splitting a draft into parts, rigging
  and animation presets, and texturing a model file of your own.
- **Previews as images:** the server's renders of each stage and local renders
  of the stage's mesh in every viewer mode of the web app (normal, classic,
  glossy, copper, metal, AO, wireframe, PBR, shaded, toon, depth, ...).
- **A viewer window** the agent opens when you ask to see something: the
  model, its stages, the input images with their view tags, the texture maps.
  You and the agent both move and change the view.
- **Money stays in your hands:** a paid call runs only at the price the agent
  was quoted, and any paid or changing operation you deny is refused.

rodin3d-mcp is one binary for Windows, macOS and Linux (amd64 and arm64). It
needs no browser at run time and no other software.

## Install

The releases ship as binaries; install from a release (`go install` does not
work for this module, see [Building](#building)).

**Linux and macOS:**

```sh
curl -fsSL https://github.com/sairaph/rodin3d-mcp/releases/latest/download/install.sh | sh
```

**Windows (PowerShell):**

```powershell
irm https://github.com/sairaph/rodin3d-mcp/releases/latest/download/install.ps1 | iex
```

The script downloads the binary for your system, checks it against the
release's `SHA256SUMS.txt`, puts it on your PATH (`~/.rodin3d-mcp/bin`, or
`%LOCALAPPDATA%\rodin3d-mcp\bin` on Windows) and starts the setup:

1. **AI clients:** pick the clients on this computer that should use the
   server; each one is registered as `rodin3d` running `rodin3d-mcp mcp`.
2. **Sign in** to Hyper3D (below).
3. **Permissions:** every operation starts allowed; deny the ones the agent
   should not run (below).
4. **Guide skill:** after the registration, the `rodin3d` skill is written
   into the skill folder each registered client reads ([Guide
   skill](#guide-skill)).

Run the setup again at any time with `rodin3d-mcp install`.
`rodin3d-mcp install --yes --all` registers every detected client without
questions; `rodin3d-mcp add` registers the server in the current project's
client configs only; `--dry-run` shows what would change.
Restart your AI client after the setup so it loads the server.

`rodin3d-mcp update` updates to the latest release and `rodin3d-mcp doctor`
checks the installation.

## Uninstall

`rodin3d-mcp uninstall` removes everything the installers and the program
put on this computer. It lists all of it first and asks before removing
anything (`--yes` skips the question, `--dry-run` only lists):

1. Signs out: revokes the Hyper3D grant and deletes the tokens, like
   `logout`. Without a network the grant cannot be revoked; its token is
   still deleted, so nothing on the computer can use it.
2. Stops the background syncer and closes the viewer window.
3. Removes the `rodin3d` entry from every AI client config that has it,
   and nothing else in those files, and the `rodin3d` guide skill copies it
   wrote into the clients' skill folders.
4. Deletes `~/.rodin3d-mcp`: the task index, downloads, previews, cache,
   permissions, sign-in and logs, and on Linux and macOS the program in
   `~/.rodin3d-mcp/bin`. Model files you downloaded to a folder of your own
   stay.
5. On Windows, deletes `%LOCALAPPDATA%\rodin3d-mcp` and removes its `bin`
   folder from your user PATH. On Linux and macOS, removes the PATH line
   the installer added to your shell profile.

The running program goes last. Windows cannot delete a running program, so
on Windows `rodin3d-mcp.exe` stays and the summary names the folder to
delete afterwards. The summary also reports anything that could not be
removed and any other `rodin3d-mcp` still on your PATH.

Registrations and guide skills made with `rodin3d-mcp add` live in that
project's own files: remove them first with
`rodin3d-mcp uninstall --scope project --dir <project>`.

## Sign in

rodin3d-mcp signs in with Hyper3D's own authorization page; it never sees
your password.

- The setup and the app open the sign-in link in the browser of this
  computer. Press `c` to copy the link for another device instead; a QR code
  of it shows when the terminal is large enough. `rodin3d-mcp login` prints
  the link (and the QR code in a terminal) and opens the browser.
- Sign in to Hyper3D there if asked (with any method the site offers) and
  click **Authorize** on the page "Connect rodin3d-mcp to Hyper3D task API".
  The page says Hyper3D cannot verify the app: rodin3d-mcp registered itself
  as an app of your account, which is expected.
- If that browser runs on this computer, the sign-in finishes by itself.
- On another device (a phone, or when this computer has no browser, such as
  over SSH) the browser ends on a page that cannot connect to `127.0.0.1`.
  Copy that page's whole address, press `p` in the setup and paste it, or run
  `rodin3d-mcp login "<address>"` (quote it).

The session is kept in `~/.rodin3d-mcp/credentials.json`, readable only by
you, and renewed in the background. `rodin3d-mcp logout` revokes it on
Hyper3D and deletes it here. An agent can also start a sign-in with the
`login` tool and hand you the link.

## Permissions

Reads (tasks, previews, downloads, quotes) are always allowed. Each operation
that spends credits or changes a task has one key in
`~/.rodin3d-mcp/config.toml`, and every key is `allow` until you set it to
`deny`:

```toml
[permission]
generate         = "allow" # generate_model
redo             = "allow" # redo_geometry, redo_material, select_candidate
confirm_geometry = "allow" # confirm_geometry
generate_texture = "allow" # generate_texture
confirm_material = "allow" # confirm_material
export           = "allow" # export_model
fork             = "allow" # fork_task
agentic          = "allow" # generate_agentic
split_parts      = "allow" # split_parts
rig              = "allow" # rig_model
animate          = "allow" # animate_model
texture_model    = "allow" # texture_model
```

Only `"deny"` blocks an operation: without the file, without a key, with a
file that cannot be read or parsed, and with any other value the operation is
allowed. An unknown key or a wrong value counts only for itself; the other
keys still apply (`get_account` and `rodin3d-mcp doctor` report a file with a
problem). Set
them in the setup, under **Permissions** in the app (`rodin3d-mcp` with no
arguments), or by editing the file; every call reads it again, so a change
applies at once. A denied call fails and names the key to change.

On top of that, every paid tool takes `expected_credits`: it runs only when
the current price equals the quote the agent got from `quote_cost`, so a
price never changes behind the agent's back.

## Tools

| Tool | What it does |
|---|---|
| `get_account` | Account, plan, wallet, permissions, session and sync state |
| `login`, `logout` | Start or finish a sign-in (link, QR code, pasted address); sign out |
| `get_generation_options` | Every setting with its values and plan gates, and recipes from your history |
| `quote_cost` | The price of an operation before it runs |
| `list_tasks`, `get_task`, `wait_task` | Your generations, one task's settings, inputs, files and next actions, waiting for a running step |
| `get_preview` | A picture of a task stage: a server image or a local render in any viewer mode and view |
| `generate_model` | A geometry draft from 1-5 images with view tags and weights, Gen-2.5 or Gen-1.5 |
| `redo_geometry`, `confirm_geometry`, `select_candidate` | Redo, confirm or pick the geometry draft |
| `generate_texture`, `redo_material`, `confirm_material` | Texture the confirmed geometry, redo or confirm the material |
| `export_model`, `download_model` | Pack the files (GLB, FBX, OBJ, USDZ, STL; resolutions by plan) and download them |
| `fork_task`, `set_private` | Branch a task from a stage; make a task private or public |
| `generate_agentic` | An Agentic build from images |
| `split_parts` | Split a geometry draft into parts |
| `rig_model`, `animate_model`, `list_animation_presets` | Rig a model (templates, own skeleton or FBX bones) and export it with animations |
| `texture_model` | Texture a model file of your own |
| `open_viewer`, `show_in_viewer`, `control_viewer`, `get_viewer_state`, `close_viewer` | The viewer window |
| `get_guide` | The usage guide by topic (below) |

Each result carries the task's stage and the actions it allows, and the tools
that change a stage attach a preview.

## Guide skill

rodin3d-mcp ships a short guide that teaches an agent to use these tools
well: sign in, quote before every paid call, never resend a call that timed
out, tag the input views, walk the stages and show each draft to you, and
what to do for each error. It is the `rodin3d` skill, in topics:

| Topic | What it covers |
|---|---|
| `start` | Account, sign-in with the link and QR code, permissions |
| `credits` | `quote_cost`, `expected_credits`, timeouts, free operations, frozen credits |
| `inputs` | Images, view tags, weights, input sets from the viewer |
| `settings` | Models, tiers, modes, recipes and every stage's settings |
| `staged-flow` | Draft, redo, confirm, texture, export, download, fork, waiting |
| `other-modes` | Agentic builds, parts, rigging, animation, texturing your own model |
| `viewing` | `get_preview` modes and the viewer window |
| `privacy-and-errors` | Private tasks and what to do for each error code |

- **As a skill:** `install` and `add` write it into the skill folder each
  registered client reads, one copy per folder: `.claude/skills` for Claude
  Code (and Cursor, OpenCode and VS Code beside it), `.agents/skills` for
  Codex, Gemini CLI and the other clients, `.kiro/skills` for Kiro and
  `.continue/skills` for Continue; under your home folder, or under the
  project for `add`. Claude Desktop takes skills only as an upload, so the
  setup says so instead.
- **As a tool:** `get_guide` serves the same topics to any client, Claude
  Desktop included, without signing in; the server instructions point agents
  to it. `get_guide` with no arguments lists the topics.
- Only a copy whose `SKILL.md` carries the `generator: rodin3d-mcp` marker is
  ever replaced or removed: a `rodin3d` skill without it (one you wrote, or a
  copy whose marker line you removed to keep your edits) stays as it is. `rodin3d-mcp update` rewrites the copies it wrote with the
  new version's guide, and `uninstall` removes them.

## Viewer

The viewer is a window of rodin3d-mcp itself, not a web page. The agent opens
it with `open_viewer` when you ask to see something, and can show a task at
any stage, two tasks side by side, a task's input images with their view tags
and weights, its texture maps, or a local model file (GLB, glTF, OBJ, STL).
You orbit, pan and zoom with the mouse, switch modes and stages, retag and
reorder input images; the agent reads what you see (with a screenshot) and
changes the view too. The window stays open until you close it.

On Linux the viewer needs a desktop session (X11 or XWayland); everything
else runs without a display.

## App and command line

`rodin3d-mcp` with no arguments in a terminal opens the app: your tasks with
their details and previews, the account and wallet, the permissions, sign-in
and sign-out, the viewer and the doctor, with the sync state at the bottom.

Every tool also has a one-shot command with the same results, for example:

```sh
rodin3d-mcp account
rodin3d-mcp tasks chair
rodin3d-mcp task <id>
rodin3d-mcp preview <id> pbr three_quarter out.png
rodin3d-mcp quote generate_model tier=medium
rodin3d-mcp generate front.png:F back.png:B tier=medium expected=<quoted credits>
rodin3d-mcp download <id> ./models
```

Add `json` to print the result as JSON. `rodin3d-mcp help` lists every command
and its options.

## Data

Everything lives in `~/.rodin3d-mcp`: `config.toml` (permissions),
`credentials.json` (the session), `rodin.db` (the task index), `cache/`
(stage images), `previews/`, `downloads/` and the logs. A background syncer
(`rodin3d-mcp daemon`) keeps the task index current and renews the session;
any command or tool call starts it, and `rodin3d-mcp daemon --stop` stops it.
New tasks are created private.

## Building

```sh
git clone https://github.com/sairaph/rodin3d-mcp
cd rodin3d-mcp
CGO_ENABLED=0 go build -trimpath .
```

Go 1.27.1 or later. Builds are pure Go (`CGO_ENABLED=0`) and cross-compile to
all six targets from one machine. `go.mod` replaces one module with a local
directory (`third_party/hideconsole`), which is why `go install ...@latest`
does not work; build from a clone or install a release.

The Draco mesh decoder runs as an embedded WebAssembly module;
`tools/draco-wasm` rebuilds it reproducibly from pinned sources.

## Licenses

rodin3d-mcp is MIT-licensed (see `LICENSE`).

`THIRD_PARTY_NOTICES.txt` (shipped with every release, and printed by
`rodin3d-mcp licenses`) holds the licenses of the third-party code in the
binary: the Go standard library, every linked Go module, and the Draco
decoder module (Google Draco, wasi-libc and the LLVM C++ runtimes;
`internal/draco/NOTICE` says which parts). `go run ./tools/notices`
regenerates it.

Rodin and Hyper3D are trademarks of their owners. This project is not
affiliated with or endorsed by Hyper3D.
