package registry_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"kuack-node/pkg/registry"
)

func TestResolveWasmConfig_Bindgen(t *testing.T) {
	t.Parallel()

	pkgJson := map[string]string{
		"name": "my-app",
		"main": "my-app.js",
	}
	pkgJsonBytes, err := json.Marshal(pkgJson)
	require.NoError(t, err)

	layer := createLayer(t, map[string][]byte{
		"pkg/package.json": pkgJsonBytes,
	})

	img, err := mutate.AppendLayers(empty.Image, layer)
	require.NoError(t, err)

	p := registry.NewProxy()
	p.SetImageFetcher(func(ref name.Reference, options ...remote.Option) (v1.Image, error) {
		return img, nil
	})

	config, err := p.ResolveWasmConfig(context.Background(), "my-app:latest")
	require.NoError(t, err)

	assert.Equal(t, &registry.WasmConfig{
		Type:    "bindgen",
		Path:    "pkg/my-app_bg.wasm",
		Variant: "wasm32/wasi",
	}, config)
}

func TestResolveWasmConfig_WASI(t *testing.T) {
	t.Parallel()

	layer := createLayer(t, map[string][]byte{
		"output.wasm": []byte("wasm-magic"),
	})

	img, err := mutate.AppendLayers(empty.Image, layer)
	require.NoError(t, err)

	p := registry.NewProxy()
	p.SetImageFetcher(func(ref name.Reference, options ...remote.Option) (v1.Image, error) {
		return img, nil
	})

	config, err := p.ResolveWasmConfig(context.Background(), "kuack/hello:latest")
	require.NoError(t, err)

	assert.Equal(t, &registry.WasmConfig{
		Type:    "wasi",
		Path:    "/output.wasm",
		Variant: "wasm32/wasi",
	}, config)
}

func TestResolveWasmConfig_Fallback(t *testing.T) {
	t.Parallel()

	layer := createLayer(t, map[string][]byte{
		"readme.md": []byte("hello"),
	})

	img, err := mutate.AppendLayers(empty.Image, layer)
	require.NoError(t, err)

	p := registry.NewProxy()
	p.SetImageFetcher(func(ref name.Reference, options ...remote.Option) (v1.Image, error) {
		return img, nil
	})

	config, err := p.ResolveWasmConfig(context.Background(), "kuack/checker:latest")
	require.NoError(t, err)

	// Fallback guesses based on default, not image name unless file exists
	assert.Equal(t, &registry.WasmConfig{
		Type:    "wasi",
		Path:    "/output.wasm",
		Variant: "wasm32/wasi",
	}, config)
}

func TestResolveWasmConfig_DerivedWasm(t *testing.T) {
	t.Parallel()

	// If image is test/my-app, it should look for /my-app.wasm
	layer := createLayer(t, map[string][]byte{
		"my-app.wasm": []byte("magic"),
	})
	img, err := mutate.AppendLayers(empty.Image, layer)
	require.NoError(t, err)

	p := registry.NewProxy()
	p.SetImageFetcher(func(ref name.Reference, options ...remote.Option) (v1.Image, error) {
		return img, nil
	})

	config, err := p.ResolveWasmConfig(context.Background(), "test/my-app:1.0")
	require.NoError(t, err)

	assert.Equal(t, &registry.WasmConfig{
		Type:    "wasi",
		Path:    "/my-app.wasm",
		Variant: "wasm32/wasi",
	}, config)
}
