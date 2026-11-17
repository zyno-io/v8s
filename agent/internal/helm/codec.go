// Package helm is the agent's Helm module: the release list (a virtual curated kind built from release
// Secret labels only), lazy payload decode with a byte-budget LRU, and the rollback / uninstall / retry
// actions through the Helm SDK. It does not import wire, cluster, fake or ops: callers pass callbacks.
package helm

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	releasev1 "helm.sh/helm/v4/pkg/release/v1"
)

// maxReleaseBytes caps the decompressed size of a release payload.
const maxReleaseBytes = 64 << 20

var magicGzip = []byte{0x1f, 0x8b, 0x08}

// Secret and label names Helm writes.
const (
	ReleaseDataKey = "release"
	ReleaseType    = "helm.sh/release.v1"
	LabelOwner     = "owner"
	LabelName      = "name"
	LabelStatus    = "status"
	LabelVersion   = "version"
	LabelCreatedAt = "createdAt"
	LabelModified  = "modifiedAt"
	FluxNameLabel  = "helm.toolkit.fluxcd.io/name"
	FluxNSLabel    = "helm.toolkit.fluxcd.io/namespace"
)

// DecodeRelease decodes the Secret's "release" value (as returned by the API: Helm's own base64 text of the
// gzipped JSON; the API's base64 layer is already removed). Releases stored before Helm compressed them
// (no gzip header) are accepted. Helm 3 payloads decode with the same code.
func DecodeRelease(secretData []byte) (*releasev1.Release, error) {
	if len(secretData) == 0 {
		return nil, errors.New("empty release payload")
	}
	b, err := base64.StdEncoding.DecodeString(string(secretData))
	if err != nil {
		return nil, err
	}
	if len(b) > 3 && bytes.Equal(b[:3], magicGzip) {
		r, err := gzip.NewReader(bytes.NewReader(b))
		if err != nil {
			return nil, err
		}
		defer r.Close()
		// bounded read: a gzip bomb in a Secret must not exhaust memory
		if b, err = io.ReadAll(io.LimitReader(r, maxReleaseBytes+1)); err != nil {
			return nil, err
		}
		if len(b) > maxReleaseBytes {
			return nil, fmt.Errorf("release payload exceeds %d MiB after decompression", maxReleaseBytes>>20)
		}
	}
	var rls releasev1.Release
	if err := json.Unmarshal(b, &rls); err != nil {
		return nil, err
	}
	return &rls, nil
}

// EncodeRelease encodes a release the way Helm stores it (base64 of gzipped JSON); used by fixtures.
func EncodeRelease(r *releasev1.Release) (string, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	return encodeBytes(b)
}

func encodeBytes(b []byte) (string, error) {
	var buf bytes.Buffer
	w, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return "", err
	}
	if _, err := w.Write(b); err != nil {
		w.Close()
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}
