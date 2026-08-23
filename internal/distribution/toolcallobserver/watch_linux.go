//go:build linux

package toolcallobserver

import "golang.org/x/sys/unix"

func watchFile(path string, notify chan<- struct{}) func() {
	stop := make(chan struct{})
	fd, err := unix.InotifyInit()
	if err != nil {
		return func() {}
	}
	if _, err := unix.InotifyAddWatch(fd, path, unix.IN_MODIFY|unix.IN_ATTRIB|unix.IN_MOVE_SELF|unix.IN_DELETE_SELF); err != nil {
		unix.Close(fd)
		return func() {}
	}
	go func() {
		defer unix.Close(fd)
		buf := make([]byte, unix.SizeofInotifyEvent+unix.NAME_MAX+1)
		for {
			select {
			case <-stop:
				return
			default:
			}
			n, err := unix.Read(fd, buf)
			if err != nil {
				return
			}
			if n < unix.SizeofInotifyEvent {
				continue
			}
			select {
			case notify <- struct{}{}:
			default:
			}
		}
	}()
	return func() {
		close(stop)
		unix.Close(fd)
	}
}
