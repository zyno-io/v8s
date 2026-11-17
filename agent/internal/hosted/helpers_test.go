package hosted

import (
	"io"
	"log/slog"
	"testing"

	"github.com/zyno-io/v8s/agent/internal/hosted/config"
	"sigs.k8s.io/yaml"
)

func mustYAML(t *testing.T, c *config.Config) []byte {
	t.Helper()
	b, err := yaml.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newTestLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug}))
}
