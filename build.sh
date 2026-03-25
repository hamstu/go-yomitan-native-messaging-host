#!/bin/bash

# Create builds directory
mkdir -p builds

echo "Building Yomitan Native Messaging Host for all platforms..."

# macOS
echo "Building for macOS (Intel)..."
GOOS=darwin GOARCH=amd64 go build -o builds/yomitan-host-darwin-amd64
echo "Building for macOS (Apple Silicon)..."
GOOS=darwin GOARCH=arm64 go build -o builds/yomitan-host-darwin-arm64

# Windows
echo "Building for Windows (64-bit)..."
GOOS=windows GOARCH=amd64 go build -o builds/yomitan-host-windows-amd64.exe
echo "Building for Windows (32-bit)..."
GOOS=windows GOARCH=386 go build -o builds/yomitan-host-windows-386.exe

# Linux
echo "Building for Linux (64-bit)..."
GOOS=linux GOARCH=amd64 go build -o builds/yomitan-host-linux-amd64
echo "Building for Linux (32-bit)..."
GOOS=linux GOARCH=386 go build -o builds/yomitan-host-linux-386
echo "Building for Linux (ARM)..."
GOOS=linux GOARCH=arm go build -o builds/yomitan-host-linux-arm
echo "Building for Linux (ARM64)..."
GOOS=linux GOARCH=arm64 go build -o builds/yomitan-host-linux-arm64

echo "Build complete! Check the 'builds' directory."
ls -lh builds/