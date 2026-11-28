package command

import "context"

type Origin struct {
	Actor     string `json:"actor"`
	RequestID string `json:"request_id,omitempty"`
}
type originKey struct{}

func WithOrigin(ctx context.Context, origin Origin) context.Context {
	return context.WithValue(ctx, originKey{}, origin)
}
func Caller(ctx context.Context) Origin {
	origin, _ := ctx.Value(originKey{}).(Origin)
	if origin.Actor == "" {
		origin.Actor = "system"
	}
	return origin
}
