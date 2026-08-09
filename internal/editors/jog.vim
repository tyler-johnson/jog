" jog — a memory for your working tree. Installed by `jog editors install`;
" `jog editors uninstall vim` (or nvim) removes it.
"
" Snapshots the repo after every file save by running `jog editor-hook`.
" The hook exits in milliseconds outside a git repo, so firing on every
" write is safe. Async wherever this editor has jobs, and absent for the
" whole session when jog is not on PATH — a snapshot must never interrupt
" a save.

if exists('g:loaded_jog') || !executable('jog')
  finish
endif
let g:loaded_jog = 1

" One file serves vim and neovim; the name tells jog which saved.
let s:editor = has('nvim') ? 'nvim' : 'vim'

function! s:JogSave(path) abort
  " Real files only: writes that land nowhere on disk never reach jog.
  if a:path ==# '' || !filereadable(a:path)
    return
  endif
  if exists('*jobstart')                " neovim
    call jobstart(['jog', 'editor-hook', s:editor, a:path])
  elseif exists('*job_start')           " vim 8+
    call job_start(['jog', 'editor-hook', s:editor, a:path])
  else                                  " vim 7: shell-backgrounded, still silent
    silent call system('jog editor-hook ' . s:editor . ' '
          \ . shellescape(a:path) . ' >/dev/null 2>&1 &')
  endif
endfunction

augroup jog
  autocmd!
  autocmd BufWritePost * call s:JogSave(expand('<afile>:p'))
augroup END
