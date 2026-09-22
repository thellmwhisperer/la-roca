package securefile

import "sync"

var publicationMutex sync.Mutex

func lockPublication(dir string) (func() error, error) {
	publicationMutex.Lock()
	releaseDirectory, err := lockDirectory(dir)
	if err != nil {
		publicationMutex.Unlock()
		return nil, err
	}
	return func() error {
		directoryErr := releaseDirectory()
		publicationMutex.Unlock()
		return directoryErr
	}, nil
}
