#!/bin/bash

# Install the lint-tool
go get -u golang.org/x/lint/golint

# At this point failures cause aborts
set -e

# Run the linter
echo "Launching linter .."
golint -set_exit_status ./...
echo "Completed linter .."

# Run the shadow-checker
echo "Launching 'go vet' check .."
go vet  ./...
echo "Completed 'go vet' check .."

# Run golang tests
go test ./...
