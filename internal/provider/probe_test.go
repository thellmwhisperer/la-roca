package provider

import (
	"context"
	"errors"
	"testing"
)

type probeProvider struct {
	err      error
	requests []ChatRequest
}

func (p *probeProvider) Name() string    { return "fake" }
func (p *probeProvider) ModelID() string { return "fake-model" }
func (p *probeProvider) Ready(context.Context) Readiness {
	return Readiness{Ready: true, ModelID: p.ModelID()}
}
func (p *probeProvider) Models(context.Context) ModelReport {
	return ModelReport{Ready: true, Models: []string{p.ModelID()}}
}
func (p *probeProvider) Chat(_ context.Context, req ChatRequest) (ChatResponse, error) {
	p.requests = append(p.requests, req)
	return ChatResponse{}, p.err
}

func TestProbeModelUsesOneRealMinimalRequest(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		fake := &probeProvider{}
		if err := ProbeModel(context.Background(), fake); err != nil {
			t.Fatal(err)
		}
		if len(fake.requests) != 1 {
			t.Fatalf("requests = %d, want one", len(fake.requests))
		}
		request := fake.requests[0]
		if request.MaxTokens != 1 || len(request.Messages) != 1 || request.Messages[0].Role != RoleUser {
			t.Fatalf("probe request is not minimal: %+v", request)
		}
	})

	t.Run("provider rejection", func(t *testing.T) {
		serverErr := errors.New(`server said: model "fake-model" is not available`)
		fake := &probeProvider{err: serverErr}
		if err := ProbeModel(context.Background(), fake); !errors.Is(err, serverErr) {
			t.Fatalf("error = %v, want the provider's own error", err)
		}
		if len(fake.requests) != 1 {
			t.Fatalf("requests = %d, want one", len(fake.requests))
		}
	})
}
