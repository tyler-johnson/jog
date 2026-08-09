-- jog — a memory for your working tree. Installed by `jog editors install`;
-- `jog editors uninstall micro` removes it.
--
-- Snapshots the repo after every save by running `jog editor-hook micro
-- <file>`. The hook exits in milliseconds outside a git repo. JobSpawn
-- is micro's async runner; with nil callbacks the output vanishes and a
-- missing jog fails without a trace.

VERSION = "1.0.0"

local shell = import("micro/shell")

function onSave(bp)
    local path = bp.Buf.AbsPath
    if path ~= nil and path ~= "" then
        shell.JobSpawn("jog", {"editor-hook", "micro", path}, nil, nil, nil)
    end
    return false
end
