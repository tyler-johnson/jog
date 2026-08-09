// jog — a memory for your working tree. Installed by `jog agents install`;
// `jog agents uninstall opencode` removes it.
//
// Snapshots the repo before every prompt and mutating tool call by piping
// the event to `jog hook opencode`. Every handler swallows its own
// failures — a broken snapshot must never block the agent.
const MUTATING = new Set(["bash", "edit", "write", "patch", "multiedit"])

export const JogPlugin = async ({ $, directory }) => {
  const hook = async (payload) => {
    try {
      const json = JSON.stringify(payload)
      const out = await $`echo ${json} | jog hook opencode`
        .cwd(directory)
        .quiet()
        .nothrow()
        .text()
      return (out || "").trim()
    } catch {
      return ""
    }
  }
  return {
    "chat.message": async (input, output) => {
      let prompt = ""
      try {
        const part = (output.parts || []).find((p) => p && p.type === "text" && p.text)
        if (part) prompt = part.text
      } catch {}
      const notice = await hook({
        hook_event_name: "chat.message",
        session_id: (input && input.sessionID) || "",
        prompt,
      })
      // jog introduces itself once per session; the notice rides into the
      // model's context as an extra message part.
      if (notice) {
        try {
          output.parts.push({ type: "text", text: notice })
        } catch {}
      }
    },
    "tool.execute.before": async (input, output) => {
      if (!MUTATING.has(String((input && input.tool) || "").toLowerCase())) return
      await hook({
        hook_event_name: "tool.execute.before",
        session_id: (input && input.sessionID) || "",
        tool_name: input.tool,
        tool_input: (output && output.args) || {},
      })
    },
  }
}
