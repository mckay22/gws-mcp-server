package main

import "context"

// actorCtxKey is the context key under which the resource-server middleware
// stashes the verified requesting actor (the human who made the call). A private
// zero-size type prevents collisions.
type actorCtxKey struct{}

// actorLocal is the actor recorded when there is no verified caller — i.e. the
// stdio/classic-delegated mode, where the operator runs the server locally.
const actorLocal = "local"

// withActor returns a copy of ctx carrying the verified requesting actor.
func withActor(ctx context.Context, actor string) context.Context {
	return context.WithValue(ctx, actorCtxKey{}, actor)
}

// actorFromContext returns the requesting actor previously attached by
// withActor, or actorLocal when none is present (stdio mode). This is the
// identity logged against every applied application-tier mutation — Google's own
// audit attributes a DWD action to the impersonated user, so this log is where
// the real requester is recorded.
func actorFromContext(ctx context.Context) string {
	if a, ok := ctx.Value(actorCtxKey{}).(string); ok && a != "" {
		return a
	}
	return actorLocal
}
