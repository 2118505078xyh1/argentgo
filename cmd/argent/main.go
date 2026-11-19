package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/2118505078xyh1/argent-go/internal/agent"
	"github.com/2118505078xyh1/argent-go/internal/llm"
	"github.com/2118505078xyh1/argent-go/internal/session"
	"github.com/2118505078xyh1/argent-go/internal/tools"
)

const version = "0.1.0"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	flags := flag.NewFlagSet("argent", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	var (
		model      = flags.String("m", "", "model id")
		baseURL    = flags.String("base-url", "", "OpenAI-compatible base URL")
		apiKey     = flags.String("api-key", "", "API key")
		yolo       = flags.Bool("yolo", false, "allow mutating tools (write, edit, bash)")
		maxTurns   = flags.Int("max-turns", 12, "maximum model turns")
		mode       = flags.String("mode", "text", "output mode: text or json")
		cwd        = flags.String("cwd", "", "workspace root (default: current directory)")
		printOnly  = flags.Bool("version", false, "print version and exit")
	)
	flags.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: argent [flags] <prompt>")
		fmt.Fprintln(os.Stderr, "       argent [flags] -            (prompt from stdin)")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *printOnly {
		fmt.Printf("argent-go %s\n", version)
		return 0
	}

	prompt := strings.TrimSpace(strings.Join(flags.Args(), " "))
	if prompt == "" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error: read stdin:", err)
			return 1
		}
		prompt = strings.TrimSpace(string(data))
	}
	if prompt == "" {
		flags.Usage()
		return 2
	}

	root := *cwd
	if root == "" {
		root, _ = os.Getwd()
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: resolve workspace:", err)
		return 1
	}

	client := llm.New(resolveBaseURL(*baseURL), resolveAPIKey(*apiKey))
	registry := tools.New(absRoot, *yolo)

	var out io.Writer = os.Stdout
	if *mode == "json" {
		out = jsonWriter{w: os.Stdout}
	}

	var recorder *session.Writer
	if home, err := os.UserHomeDir(); err == nil {
		if writer, err := session.New(filepath.Join(home, ".argent-go", "sessions")); err == nil {
			recorder = writer
			defer recorder.Close()
		}
	}

	runner := &agent.Runner{
		Client: client,
		Tools:  registry,
		Opts: agent.Options{
			Model:    resolveModel(*model),
			MaxTurns: *maxTurns,
			Root:     absRoot,
			Out:      out,
			Record: func(kind string, payload any) {
				if recorder != nil {
					_ = recorder.Write(kind, payload)
				}
			},
		},
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	result, err := runner.Run(ctx, prompt)
	if *mode == "json" {
		emit(map[string]any{"type": "result", "text": result, "error": errText(err)})
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if *mode == "text" {
		fmt.Fprintln(os.Stdout)
	}
	return 0
}

type jsonWriter struct {
	w io.Writer
}

func (j jsonWriter) Write(p []byte) (int, error) {
	if err := emit(map[string]any{"type": "text", "text": string(p)}); err != nil {
		return 0, err
	}
	return len(p), nil
}

func emit(event map[string]any) error {
	line, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(os.Stdout, string(line))
	return err
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func resolveBaseURL(flagValue string) string {
	for _, candidate := range []string{
		flagValue,
		os.Getenv("ARGENT_BASE_URL"),
		os.Getenv("OPENAI_BASE_URL"),
	} {
		if candidate != "" {
			return candidate
		}
	}
	return "https://api.openai.com/v1"
}

func resolveAPIKey(flagValue string) string {
	for _, candidate := range []string{
		flagValue,
		os.Getenv("ARGENT_API_KEY"),
		os.Getenv("OPENAI_API_KEY"),
	} {
		if candidate != "" {
			return candidate
		}
	}
	return ""
}

func resolveModel(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if env := os.Getenv("ARGENT_MODEL"); env != "" {
		return env
	}
	return "gpt-4o-mini"
}
