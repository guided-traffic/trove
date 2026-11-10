#!/bin/sh
# trove-clean <command> [argument...]: runs a command of a test in a clean home, so that no state of an earlier scenario (a
# ~/.ansible cache, a ~/.netrc, an ansible.cfg, an installed collection) reaches it. HOME, ANSIBLE_HOME and the working directory are one
# new directory that is removed when the command ends; the command writes the files it needs (an ansible.cfg, a ~/.netrc) itself, and
# what it exports stays inside it. The exit status is the command's.
set -u
home="$(mktemp -d)"
trap 'rm -rf "$home"' EXIT INT TERM
cd "$home" || exit 125
HOME="$home" ANSIBLE_HOME="$home/.ansible" "$@"
