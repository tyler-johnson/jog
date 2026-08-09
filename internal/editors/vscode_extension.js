// jog — a memory for your working tree. Installed by `jog editors install`;
// `jog editors uninstall vscode` removes it.
//
// Snapshots the repo after every save by running `jog editor-hook vscode
// <file>`. The hook exits in milliseconds outside a git repo. The child
// is detached with all stdio ignored, so saves never wait on jog — and a
// missing or moved jog is swallowed by the error handler. The path below
// is baked by the installer because GUI editors often launch without the
// shell's PATH.
const vscode = require("vscode")
const { spawn } = require("child_process")

const JOG = "{{JOG}}"

function activate(context) {
  context.subscriptions.push(
    vscode.workspace.onDidSaveTextDocument((doc) => {
      if (doc.uri.scheme !== "file") return // untitled, settings, remote, …
      try {
        const child = spawn(JOG, ["editor-hook", "vscode", doc.uri.fsPath], {
          detached: true,
          stdio: "ignore",
        })
        child.on("error", () => {}) // ENOENT and friends: never surface
        child.unref()
      } catch (e) {}
    })
  )
}

module.exports = { activate, deactivate: () => {} }
