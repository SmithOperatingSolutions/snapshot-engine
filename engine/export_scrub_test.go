package engine

import "context"

// Scrub exposes scrub to the tests.
func Scrub(ctx context.Context, l Logger, err error) error { return scrub(ctx, l, err) }
