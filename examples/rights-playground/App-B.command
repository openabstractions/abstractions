#!/bin/sh
set -eu
exec "${0%/*}/Playground B" menu --connection "${0%/*}/run/connection.json"
