package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// STS and explicitly loaded TTS share one resident process slot.
const stsIdleLifetime = 75 * time.Second

type stsProcess struct {
	cmd        *exec.Cmd
	input      io.WriteCloser
	output     io.ReadCloser
	exited     chan struct{}
	key        string
	model      string
	log        string
	timer      *time.Timer
	generation uint64
	pinned     bool
	instance   string
	tts        *ttsTarget
}

func (s *studio) stopSTSWorkerLocked() {
	p := s.stsWorker
	if p == nil {
		return
	}
	s.stsWorker = nil
	if p.timer != nil {
		p.timer.Stop()
	}
	_ = p.input.Close()
	_ = p.cmd.Process.Kill()
	<-p.exited
}
func (s *studio) runSTSWorker(ctx context.Context, root, model string, src []byte, env []string, payload []byte, target *stsPreview, trace io.Writer) (err error) {
	cmd := exec.Command(filepath.Join(root, "runtime", "python.exe"), "-u", "-c", string(src), "--sts-resident")
	cmd.Dir, cmd.Env = root, env
	return s.runSTSProcess(ctx, root+"\x00"+model, defaultSTS, cmd, payload, target, trace)
}

func (s *studio) runSTSProcess(ctx context.Context, token, modelID string, cmd *exec.Cmd, payload []byte, target *stsPreview, trace io.Writer, tts ...*ttsTarget) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	p := s.stsWorker
	if p != nil {
		select {
		case <-p.exited:
			s.stopSTSWorkerLocked()
			p = nil
		default:
		}
		if p != nil && p.key != token {
			if p.pinned {
				s.mu.Unlock()
				return errors.New("請先停止或確認切換常駐模型。")
			}
			s.stopSTSWorkerLocked()
			p = nil
		}
	}
	if p == nil {
		logKind := "sts"
		if len(tts) > 0 {
			logKind = "tts"
		}
		logPath := s.path("logs", logKind+"-worker-"+target.id+".log")
		f, e := os.Create(logPath)
		if e != nil {
			s.mu.Unlock()
			return e
		}
		stdin, e := cmd.StdinPipe()
		if e != nil {
			f.Close()
			s.mu.Unlock()
			return e
		}
		stdout, e := cmd.StdoutPipe()
		if e != nil {
			stdin.Close()
			f.Close()
			s.mu.Unlock()
			return e
		}
		cmd.Stderr = f
		if e = cmd.Start(); e != nil {
			stdin.Close()
			stdout.Close()
			f.Close()
			s.mu.Unlock()
			return e
		}
		p = &stsProcess{cmd: cmd, input: stdin, output: stdout, exited: make(chan struct{}), key: token, model: modelID, log: filepath.Base(logPath), instance: id()}
		if len(tts) > 0 {
			p.tts = tts[0]
		}
		go func(q *stsProcess) { _ = q.cmd.Wait(); _ = f.Close(); close(q.exited) }(p)
		s.stsWorker = p
	}
	if p.timer != nil {
		p.timer.Stop()
	}
	p.generation++
	generation := p.generation
	s.mu.Unlock()
	fmt.Fprintln(trace, "Resident worker:", p.log)
	cancel := context.AfterFunc(ctx, func() { _ = p.cmd.Process.Kill() })
	defer func() {
		stopped := cancel()
		s.mu.Lock()
		defer s.mu.Unlock()
		if err != nil || !stopped {
			if s.stsWorker == p {
				s.stopSTSWorkerLocked()
			}
			return
		}
		p.timer = time.AfterFunc(stsIdleLifetime, func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.stsWorker == p && p.generation == generation && !p.pinned && !s.busy() {
				s.stopSTSWorkerLocked()
			}
		})
	}()
	if _, err = p.input.Write(append(payload, '\n')); err != nil {
		return err
	}
	var h [5]byte
	for {
		if _, err = io.ReadFull(p.output, h[:]); err != nil {
			return err
		}
		n := binary.LittleEndian.Uint32(h[1:])
		switch {
		case h[0] == 'D' && n == 0:
			return ctx.Err()
		case h[0] == 'E' && n == 0:
			return fmt.Errorf("Resident worker %s failed; see logs/%s", modelID, p.log)
		case h[0] == 'P' && len(tts) == 0 && n > 0 && n <= stsPCMCap:
			b := make([]byte, int(n))
			if _, err = io.ReadFull(p.output, b); err != nil {
				return err
			}
			if _, err = target.Write(b); err != nil {
				return err
			}
		default:
			return errors.New("invalid STS frame")
		}
	}
}
