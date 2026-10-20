#!/bin/sh
set -eu
exec "${0%/*}/Playground A" lifecycle Reset "$@"
