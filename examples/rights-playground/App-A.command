#!/bin/sh
set -eu
exec "${0%/*}/Playground A" menu --connection "${0%/*}/run/connection.json"
