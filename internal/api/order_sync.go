package api

import (
	"context"
	"github.com/zhinianboke/xianyu-auto-go/internal/xianyu"
)

func (s *Server) callOrderMTOP(ctx context.Context, options xianyu.MTOPOptions) xianyu.MTOPResult {
	if s.orderMTOPCall != nil {
		return s.orderMTOPCall(ctx, options)
	}
	return xianyu.MTOPCall(ctx, options)
}
