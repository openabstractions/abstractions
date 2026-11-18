# Changes

## 0.8.0

### Models, chat and local inference through the router

- OpenCode follows OA's model list while it runs, through a long poll on the router.
- Models added in other applications appear in OpenCode and are hosted the same way as the shipped model.
- Model pickers show catalogue titles and never show a content digest.
- OpenCode shows loading notices while a model loads cold.
- A model load continues when the waiting caller stops waiting.
- The local title request picks a model that fits free memory and is off by default.
- Reasoning text stays out of the answer text.
- A request to an unestablished local assistant route is refused.

### Speech and images through inference

- `oa_video_generate` registers while the runtime serves a video model: it makes one video from a prompt, saves it in `Videos/OpenAbstractions` and answers its path, or a handle with what the job is doing now.
- With a text-only chat model, image attachments go through the image tool, and raw image bytes never reach OA chat.
- Image discovery names the exact required target when no image model fits.
- `oa_image_generate` needs only a prompt: it uses the account's picture model at its own size, saves the picture in the person's `Pictures/OpenAbstractions` folder and answers `Saved to <path>.`, naming the file; a wrong size is refused before anything runs, naming the allowed sizes.
- A picture for the Pictures folder, an edit or a saved policy included, is made by the gateway's `oa_image_generate` through the chat's OA connection, the same tool Claude and Codex use; a slow picture answers with a handle to call again with. `save_to` keeps saving into the project.
- The four `oa_inference_job_*` tools carry video only.

### Messages and agent tools over MCP

- OpenCode chats connect to OA Messages through the native MCP helper.
- Each chat binds to one helper that owns the canonical OA tools.
- The Messages tool catalogue is generated from MCP tool metadata.
- Agent discovery lists only conversations the caller has permission to reach.
- An open OA inbox stays open while the model list reloads and through service outages.
- OpenCode wakes only for live incoming questions and rechecks wake consent.
- A recent closed chat wakes once for messages that arrived after its last activity; messages its own turns already saw start no turn.
- Resumed chats recover their Messages context from the host tool context.
- Optional standard MCP servers can be configured beside native messaging.

### Asks, rights and project-folder access

- OpenCode Asks go through its enrolled native MCP helper.
- An enrolled OpenCode chat is woken once for a question it may answer, with the fixed text "OpenAbstractions: a question is waiting for your answer.".
- OpenCode waits for the person's answer and retries once.
- A permission wait that stays unresolved points the person to Panel Questions.
- Current permission takes priority over a retained answer.
- OpenCode can read what OA recorded about its own failed request.
- Sharing that history needs separate consent.
- The session definition for project-folder access is published with this adopter.

### Installer and configuration lifecycle

- The installer provisions an offline OpenCode first-use closure from a locked npm package set.
- An existing OpenCode user's configuration folder stays theirs, and the installer adopts their own bootstrap files.
- Ambiguous plugin, provider and MCP edits are refused before configuration is written.
- An upgrade replaces the earlier package's registration manifest.
- The installer registers the official OpenCode Desktop program after verifying it.
- OpenCode asks to register at start when its earlier request was lost.
- Preflight names elevated accounts and Setup run as administrator.
- Installer child failures keep their diagnostics.
