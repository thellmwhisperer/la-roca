//go:build !darwin && !linux

package toolcallobserver

func watchFile(string, chan<- struct{}) func() { return func() {} }
