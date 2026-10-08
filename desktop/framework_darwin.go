package main

// #cgo LDFLAGS: -framework UniformTypeIdentifiers
import "C"

// Wails 2.15 file dialogs reference UTType; link the framework explicitly.
