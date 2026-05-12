package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math/rand"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const maxInput = 1 << 20 // matches png_read_harness MAX_INPUT

func main() {
	harness := flag.String("harness", "./png_read_harness", "path to target binary (first arg = input file)")
	corpusDir := flag.String("corpus", "./corpus", "directory with seed files")
	outDir := flag.String("out", "./simple-fuzz-out", "directory for crashes and hangs")
	timeout := flag.Duration("timeout", 2*time.Second, "per-execution timeout (hang detection)")
	workers := flag.Int("workers", 1, "parallel workers (each runs the harness)")
	seed := flag.Int64("seed", 0, "RNG seed (0 = random)")
	maxRuns := flag.Uint64("runs", 0, "stop after N executions (0 = unlimited)")
	verbose := flag.Bool("v", false, "log every execution error")
	flag.Parse()

	if *workers < 1 {
		log.Fatal("-workers must be >= 1")
	}

	seeds, err := loadCorpus(*corpusDir)
	if err != nil {
		log.Fatal(err)
	}
	if len(seeds) == 0 {
		log.Fatalf("no seed files in %q", *corpusDir)
	}

	if err := os.MkdirAll(filepath.Join(*outDir, "crashes"), 0o755); err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(*outDir, "hangs"), 0o755); err != nil {
		log.Fatal(err)
	}

	rng := rand.New(rand.NewSource(timeSeed(*seed)))

	var execCount uint64
	var crashCount uint64
	var hangCount uint64
	var interestingCount uint64

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if runtime.GOOS == "windows" {
		log.Fatal("this fuzzer uses syscall.WaitStatus; use Linux/macOS or adapt runTarget for Windows")
	}

	log.Printf("seeds=%d workers=%d harness=%s corpus=%s", len(seeds), *workers, *harness, *corpusDir)

	if *workers == 1 {
		runLoop(ctx, *harness, seeds, rng, *timeout, *outDir, *verbose, *maxRuns, &execCount, &crashCount, &hangCount, &interestingCount)
	} else {
		var wg sync.WaitGroup
		for w := 0; w < *workers; w++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				// independent RNG stream per worker
				local := rand.New(rand.NewSource(timeSeed(*seed) + int64(id)*1_000_003))
				runLoop(ctx, *harness, seeds, local, *timeout, *outDir, *verbose, *maxRuns, &execCount, &crashCount, &hangCount, &interestingCount)
			}(w)
		}
		wg.Wait()
	}

	log.Printf("done exec=%d crashes=%d hangs=%d nonzero=%d out=%s",
		atomic.LoadUint64(&execCount), atomic.LoadUint64(&crashCount), atomic.LoadUint64(&hangCount),
		atomic.LoadUint64(&interestingCount), *outDir)
}

func timeSeed(seed int64) int64 {
	if seed != 0 {
		return seed
	}
	return time.Now().UnixNano()
}

func loadCorpus(dir string) ([][]byte, error) {
	var out [][]byte
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		// skip obvious non-input
		low := strings.ToLower(filepath.Base(path))
		if strings.HasSuffix(low, ".md") || strings.HasSuffix(low, ".txt") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if len(b) == 0 {
			return nil
		}
		if len(b) > maxInput {
			b = b[:maxInput]
		}
		out = append(out, b)
		return nil
	})
	return out, err
}

func runLoop(
	ctx context.Context,
	harness string,
	seeds [][]byte,
	rng *rand.Rand,
	timeout time.Duration,
	outDir string,
	verbose bool,
	maxRuns uint64,
	execCount, crashCount, hangCount, interestingCount *uint64,
) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if maxRuns > 0 && atomic.LoadUint64(execCount) >= maxRuns {
			return
		}

		base := seeds[rng.Intn(len(seeds))]
		data := mutate(rng, base)

		res := runTarget(ctx, harness, data, timeout)
		n := atomic.AddUint64(execCount, 1)
		if n%10000 == 0 {
			log.Printf("exec=%d crashes=%d hangs=%d nonzero=%d",
				n, atomic.LoadUint64(crashCount), atomic.LoadUint64(hangCount), atomic.LoadUint64(interestingCount))
		}

		switch res.kind {
		case resultOK:
			continue
		case resultHang:
			atomic.AddUint64(hangCount, 1)
			saveArtifact(filepath.Join(outDir, "hangs"), "hang", data, res.detail)
			log.Printf("HANG %s", res.detail)
		case resultCrash:
			atomic.AddUint64(crashCount, 1)
			saveArtifact(filepath.Join(outDir, "crashes"), "crash", data, res.detail)
			log.Printf("CRASH %s", res.detail)
		case resultNonZero:
			atomic.AddUint64(interestingCount, 1)
			saveArtifact(filepath.Join(outDir, "crashes"), "exit", data, res.detail)
			log.Printf("NONZERO %s", res.detail)
		case resultError:
			if verbose {
				log.Printf("exec error: %v", res.err)
			}
		}
	}
}

