;;; jog.el --- a memory for your working tree  -*- lexical-binding: t; -*-
;; Installed by `jog editors install`; `jog editors uninstall emacs`
;; removes it. Emacs has no drop-in autoload directory, so load it from
;; your init file with:
;;
;;   (load "~/.emacs.d/jog.el" t)
;;
;; (the t keeps emacs quiet if this file is ever removed).
;;
;; Snapshots the repo after every save by running `jog editor-hook emacs
;; <file>`. The hook exits in milliseconds outside a git repo.
;; start-process keeps saves instant, and the guards keep a missing or
;; moved jog from ever surfacing in the editor. The path below is baked
;; by the installer because GUI editors often launch without the shell's
;; PATH.

(defconst jog--program "{{JOG}}"
  "How this install invokes jog, chosen by `jog editors install`.")

(defun jog--after-save ()
  "Hand the saved file to jog, silently and in the background."
  (when (and buffer-file-name
             (or (file-executable-p jog--program)
                 (executable-find jog--program)))
    (ignore-errors
      (let ((process-connection-type nil)) ; pipe, not pty — cheaper
        (start-process "jog" nil jog--program
                       "editor-hook" "emacs" buffer-file-name)))))

(add-hook 'after-save-hook #'jog--after-save)

(provide 'jog)
;;; jog.el ends here
