//go:build !linux && !darwin

package model

import "os"

func verifiedIdentity(info os.FileInfo, checksum string) string { return "" }
func lockVerification(file *os.File) (func(), error)            { return func() {}, nil }
func readVerification(file *os.File) string                     { return "" }
func writeVerification(file *os.File, identity string)          {}
