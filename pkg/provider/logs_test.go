package provider_test

import (
	"context"
	"io"
	"testing"
	"time"

	"kuack-node/pkg/provider"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtual-kubelet/virtual-kubelet/node/api"
)

func TestPodLogStream_WriteAndRead_NoFollow(t *testing.T) {
	t.Parallel()

	stream := provider.NewPodLogStream()
	msg := "log message\n"
	_, err := stream.Write([]byte(msg))
	require.NoError(t, err)

	reader := stream.Subscribe(false)

	defer func() {
		assert.NoError(t, reader.Close())
	}()

	buf := make([]byte, 1024)
	n, err := reader.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, msg, string(buf[:n]))

	n, err = reader.Read(buf)
	assert.Equal(t, 0, n)
	assert.Equal(t, io.EOF, err)
}

func TestPodLogStream_WriteAndRead_Follow(t *testing.T) {
	t.Parallel()

	stream := provider.NewPodLogStream()
	initialMsg := "initial log\n"
	_, err := stream.Write([]byte(initialMsg))
	require.NoError(t, err)

	reader := stream.Subscribe(true)

	defer func() {
		assert.NoError(t, reader.Close())
	}()

	// Read initial logs
	buf := make([]byte, 1024)
	n, err := reader.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, initialMsg, string(buf[:n]))

	// Write more logs in a goroutine
	go func() {
		time.Sleep(10 * time.Millisecond)

		_, err := stream.Write([]byte("more logs\n"))
		assert.NoError(t, err)
	}()

	// Read more logs
	n, err = reader.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, "more logs\n", string(buf[:n]))
}

func TestWASMProvider_GetContainerLogs(t *testing.T) {
	t.Parallel()

	p, err := provider.NewWASMProvider("node-1")
	require.NoError(t, err)

	// 1. Get logs (follow=true)
	reader, err := p.GetContainerLogs(context.Background(), "default", "pod-1", "container-1", api.ContainerLogOpts{Follow: true})
	require.NoError(t, err)

	defer func() {
		assert.NoError(t, reader.Close())
	}()

	// 2. Append logs
	go func() {
		time.Sleep(10 * time.Millisecond)
		p.AppendPodLog("default", "pod-1", "log line 1")
		p.AppendPodLog("default", "pod-1", "log line 2")
	}()

	// 3. Read logs
	buf := make([]byte, 1024)

	// Read first line
	n, err := reader.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, "log line 1\n", string(buf[:n]))

	// Read second line
	n, err = reader.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, "log line 2\n", string(buf[:n]))
}
