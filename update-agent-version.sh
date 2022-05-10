#!/bin/sh

# Set the current working directory to the directory of the script in Bash
cd "$(dirname "$0")"

VERSION="$(cat ./VERSION)"
sed -i.bu 's/\$AGENT_VERSION/'"$VERSION"'/g' ./bctl/agent/agent.go