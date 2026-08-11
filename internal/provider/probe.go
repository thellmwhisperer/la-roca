package provider

import "context"

// ProbeModel sends one minimal real request through the candidate provider.
// Catalogue membership proves only that an ID exists; this request proves the
// current credential can actually execute it. Adapter errors are returned
// unchanged so the operator sees the server's own bounded response body.
func ProbeModel(ctx context.Context, candidate Provider) error {
	_, err := candidate.Chat(ctx, ChatRequest{
		Messages: []Message{{
			Role:    RoleUser,
			Content: "Reply with one character.",
		}},
		MaxTokens: 1,
	})
	return err
}
