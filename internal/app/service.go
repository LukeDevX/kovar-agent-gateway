package app

import (
	"context"
	"encoding/json"
	"errors"

	"kovar-gateway/internal/binding"
	"kovar-gateway/internal/identity"
	"kovar-gateway/internal/platform/httpx"
	"kovar-gateway/internal/task"
)

type Service struct {
	Identity *identity.Service
	Binding  *binding.Service
	Tasks    *task.Service
}

func (s *Service) AdminAgents(ctx context.Context, page, size int, whitelist string) (any, error) {
	agents, err := s.Identity.List(ctx, page, size, whitelist)
	if err != nil {
		return nil, err
	}
	addresses := make([]string, len(agents))
	for i, a := range agents {
		addresses[i] = a.Address
	}
	bound, err := s.Binding.Summaries(ctx, addresses)
	if err != nil {
		return nil, err
	}
	usage, err := s.Tasks.Summaries(ctx, addresses)
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(agents))
	for _, a := range agents {
		items = append(items, merge(a, bound[a.Address], usage[a.Address]))
	}
	return map[string]any{"items": items, "page": page, "page_size": size}, nil
}
func (s *Service) AdminAgent(ctx context.Context, address string) (any, error) {
	a, err := s.Identity.Get(ctx, address)
	if err != nil {
		return nil, err
	}
	bound, err := s.Binding.Summaries(ctx, []string{address})
	if err != nil {
		return nil, err
	}
	sum, err := s.Tasks.Summary(ctx, address)
	if err != nil {
		return nil, err
	}
	out := merge(a, bound[address], sum)
	t, err := s.Binding.Token(ctx, address)
	if err == nil {
		for k, v := range merge(t) {
			out[k] = v
		}
	} else {
		var e *httpx.Error
		if !errors.As(err, &e) || e.Code != "KOVAR_TOKEN_NOT_BOUND" {
			out["token_refresh_error"] = httpx.Normalize(err).Code
		}
	}
	return out, nil
}
func (s *Service) Review(ctx context.Context, admin identity.Admin, address, action, remark string) (any, error) {
	a, err := s.Identity.Review(ctx, admin, address, action, remark)
	if err != nil {
		return nil, err
	}
	out := merge(a)
	if action == "revoke" {
		if err = s.Binding.DeleteKey(ctx, address); err != nil {
			out["token_deletion_pending"] = true
			out["token_deletion_error"] = httpx.Normalize(err).Code
		}
	}
	return out, nil
}
func merge(values ...any) map[string]any {
	out := map[string]any{}
	for _, v := range values {
		b, _ := json.Marshal(v)
		var m map[string]json.RawMessage
		_ = json.Unmarshal(b, &m)
		for k, x := range m {
			out[k] = x
		}
	}
	return out
}

func (s *Service) Cleanup(ctx context.Context) error {
	if err := s.Identity.Cleanup(ctx); err != nil {
		return err
	}
	return s.Binding.Cleanup(ctx)
}
