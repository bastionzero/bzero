#!/bin/sh

rootDir="${PWD}"

echo "Running agent tests..."
cd $rootDir/bctl/agent
go test -v ./... -timeout 5000ms

echo "Running daemon tests..."
cd $rootDir/bctl/daemon
go test -v ./... -timeout 5000ms

#echo "Running bzerolib tests..."
#cd $rootDir/bzerolib
#go test -v ./...