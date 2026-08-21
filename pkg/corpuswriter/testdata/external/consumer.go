package corpuswriterconsumer

import (
	"context"
	"database/sql"

	"github.com/thellmwhisperer/la-roca/pkg/corpuswriter"
)

func write(ctx context.Context, tx *sql.Tx) (corpuswriter.Counts, error) {
	return corpuswriter.Write(ctx, tx, corpuswriter.Records{
		Sessions: []corpuswriter.Session{{
			ID:          "synthetic-session",
			SourceAgent: "synthetic-agent",
			Exchanges: []corpuswriter.Exchange{{
				Number:    1,
				HumanText: "question",
				AgentText: "answer",
			}},
		}},
	})
}
