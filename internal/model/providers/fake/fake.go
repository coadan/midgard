package fake

import (
	"context"
	"fmt"
	"sync"

	"midgard/internal/model"
)

type Response struct {
	Text  string
	Usage model.Usage
}

type Provider struct {
	mu        sync.Mutex
	responses []Response
	calls     int
	packets   []model.Packet
}

func New(responses ...Response) *Provider {
	return &Provider{responses: responses}
}

func (p *Provider) ID() string {
	return "fake"
}

func (p *Provider) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func (p *Provider) Packets() []model.Packet {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]model.Packet(nil), p.packets...)
}

func (p *Provider) Stream(ctx context.Context, packet model.Packet, emit func(model.Delta) error) (model.Usage, error) {
	_ = ctx
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.calls >= len(p.responses) {
		return model.Usage{}, fmt.Errorf("fake response %d not configured", p.calls)
	}
	response := p.responses[p.calls]
	p.calls++
	p.packets = append(p.packets, packet)
	if err := emit(model.Delta{Text: response.Text}); err != nil {
		return model.Usage{}, err
	}
	return response.Usage, nil
}
