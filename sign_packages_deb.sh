#!/bin/sh

if [ "x${SIGN_PACKAGES}" = "x1" ]; then
    PATH="$HOME/bin:$PATH" debsigs --sign=origin -v --default-key='Platform9 Systems' "$@"
fi
