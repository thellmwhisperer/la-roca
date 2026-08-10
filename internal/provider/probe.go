/**
 * @overview Owns the real account-level model probe. ~45 lines, 1 public symbol.
 *
 *   READING GUIDE
 *   -------------
 *   1. Start at ProbeModel
 *   2. The request literal is the complete probe budget
 *   3. Provider adapters retain ownership of transport errors
 *
 *   MAIN FLOW
 *   Provider -> one-token Chat -> nil or provider error
 *
 *   PUBLIC API
 *   ----------
 *   ProbeModel()  Proves the configured account can execute the model
 *
 *   INTERNALS
 *   ---------
 *   None
 *
 * @exports ProbeModel
 * @deps context; provider.Provider and ChatRequest contracts
 */
package provider

import "context"

// -- 1/1 CORE · ProbeModel <- START HERE --

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

// -/ 1/1
