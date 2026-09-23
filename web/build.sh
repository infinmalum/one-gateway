#!/bin/sh
set -eu

version=$(cat VERSION)
pwd

while IFS= read -r theme; do
    echo "Building theme: $theme"
    cd "$theme"
    npm ci --legacy-peer-deps
    REACT_APP_VERSION=$version npm run build
    cd ..
done < THEMES
