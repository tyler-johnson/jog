// jog — a memory for your working tree. Installed by `jog editors install`;
// `jog editors uninstall vscode` removes it.
//
// Snapshots the repo after every save by running `jog editor-hook vscode
// <file>`. The hook exits in milliseconds outside a git repo. The child
// is detached with all stdio ignored, so saves never wait on jog — and a
// missing or moved jog is swallowed by the error handler. The path below
// is baked by the installer because GUI editors often launch without the
// shell's PATH; when it fails (VS Code's own install-on-remote flow
// copies this file to machines where that path means nothing), the hook
// falls back to bare `jog` from the extension host's PATH.
const vscode = require("vscode")
const { spawn } = require("child_process")

const JOG = "{{JOG}}"

function run(cmd, path) {
  try {
    const child = spawn(cmd, ["editor-hook", "vscode", path], {
      detached: true,
      stdio: "ignore",
    })
    child.on("error", () => {
      if (cmd !== "jog") run("jog", path) // baked path failed: PATH gets one try
    })
    child.unref()
  } catch (e) {}
}

function activate(context) {
  context.subscriptions.push(
    vscode.workspace.onDidSaveTextDocument((doc) => {
      if (doc.uri.scheme !== "file") return // untitled, settings, output, …
      run(JOG, doc.uri.fsPath)
    })
  )
}

module.exports = { activate, deactivate: () => {} }
