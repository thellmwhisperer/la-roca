//go:build darwin

package toolcallobserver

import (
	"time"

	"golang.org/x/sys/unix"
)

func watchFile(path string, notify chan<- struct{}) func() {
	stop := make(chan struct{})
	go func() {
		fd, err := unix.Open(path, unix.O_EVTONLY, 0)
		if err != nil {
			return
		}
		defer unix.Close(fd)
		kq, err := unix.Kqueue()
		if err != nil {
			return
		}
		defer unix.Close(kq)
		change := unix.Kevent_t{
			Ident:  uint64(fd),
			Filter: unix.EVFILT_VNODE,
			Flags:  unix.EV_ADD | unix.EV_CLEAR,
			Fflags: unix.NOTE_WRITE | unix.NOTE_EXTEND | unix.NOTE_DELETE | unix.NOTE_RENAME,
		}
		if _, err := unix.Kevent(kq, []unix.Kevent_t{change}, nil, nil); err != nil {
			return
		}
		events := make([]unix.Kevent_t, 1)
		timeout := unix.NsecToTimespec(int64(200 * time.Millisecond))
		for {
			select {
			case <-stop:
				return
			default:
			}
			n, err := unix.Kevent(kq, nil, events, &timeout)
			if err != nil || n <= 0 {
				continue
			}
			select {
			case notify <- struct{}{}:
			default:
			}
		}
	}()
	return func() { close(stop) }
}
