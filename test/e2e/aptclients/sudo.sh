#!/bin/sh
# The client pods run as root, and the setup snippets of the format write below /etc/apt with sudo: this runs its arguments.
exec "$@"
