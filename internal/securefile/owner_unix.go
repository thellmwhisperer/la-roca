//go:build !windows

package securefile

import (
	"fmt"
	"os"
	"os/user"
	"strconv"
	"syscall"
)

func realStatIdentity(path string) (Identity, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return Identity{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return Identity{}, fmt.Errorf("stat %s: no unix owner", path)
	}
	return Identity{UID: stat.Uid, GID: stat.Gid, Name: nameForUID(stat.Uid)}, nil
}

func realCurrentIdentity() Identity {
	uid := os.Geteuid()
	if uid < 0 {
		return Identity{}
	}
	return Identity{UID: uint32(uid), GID: uint32(os.Getegid()), Name: nameForUID(uint32(uid))}
}

func nameForUID(uid uint32) string {
	id := strconv.FormatUint(uint64(uid), 10)
	if current, err := user.Current(); err == nil && current.Uid == id {
		return current.Username
	}
	if account, err := user.LookupId(id); err == nil {
		return account.Username
	}
	return id
}
