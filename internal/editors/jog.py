# jog — a memory for your working tree. Installed by `jog editors install`;
# `jog editors uninstall sublime` removes it.
#
# Snapshots the repo after every save by running `jog editor-hook sublime
# <file>`. The hook exits in milliseconds outside a git repo.
# on_post_save_async runs on Sublime's worker thread and the process is
# fully detached with every fd on DEVNULL, so saves never wait on jog —
# and a missing or moved jog is swallowed whole. The path below is baked
# by the installer because GUI editors often launch without the shell's
# PATH.

import subprocess

import sublime_plugin

JOG = "{{JOG}}"


class JogListener(sublime_plugin.EventListener):
    def on_post_save_async(self, view):
        path = view.file_name()
        if not path:
            return
        try:
            subprocess.Popen(
                [JOG, "editor-hook", "sublime", path],
                stdin=subprocess.DEVNULL,
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
                start_new_session=True,
            )
        except OSError:
            pass
