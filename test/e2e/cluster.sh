#!/usr/bin/env bash
# Creates a disposable kind cluster for v8s end-to-end tests, seeded with integrations and
# deliberately broken workloads so every Problems class has something to find.
#
# It never touches ~/.kube/config: the kubeconfig is written to test/e2e/out/kubeconfig.
#
# usage: test/e2e/cluster.sh up [--with-kubescape]   create and seed
#        test/e2e/cluster.sh down                     delete
#        test/e2e/cluster.sh cilium-up                a second cluster with Cilium + Hubble
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
out="$here/out"
mkdir -p "$out"
export KUBECONFIG="$out/kubeconfig"
KIND="${KIND:-kind}"
name="v8s-e2e"

seed_broken() {
    kubectl create namespace payments --dry-run=client -o yaml | kubectl apply -f -
    kubectl create namespace search --dry-run=client -o yaml | kubectl apply -f -
    kubectl create namespace reporting --dry-run=client -o yaml | kubectl apply -f -
    kubectl apply -f "$here/fixtures/broken.yaml"
}

seed_integrations() {
    # Flux: controllers plus a healthy and a broken source/kustomization.
    flux install --components-extra=image-reflector-controller,image-automation-controller
    kubectl apply -f "$here/fixtures/flux.yaml"

    # Helm: one healthy release and one that fails its upgrade.
    helm repo add podinfo https://stefanprodan.github.io/podinfo >/dev/null
    helm repo add kyverno https://kyverno.github.io/kyverno >/dev/null
    helm repo add aqua https://aquasecurity.github.io/helm-charts >/dev/null
    helm repo update >/dev/null
    helm upgrade --install podinfo podinfo/podinfo -n podinfo --create-namespace --wait
    helm upgrade --install broken-release podinfo/podinfo -n podinfo --set image.tag=does-not-exist --timeout 30s --wait || true

    # Kyverno with an audit policy that some fixtures violate.
    helm upgrade --install kyverno kyverno/kyverno -n kyverno --create-namespace --set admissionController.replicas=1 --wait
    kubectl apply -f "$here/fixtures/kyverno.yaml"

    # Trivy Operator (scans images in the cluster; needs internet access).
    helm upgrade --install trivy-operator aqua/trivy-operator -n trivy-system --create-namespace \
        --set trivy.ignoreUnfixed=true --set operator.scanJobsConcurrentLimit=2 --wait
}

seed_kubescape() {
    helm repo add kubescape https://kubescape.github.io/helm-charts/ >/dev/null
    helm repo update >/dev/null
    helm upgrade --install kubescape kubescape/kubescape-operator -n kubescape --create-namespace \
        --set clusterName="$name" --set capabilities.continuousScan=enable --wait --timeout 10m
}

case "${1:-}" in
up)
    "$KIND" create cluster --name "$name" --config "$here/kind.yaml" --kubeconfig "$KUBECONFIG"
    seed_broken
    seed_integrations
    [[ "${2:-}" == "--with-kubescape" ]] && seed_kubescape
    echo "ready: KUBECONFIG=$KUBECONFIG"
    ;;
down)
    "$KIND" delete cluster --name "$name" --kubeconfig "$KUBECONFIG"
    "$KIND" delete cluster --name "$name-cilium" --kubeconfig "$KUBECONFIG" 2>/dev/null || true
    ;;
cilium-up)
    "$KIND" create cluster --name "$name-cilium" --config "$here/kind-cilium.yaml" --kubeconfig "$KUBECONFIG"
    helm repo add cilium https://helm.cilium.io/ >/dev/null
    helm repo update >/dev/null
    helm upgrade --install cilium cilium/cilium -n kube-system \
        --set hubble.relay.enabled=true --set hubble.enabled=true --set ipam.mode=kubernetes \
        --set kubeProxyReplacement=true --set k8sServiceHost="$name-cilium-control-plane" --set k8sServicePort=6443 \
        --set operator.replicas=1 --wait --timeout 10m
    kubectl apply -f "$here/fixtures/cilium.yaml"
    echo "ready: KUBECONFIG=$KUBECONFIG (context kind-$name-cilium)"
    ;;
*)
    echo "usage: $0 up [--with-kubescape] | down | cilium-up" >&2
    exit 2
    ;;
esac
