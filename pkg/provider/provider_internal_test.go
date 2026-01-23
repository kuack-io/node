package provider

import (
	"context"
	"strings"
	"testing"

	"kuack-node/pkg/registry"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
)

func TestProcessEnvVars(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		wasmConfig *registry.WasmConfig
		container  corev1.Container
		want       []AgentEnvVar
	}{
		{
			name: "basic image and pod envs",
			wasmConfig: &registry.WasmConfig{
				Env: []string{"IMAGE_VAR=image_val"},
			},
			container: corev1.Container{
				Env: []corev1.EnvVar{
					{Name: "POD_VAR", Value: "pod_val"},
				},
			},
			want: []AgentEnvVar{
				{Name: "POD_VAR", Value: "pod_val"},
				{Name: "IMAGE_VAR", Value: "image_val"},
			},
		},
		{
			name: "pod env overrides image env",
			wasmConfig: &registry.WasmConfig{
				Env: []string{"COMMON_VAR=image_val"},
			},
			container: corev1.Container{
				Env: []corev1.EnvVar{
					{Name: "COMMON_VAR", Value: "pod_val"},
				},
			},
			want: []AgentEnvVar{
				{Name: "COMMON_VAR", Value: "pod_val"},
			},
		},
		{
			name: "filter kubernetes service envs",
			wasmConfig: &registry.WasmConfig{
				Env: []string{"APP_CONFIG=v1", "KUBERNETES_SERVICE_HOST=10.0.0.1"},
			},
			container: corev1.Container{
				Env: []corev1.EnvVar{
					{Name: "OTHER_SERVICE_PORT", Value: "9090"}, // Should be filtered
					{Name: "DB_CONNECTION_STRING", Value: "db://..."},
				},
			},
			want: []AgentEnvVar{
				{Name: "APP_CONFIG", Value: "v1"},
				{Name: "DB_CONNECTION_STRING", Value: "db://..."},
			},
		},
		{
			name: "truncate long env values",
			wasmConfig: &registry.WasmConfig{
				Env: []string{"LONG_IMAGE_VAR=" + strings.Repeat("a", 2000)},
			},
			container: corev1.Container{
				Env: []corev1.EnvVar{
					{Name: "LONG_POD_VAR", Value: strings.Repeat("b", 2000)},
				},
			},
			want: []AgentEnvVar{
				{Name: "LONG_POD_VAR", Value: strings.Repeat("b", 1024)},
				{Name: "LONG_IMAGE_VAR", Value: strings.Repeat("a", 1024)},
			},
		},
		{
			name:       "empty config and container",
			wasmConfig: &registry.WasmConfig{},
			container:  corev1.Container{},
			want:       []AgentEnvVar{},
		},
		{
			name: "invalid image env format",
			wasmConfig: &registry.WasmConfig{
				Env: []string{"INVALID_FORMAT"},
			},
			container: corev1.Container{},
			want:      []AgentEnvVar{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := processEnvVars(tt.wasmConfig, tt.container)
			assert.ElementsMatch(t, tt.want, got)
		})
	}
}

func TestProcessEnvVars_Limit(t *testing.T) {
	t.Parallel()
	// Create many env vars
	imageEnvs := make([]string, 0, 150)
	for range 150 {
		imageEnvs = append(imageEnvs, "IMG_VAR_"+strings.Repeat("0", 3)+"="+strings.Repeat("v", 5))
	}

	podEnvs := []corev1.EnvVar{
		{Name: "POD_VAR_1", Value: "val1"},
		{Name: "POD_VAR_2", Value: "val2"},
	}

	wasmConfig := &registry.WasmConfig{Env: imageEnvs}
	container := corev1.Container{Env: podEnvs}

	got := processEnvVars(wasmConfig, container)

	assert.LessOrEqual(t, len(got), 100)

	// Pod envs should be preserved
	foundPod1 := false
	foundPod2 := false

	for _, env := range got {
		if env.Name == "POD_VAR_1" {
			foundPod1 = true
		}

		if env.Name == "POD_VAR_2" {
			foundPod2 = true
		}
	}

	assert.True(t, foundPod1, "POD_VAR_1 should be preserved")
	assert.True(t, foundPod2, "POD_VAR_2 should be preserved")
}

func TestUnimplementedMethods(t *testing.T) {
	t.Parallel()

	p := &WASMProvider{}
	ctx := context.Background()

	err := p.RunInContainer(ctx, "ns", "pod", "container", nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not implemented")

	err = p.AttachToContainer(ctx, "ns", "pod", "container", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not implemented")
}

func TestPing(t *testing.T) {
	t.Parallel()

	p := &WASMProvider{}
	err := p.Ping(context.Background())
	require.NoError(t, err)
}

func TestIsKubernetesServiceEnv(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		val  string
		want bool
	}{
		{"kubernetes service host", "KUBERNETES_SERVICE_HOST", true},
		{"kubernetes service port", "KUBERNETES_SERVICE_PORT", true},
		{"kubernetes port", "KUBERNETES_PORT", true},
		{"random service host", "MY_SERVICE_HOST", true}, // Suffix match
		{"random service port", "MY_SERVICE_PORT", true}, // Suffix match
		{"port var", "SOME_PORT", true},                  // Suffix match _PORT
		{"prefix match", "KUBERNETES_PORT_443_TCP", true},
		{"service host suffix", "MY_SERVICE_SERVICE_HOST", true},
		{"normal var", "MY_VAR", false},
		{"path var", "MY_PATH", false}, // Contains PATH, exception for _PORT check?
		// Logic: strings.HasSuffix(name, "_PORT") && !strings.Contains(name, "PATH")
		{"port path", "MY_PORT_PATH", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, isKubernetesServiceEnv(tt.val))
		})
	}
}
