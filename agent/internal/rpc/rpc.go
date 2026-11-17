// Package rpc defines the handler shape modules use to add request methods to the wire server without the
// wire package knowing them (security.*, hubble.*).
package rpc

import "context"

// Handler serves one request method. decode unmarshals the request params into v (it fails when the request
// carried none). The result is sent as the response; an error is mapped to an error response (use
// ops.NewError for protocol error codes).
type Handler func(ctx context.Context, decode func(v any) error) (any, error)
