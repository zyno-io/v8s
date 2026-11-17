package engine

import "testing"

// The Hubble problem monitor asks for the client count from its own goroutine; before the wire server exists
// that must answer 0 rather than dereference nil.
func TestClientCountBeforeServerExists(t *testing.T) {
	e := &Engine{}
	if n := e.clientCount(); n != 0 {
		t.Fatalf("client count before the server exists: %d", n)
	}
}
