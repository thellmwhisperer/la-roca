package ingest

import (
	"strings"

	"github.com/thellmwhisperer/la-roca/pkg/parsers"
)

// QualifySessionID namespaces a remote session identity so the same native id
// on two machines does not share a primary key. Local ids stay unchanged.
func QualifySessionID(machine, id string) string {
	machine = strings.TrimSpace(machine)
	if machine == "" || id == "" {
		return id
	}
	return machine + "/" + id
}

func owningRoots(opts Options, target Target) Roots {
	if target.Remote {
		for _, remote := range opts.Roots.Remotes {
			if remote.Machine == target.Machine && remote.Home == target.RootHome {
				return remote
			}
		}
	}
	return opts.Roots
}

func labelRecords(target Target, records *parsers.Records) {
	if target.Machine == "" {
		return
	}
	for i := range records.Sessions {
		session := &records.Sessions[i]
		session.Machine = target.Machine
		if !target.Remote {
			continue
		}
		session.ID = QualifySessionID(target.Machine, session.ID)
		if session.ParentID != "" {
			session.ParentID = QualifySessionID(target.Machine, session.ParentID)
		}
	}
}
