package router

import "context"

type RequestMeta struct {
	Listener     string
	ListenerPort string
	ServiceMode  string
}

type requestMetaKey struct{}

func WithRequestMeta(ctx context.Context, meta RequestMeta) context.Context {
	return context.WithValue(ctx, requestMetaKey{}, meta)
}

func RequestMetaFromContext(ctx context.Context) RequestMeta {
	if ctx == nil {
		return RequestMeta{}
	}
	if meta, ok := ctx.Value(requestMetaKey{}).(RequestMeta); ok {
		return meta
	}
	return RequestMeta{}
}
