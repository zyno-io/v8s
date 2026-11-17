package streams

import (
	"crypto/rand"

	"github.com/zyno-io/v8s/agent/internal/kube"
)

const alnum = "abcdefghijklmnopqrstuvwxyz0123456789"

func randSuffix(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = alnum[int(b[i])%len(alnum)]
	}
	return string(b)
}

type kubeRuntime = kube.Runtime
