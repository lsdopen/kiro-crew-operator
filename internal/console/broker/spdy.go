package broker

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	clientexec "k8s.io/client-go/util/exec"
)

// maxOutput bounds what is read from the command.
const maxOutput = 16 << 10

// SPDYExecer execs through the API server's pods/exec over SPDY (a POST, so RBAC
// needs `create`). Stdin and the TTY are off, and stderr is discarded.
type SPDYExecer struct {
	Config *rest.Config
	Client kubernetes.Interface
}

// Exec runs command and returns its stdout. Errors carry the exit code at most.
func (e *SPDYExecer) Exec(ctx context.Context, namespace, pod, container string, command []string) ([]byte, error) {
	req := e.Client.CoreV1().RESTClient().Post().
		Resource("pods").Namespace(namespace).Name(pod).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container, Command: command, Stdout: true,
		}, scheme.ParameterCodec)
	ex, err := remotecommand.NewSPDYExecutor(e.Config, "POST", req.URL())
	if err != nil {
		return nil, fmt.Errorf("building exec: %w", err)
	}
	out := &cappedBuffer{max: maxOutput}
	err = ex.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: out})
	var exit clientexec.ExitError
	switch {
	case errors.As(err, &exit):
		return nil, fmt.Errorf("command exited %d", exit.ExitStatus())
	case err != nil:
		// The stream error can echo server text, never command output.
		return nil, fmt.Errorf("exec stream: %w", err)
	}
	return out.Bytes(), nil
}

// cappedBuffer fails a write past max instead of growing without bound.
type cappedBuffer struct {
	bytes.Buffer
	max int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.max {
		return 0, errors.New("output too large")
	}
	return b.Buffer.Write(p)
}
