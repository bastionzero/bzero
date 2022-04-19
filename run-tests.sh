#!/bin/sh

rootDir="${PWD}"

echo "Running agent tests..."
cd $rootDir/bctl/agent
go test -v ./...

#echo "Running daemon tests..."
#cd $rootDir/bctl/daemon
#go test -v ./...

#echo "Running bzerolib tests..."
#cd $rootDir/bzerolib
#go test -v ./...