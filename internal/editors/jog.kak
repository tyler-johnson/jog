# jog — a memory for your working tree. Installed by `jog editors install`;
# `jog editors uninstall kakoune` removes it.
#
# Snapshots the repo after every save by running `jog editor-hook kakoune
# <file>`. The hook exits in milliseconds outside a git repo. Kakoune
# waits for a %sh{} block's stdout to close, so the job is backgrounded
# in a subshell with every fd redirected — saves never wait on jog, and a
# missing jog dies silently in the shell.

hook global BufWritePost .* %{ nop %sh{
    ( jog editor-hook kakoune "$kak_hook_param" >/dev/null 2>&1 </dev/null & )
} }
