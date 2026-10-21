package main

//go:generate go run github.com/atombender/go-jsonschema@v0.24.1 --only-models --package wasmprotocol --min-sized-ints --capitalization ID --output ../../internal/wasmprotocol/protocol.go protocol.schema.json
