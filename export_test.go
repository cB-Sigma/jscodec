package jscodec

import "context"

// HardOnly makes the worker run without the in-call limits, so only the
// hard (process) limits stop a runaway script.
func HardOnly(l Limits) Limits { l.hardOnly = true; return l }

// WorkerPIDs lists the codec's running worker processes.
func WorkerPIDs(c *Codec) []int { return c.proc.pids() }

// CrashWorker makes a worker exit in the middle of a call.
func CrashWorker(ctx context.Context, c *Codec) error {
	_, err := c.proc.do(ctx, &wireReq{Op: "crash"})
	return err
}

// SandboxProbe reports what a worker may do: bit 0 set when socket()
// is refused, bit 1 when opening a file is refused, bit 2 when the
// worker sees no network interface but loopback.
func SandboxProbe(ctx context.Context, c *Codec) (int, error) { return c.proc.probe(ctx) }
