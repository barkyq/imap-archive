#!/bin/zsh
if [[ -z "$1" ]]; then
    cat $HOME/mail/.conf_paths | imap-archive
else
    array=()
    for arg; do
        array+=("$(grep -m1 -i "$arg" $HOME/mail/.conf_paths)")
    done
    print -l $array | imap-archive
fi