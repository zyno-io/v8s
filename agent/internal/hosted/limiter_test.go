package hosted

import (
	"testing"
	"time"

	"golang.org/x/time/rate"
	authv1 "k8s.io/api/authorization/v1"
)

func reviewOf(verb string) *authv1.SelfSubjectAccessReview {
	return &authv1.SelfSubjectAccessReview{Spec: authv1.SelfSubjectAccessReviewSpec{
		ResourceAttributes: &authv1.ResourceAttributes{Verb: verb, Resource: "pods", Version: "v1"}}}
}

func newClusterLimiter() *rate.Limiter { return rate.NewLimiter(1e9, 1e9) }

func TestReadLimiterPerUserAndClusterCeiling(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	clock := func() time.Time { return now }
	cluster := rate.NewLimiter(100, 200) // per-cluster ceiling shared by every user

	user := func() *compositeLimiter { return newCompositeLimiter(20, 40, cluster, clock) }
	a := user()
	n := 0
	for a.TryAccept() {
		n++
		if n > 1000 {
			break
		}
	}
	if n != 40 {
		t.Fatalf("one user's burst is %d, want 40", n)
	}
	now = now.Add(time.Second) // refills 20 tokens
	n = 0
	for a.TryAccept() {
		n++
	}
	if n != 20 {
		t.Fatalf("after 1 s a user regains %d tokens, want 20", n)
	}

	// Users share the per-cluster ceiling: the cluster has 200 burst minus the 60 already taken (plus refill).
	now = now.Add(10 * time.Second) // cluster bucket full (200), user buckets full
	total := 0
	for i := 0; i < 10; i++ {
		u := user()
		for u.TryAccept() {
			total++
		}
	}
	if total != 200 {
		t.Fatalf("ten users drew %d requests from one cluster, want the 200 ceiling", total)
	}
	// A cluster at its ceiling still has the user's own bucket intact (no token leaked by a failed reserve).
	now = now.Add(time.Second) // cluster +100, so five users can each pull 20
	u := user()
	got := 0
	for u.TryAccept() {
		got++
	}
	if got != 40 {
		t.Fatalf("user got %d after the cluster refilled, want 40", got)
	}
	if user().QPS() != 20 {
		t.Fatal("QPS() must report the per-user rate")
	}
}
