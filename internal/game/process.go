package game

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"syscall"
)

type child struct {
	cmd  *exec.Cmd
	done chan struct{}
}

type Processes struct {
	children []*child
	fail     context.CancelCauseFunc
}

func NewProcesses(fail context.CancelCauseFunc) *Processes { return &Processes{fail: fail} }
func (p *Processes) start(name string, args []string, stdin io.Reader, stdout io.Writer) (*os.Process, error) {
	cmd := exec.Command(name, args...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c := &child{cmd: cmd, done: make(chan struct{})}
	p.children = append(p.children, c)
	slog.Info("started child", "name", name, "pid", cmd.Process.Pid)
	go func() {
		err := cmd.Wait()
		if err == nil {
			err = errors.New("unexpected clean exit")
		}
		p.fail(fmt.Errorf("%s: %w", name, err))
		close(c.done)
	}()
	return cmd.Process, nil
}

func (p *Processes) Stop(ctx context.Context) {
	for i := len(p.children) - 1; i >= 0; i-- {
		c := p.children[i]
		group := -c.cmd.Process.Pid
		_ = syscall.Kill(group, syscall.SIGTERM)
		select {
		case <-c.done:
		case <-ctx.Done():
		}
		// A helper can outlive its process-group leader.
		_ = syscall.Kill(group, syscall.SIGKILL)
		<-c.done
	}
}
