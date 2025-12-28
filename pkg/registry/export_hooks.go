package registry

import (
	"context"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// SetFetchArtifact overrides the artifact retrieval logic. Tests use this to avoid real registry calls.
func (p *Proxy) SetFetchArtifact(f func(ctx context.Context, ref, artifactPath string, platform *v1.Platform) ([]byte, error)) {
	p.fetchArtifact = f
}

// SetImageFetcher overrides the image puller. Exposed for tests that need to stub registry interactions.
func (p *Proxy) SetImageFetcher(f func(ref name.Reference, options ...remote.Option) (v1.Image, error)) {
	p.imageFetcher = f
}
