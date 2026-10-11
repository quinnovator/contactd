// Command guest is a stand-in language runtime for the tier-0 provider's unit
// tests. It speaks the same driver protocol as the real Python and QuickJS
// drivers, so the tests exercise the provider without downloading a runtime.
// The tests build it with GOOS=wasip1 GOARCH=wasm.
//
// The "code" it runs is one command per request; see run.
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// state is everything a real interpreter would keep between requests.
type state struct {
	vars map[string]string
	hog  [][]byte
}

func main() {
	st := &state{vars: map[string]string{}}
	in := bufio.NewReader(os.Stdin)
	for {
		header, err := in.ReadString('\n')
		if err != nil {
			return
		}
		token, size, _ := strings.Cut(strings.TrimSuffix(header, "\n"), " ")
		n, err := strconv.Atoi(size)
		if err != nil {
			os.Exit(70)
		}
		code := make([]byte, n)
		if _, err := io.ReadFull(in, code); err != nil {
			return
		}
		status := st.run(string(code), token)
		fmt.Fprintf(os.Stdout, "\x00%s:%03d\n", token, status)
	}
}

func (st *state) run(code, token string) int {
	cmd, arg, _ := strings.Cut(code, " ")
	switch cmd {
	case "echo":
		fmt.Fprintln(os.Stdout, arg)
	case "err":
		fmt.Fprintln(os.Stderr, arg)
	case "fail": // fail <status> <message>
		status, msg, _ := strings.Cut(arg, " ")
		fmt.Fprintln(os.Stderr, msg)
		n, _ := strconv.Atoi(status)
		return n
	case "set": // set <name> <value>
		name, value, _ := strings.Cut(arg, " ")
		st.vars[name] = value
	case "get":
		value, ok := st.vars[arg]
		if !ok {
			fmt.Fprintf(os.Stderr, "NameError: %s is not defined\n", arg)
			return 1
		}
		fmt.Fprintln(os.Stdout, value)
	case "write": // write <path> <content>
		name, content, _ := strings.Cut(arg, " ")
		return report(os.WriteFile(name, []byte(content), 0o600))
	case "read":
		b, err := os.ReadFile(arg)
		if err != nil {
			return report(err)
		}
		os.Stdout.Write(b)
	case "ls":
		entries, err := os.ReadDir(arg)
		if err != nil {
			return report(err)
		}
		for _, e := range entries {
			fmt.Fprintln(os.Stdout, e.Name())
		}
	case "mkdir":
		return report(os.MkdirAll(arg, 0o700))
	case "symlink": // symlink <target> <link>
		target, link, _ := strings.Cut(arg, " ")
		return report(os.Symlink(target, link))
	case "cwd":
		dir, err := os.Getwd()
		if err != nil {
			return report(err)
		}
		fmt.Fprintln(os.Stdout, dir)
	case "env":
		fmt.Fprintln(os.Stdout, strings.Join(os.Environ(), "\n"))
	case "args":
		fmt.Fprintln(os.Stdout, strings.Join(os.Args, " "))
	case "spin":
		for {
		}
	case "sleep": // sleep <milliseconds>
		ms, _ := strconv.Atoi(arg)
		time.Sleep(time.Duration(ms) * time.Millisecond)
	case "alloc": // alloc <MiB>, kept alive for the life of the instance
		mib, _ := strconv.Atoi(arg)
		block := make([]byte, mib<<20)
		for i := 0; i < len(block); i += 4096 {
			block[i] = 1
		}
		st.hog = append(st.hog, block)
		fmt.Fprintln(os.Stdout, "ok")
	case "flood": // flood <stdout|stderr> <bytes>
		stream, count, _ := strings.Cut(arg, " ")
		n, _ := strconv.Atoi(count)
		w := os.Stdout
		if stream == "stderr" {
			w = os.Stderr
		}
		w.WriteString("BEGIN")
		chunk := []byte(strings.Repeat("x", 1000))
		for left := n - 10; left > 0; left -= len(chunk) {
			w.Write(chunk[:min(left, len(chunk))])
		}
		w.WriteString("END!!")
	case "die": // die <status>: the whole runtime exits, as os._exit would
		n, _ := strconv.Atoi(arg)
		os.Exit(n)
	case "fake":
		// Output that looks like an end-of-request marker but carries the
		// wrong token. The host must pass it through as plain output.
		fmt.Fprintf(os.Stdout, "\x00%s:000\nafter\n", strings.Repeat("A", len(token)))
	case "tease":
		// A marker cut short, then ordinary output.
		fmt.Fprintf(os.Stdout, "\x00%s:0x\n", token)
	case "forge":
		// Hostile: claim the request is finished, then keep the CPU.
		fmt.Fprintf(os.Stdout, "\x00%s:000\n", token)
		for {
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", cmd)
		return 127
	}
	return 0
}

func report(err error) int {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
