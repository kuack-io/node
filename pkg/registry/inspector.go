package registry

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"k8s.io/klog/v2"
)

// WasmConfig describes how to execute a WASM image.
type WasmConfig struct {
	Type    string   `json:"type"` // "bindgen" or "wasi"
	Path    string   `json:"path"`
	Variant string   `json:"variant"` // defaults to "wasm32/wasi"
	Env     []string `json:"env,omitempty"`
}

// Resolver resolves WASM configuration for an image.
type Resolver interface {
	ResolveWasmConfig(ctx context.Context, imageRef string) (*WasmConfig, error)
}

// ResolveWasmConfig inspects the image to determine the WASM configuration.
func (p *Proxy) ResolveWasmConfig(ctx context.Context, imageRef string) (*WasmConfig, error) {
	klog.Infof("[Registry] Resolving WASM config for: %s", imageRef)

	ref, err := name.ParseReference(imageRef, name.WithDefaultTag("latest"))
	if err != nil {
		return nil, fmt.Errorf("failed to parse reference: %w", err)
	}

	// Fetch image metadata (manifest)
	opts := []remote.Option{
		remote.WithContext(ctx),
		remote.WithUserAgent(registryUserAgent),
		remote.WithPlatform(v1.Platform{OS: "wasi", Architecture: "wasm32"}), // Prefer WASM variant
		remote.WithTransport(p.transport),
	}

	img, err := p.imageFetcher(ref, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch image metadata: %w", err)
	}

	// Fetch image config (env vars, entrypoint, etc.)
	configFile, err := img.ConfigFile()
	if err != nil {
		klog.Warningf("[Registry] Failed to fetch config file for %s: %v", imageRef, err)
	}

	var imageEnv []string
	if configFile != nil {
		imageEnv = configFile.Config.Env
	}

	// Check for wasm-pack structure (pkg/package.json)
	// We iterate layers looking for this specific file.
	// This involves downloading layer streams. We stop as soon as we find what we need.

	config, found, err := p.inspectForBindgen(img)
	if err != nil {
		klog.Warningf("[Registry] Error inspecting for bindgen: %v", err)
	}

	if found {
		klog.Infof("[Registry] Detected bindgen layout for %s: %+v", imageRef, config)
		config.Env = imageEnv

		return config, nil
	}

	// Check for common container2wasm / standalone WASM files
	return p.inspectImageContent(img, imageRef, imageEnv)
}

func (p *Proxy) inspectImageContent(img v1.Image, imageRef string, env []string) (*WasmConfig, error) {
	layers, err := img.Layers()
	if err != nil {
		return nil, err
	}

	// Scan for:
	// - pkg/package.json (Bindgen)
	// - /output.wasm, /out.wasm, /<name>.wasm (WASI)

	// Default fallback
	bestGuess := &WasmConfig{
		Type:    "wasi",
		Path:    "/output.wasm", // Standard c2w output
		Variant: "wasm32/wasi",
		Env:     env,
	}

	// Derive name from image ref for fallback detection (e.g. "checker" from "kuack/checker")
	imageName := "unknown"
	if parts := strings.Split(imageRef, "/"); len(parts) > 0 {
		imageName = parts[len(parts)-1]
		if tagIdx := strings.Index(imageName, ":"); tagIdx != -1 {
			imageName = imageName[:tagIdx]
		}
	}

	derivedWasm := fmt.Sprintf("/%s.wasm", imageName)

	// Iterate layers from top to bottom
	for i := len(layers) - 1; i >= 0; i-- {
		layer := layers[i]

		rc, err := layer.Uncompressed()
		if err != nil {
			return nil, err
		}

		defer func() {
			err := rc.Close()
			if err != nil {
				klog.Warningf("failed to close layer reader: %v", err)
			}
		}()

		tr := tar.NewReader(rc)
		for {
			header, err := tr.Next()
			if err == io.EOF {
				break
			}

			if err != nil {
				return nil, err
			}

			// Normalize path
			cleanPath := normalizeArtifactPath(header.Name)

			// Check for pkg/package.json
			if cleanPath == "pkg/package.json" {
				// Parse it to find the wasm path
				var pkg struct {
					Main string `json:"main"`
				}

				err := json.NewDecoder(tr).Decode(&pkg)
				if err == nil && pkg.Main != "" {
					// Found bindgen!
					wasmFile := strings.Replace(pkg.Main, ".js", "_bg.wasm", 1)

					return &WasmConfig{
						Type:    "bindgen",
						Path:    path.Join("pkg", wasmFile),
						Variant: "wasm32/wasi",
						Env:     env,
					}, nil
				}
			}

			// Check for WASI candidates
			if strings.HasSuffix(cleanPath, ".wasm") {
				if cleanPath == "output.wasm" || cleanPath == "out.wasm" || cleanPath == "main.wasm" || "/"+cleanPath == derivedWasm {
					// We found a strong candidate for WASI.
					// We'll store it but keep scanning for package.json in case it's a hybrid or we missed it.
					bestGuess.Path = "/" + cleanPath
					bestGuess.Type = "wasi"
				}
			}
		}
	}

	klog.Infof("[Registry] Fallback detection for %s: %+v", imageRef, bestGuess)

	return bestGuess, nil
}

func (p *Proxy) inspectForBindgen(img v1.Image) (*WasmConfig, bool, error) {
	// Helper kept for structural reference, but logic moved to single pass inspectImageContent
	return nil, false, nil
}
