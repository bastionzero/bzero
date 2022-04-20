#!/bin/sh

rootDir="${PWD}"

echo "Running agent tests..."
cd $rootDir/bctl/agent
go test -v ./... -timeout 5000ms  -coverprofile cover-agent.out

echo "Running daemon tests..."
cd $rootDir/bctl/daemon
go test -v ./... -timeout 5000ms -coverprofile cover-daemon.out

#echo "Running bzerolib tests..."
#cd $rootDir/bzerolib
#go test -v ./...