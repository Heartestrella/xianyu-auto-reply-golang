package scheduler

import (
	"context"

	"github.com/zhinianboke/xianyu-auto-go/internal/xianyu"
)

func (e *Engine) callMTOP(ctx context.Context, options xianyu.MTOPOptions) xianyu.MTOPResult {
	if e.mtopCall != nil {
		return e.mtopCall(ctx, options)
	}
	return xianyu.MTOPCall(ctx, options)
}
