#!/bin/bash

# The basename of our binary
BASE="simple-vpn"

# Get the dependencies
go get -t -v -d $(go list ./...)


#
# Function to do a build
#
function do_build {

    export GOOS=$1
    export GOARCH=$2

    if [ "${GOARCH}" = "arm64" ]; then
        export GOOS=""
        export GOARM=7
    fi

    OUT=$3
    echo "OUT: $OUT"
    go build -ldflags "-X main.version=$(git describe --tags)" -o "${OUT}"
}

#
# Linux
#
do_build linux amd64 "${BASE}-linux-amd64"
do_build linux 386 "${BASE}-linux-i386"

#
# ARM
#
do_build arm64 arm64  "${BASE}-arm64"
