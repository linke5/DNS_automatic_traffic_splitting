package router

import "context"

type RequestMeta struct {
	Listener     string
	ListenerPort string
	ServiceMode  string
	ReturnMode   string
}

type requestMetaKey struct{}
type requestMetaHolderKey struct{}

func WithRequestMeta(ctx context.Context, meta RequestMeta) context.Context {
	holder := &meta
	ctx = context.WithValue(ctx, requestMetaKey{}, meta)
	ctx = context.WithValue(ctx, requestMetaHolderKey{}, holder)
	return ctx
}

func RequestMetaFromContext(ctx context.Context) RequestMeta {
	if ctx == nil {
		return RequestMeta{}
	}
	if holder, ok := ctx.Value(requestMetaHolderKey{}).(*RequestMeta); ok && holder != nil {
		return *holder
	}
	if meta, ok := ctx.Value(requestMetaKey{}).(RequestMeta); ok {
		return meta
	}
	return RequestMeta{}
}
