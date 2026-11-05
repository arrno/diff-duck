package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/arrno/diff-duck/internal/domain"
	"github.com/arrno/diff-duck/internal/services/ports"
)

const modelAttempts = 2

// errCallLimit means the run reached its limit on model calls.
var errCallLimit = errors.New("skipped: the model call limit for this run was reached")

// modelRunner makes model calls for one run. It records their cost and failures, retries
// failures that a retry could fix, and enforces the run's limit on calls, which keeps a small
// but hostile commit from spending without bound.
type modelRunner struct {
	model ports.LanguageModel
	// limit is the most calls that may be made: 0 means no limit, and negative means none.
	limit int

	mu       sync.Mutex
	inFlight int
	stats    domain.Generation
}

func (r *modelRunner) setLimit(limit int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.limit = limit
}

func (r *modelRunner) reserve() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.limit != 0 && r.stats.ModelCalls+r.inFlight >= max(r.limit, 0) {
		return false
	}
	r.inFlight++
	return true
}

func (r *modelRunner) complete(ctx context.Context, system string, schema json.RawMessage, input any) (json.RawMessage, error) {
	user, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("encode model input: %w", err)
	}
	var lastErr error
	for attempt := 0; attempt < modelAttempts; attempt++ {
		if !r.reserve() {
			return nil, errCallLimit
		}
		resp, err := r.model.Complete(ctx, ports.ModelRequest{System: system, User: string(user), Schema: schema})
		r.mu.Lock()
		r.inFlight--
		if err == nil {
			if resp.CacheHit {
				r.stats.CacheHits++
			} else {
				r.stats.ModelCalls++
				r.stats.InputTokens += resp.InputTokens
				r.stats.OutputTokens += resp.OutputTokens
				r.stats.CostUSD += resp.CostUSD
			}
			r.mu.Unlock()
			return resp.JSON, nil
		}
		r.stats.ModelCalls++
		r.stats.FailedCalls++
		r.mu.Unlock()
		lastErr = err
		if ctx.Err() != nil || errors.Is(err, ports.ErrModelRefused) {
			break
		}
	}
	return nil, lastErr
}