type resultKind int

const (
	resultOK resultKind = iota
	resultHang
	resultCrash
	resultNonZero
	resultError
)

type runResult struct {
	kind   resultKind
	detail string
	err    error
}

func runTarget(ctx context.Context, harness string, data []byte, timeout time.Duration) runResult {
	tmp, err := os.CreateTemp("", "fuzz-in-*")
	if err != nil {
		return runResult{kind: resultError, err: err}
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return runResult{kind: resultError, err: err}
	}
	if err := tmp.Close(); err != nil {
		return runResult{kind: resultError, err: err}
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, harness, tmpPath)
	cmd.Stdin = nil
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard

	err = cmd.Run()
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return runResult{kind: resultHang, detail: fmt.Sprintf("timeout=%s bytes=%d", timeout, len(data))}
	}
	if err == nil {
		return runResult{kind: resultOK}
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return runResult{kind: resultError, err: err}
	}

	st, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok {
		return runResult{kind: resultError, err: fmt.Errorf("wait status type %T", exitErr.Sys())}
	}
	if st.Signaled() {
		sig := st.Signal()
		name := sig.String()
		return runResult{kind: resultCrash, detail: fmt.Sprintf("signal=%s (%d) bytes=%d", name, int(sig), len(data))}
	}
	if st.Exited() {
		code := st.ExitStatus()
		if code != 0 {
			return runResult{kind: resultNonZero, detail: fmt.Sprintf("exit=%d bytes=%d", code, len(data))}
		}
	}
	return runResult{kind: resultOK}
}

func saveArtifact(dir, prefix string, data []byte, detail string) {
	name := fmt.Sprintf("%s_%s_%d.bin", prefix, time.Now().Format("20060102_150405"), time.Now().UnixNano()%1_000_000)
	path := filepath.Join(dir, name)
	_ = os.WriteFile(path, data, 0o644)
	meta := path + ".txt"
	_ = os.WriteFile(meta, []byte(detail+"\n"), 0o644)
}

func mutate(rng *rand.Rand, base []byte) []byte {
	if len(base) == 0 {
		return []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	}
	out := make([]byte, len(base))
	copy(out, base)

	steps := 1 + rng.Intn(12)
	for s := 0; s < steps; s++ {
		if len(out) == 0 {
			break
		}
		switch rng.Intn(11) {
		case 0, 1: // bit flip
			i := rng.Intn(len(out))
			out[i] ^= byte(1 << rng.Intn(8))
		case 2, 3: // random byte
			i := rng.Intn(len(out))
			out[i] = byte(rng.Intn(256))
		case 4: // insert byte
			i := rng.Intn(len(out) + 1)
			b := byte(rng.Intn(256))
			out = append(out[:i], append([]byte{b}, out[i:]...)...)
		case 5: // delete byte
			if len(out) < 2 {
				continue
			}
			i := rng.Intn(len(out))
			out = append(out[:i], out[i+1:]...)
		case 6: // splice copy inside
			if len(out) < 4 {
				continue
			}
			a, b := rng.Intn(len(out)), rng.Intn(len(out))
			if a > b {
				a, b = b, a
			}
			chunk := make([]byte, b-a)
			copy(chunk, out[a:b])
			ins := rng.Intn(len(out) + 1)
			out = append(out[:ins], append(chunk, out[ins:]...)...)
		case 7: // truncate tail
			if len(out) < 2 {
				continue
			}
			cut := 1 + rng.Intn(len(out)/2+1)
			if cut >= len(out) {
				cut = len(out) - 1
			}
			out = out[:len(out)-cut]
		case 8: // 2-byte swap
			if len(out) < 2 {
				continue
			}
			i, j := rng.Intn(len(out)), rng.Intn(len(out))
			out[i], out[j] = out[j], out[i]
		case 9: // interesting byte patch
			i := rng.Intn(len(out))
			interesting := []byte{0x00, 0xff, 0x7f, 0x80, 0x0d, 0x0a, 0x01}
			out[i] = interesting[rng.Intn(len(interesting))]
		case 10: // append small random tail
			n := rng.Intn(64) + 1
			tail := make([]byte, n)
			for i := range tail {
				tail[i] = byte(rng.Intn(256))
			}
			out = append(out, tail...)
		}
		if len(out) > maxInput {
			out = out[:maxInput]
		}
	}
	return out
}
