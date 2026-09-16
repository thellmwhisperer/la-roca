//go:build windows

package securefile

func realStatIdentity(path string) (Identity, error) {
	return Identity{}, nil
}

func realCurrentIdentity() Identity {
	return Identity{}
}
