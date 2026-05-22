package transcribe

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/ChristianSch/agentbridge/internal/core"
)

type WhisperCPP struct {
	Binary   string
	Model    string
	Language string
	Threads  int
	Timeout  time.Duration
}

func (w WhisperCPP) Transcribe(ctx context.Context, audio core.Attachment) (core.Transcript, error) {
	bin := w.Binary
	if bin == "" {
		bin = "whisper-cli"
	}
	if w.Timeout <= 0 {
		w.Timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, w.Timeout)
	defer cancel()
	audioPath := audio.Path
	if audio.MimeType != "audio/wav" && audio.MimeType != "audio/x-wav" {
		if wav, cleanup, err := convertToWAV(ctx, audio.Path); err == nil {
			defer cleanup()
			audioPath = wav
		}
	}
	args := []string{"-f", audioPath, "-nt"}
	if w.Model != "" {
		args = append(args, "-m", w.Model)
	}
	if w.Language != "" && w.Language != "auto" {
		args = append(args, "-l", w.Language)
	}
	if w.Threads > 0 {
		args = append(args, "-t", strconv.Itoa(w.Threads))
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return core.Transcript{}, fmt.Errorf("whisper failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	text := cleanWhisperOutput(stdout.String())
	if text == "" {
		text = cleanWhisperOutput(stderr.String())
	}
	if text == "" {
		return core.Transcript{}, fmt.Errorf("whisper produced no transcript")
	}
	return core.Transcript{Text: text, Engine: "whisper.cpp", Language: w.Language}, nil
}

func convertToWAV(ctx context.Context, in string) (string, func(), error) {
	out, err := os.CreateTemp("", "agentbridge-voice-*.wav")
	if err != nil {
		return "", nil, err
	}
	path := out.Name()
	_ = out.Close()
	cmd := exec.CommandContext(ctx, "ffmpeg", "-y", "-i", in, "-ar", "16000", "-ac", "1", path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		_ = os.Remove(path)
		return "", nil, fmt.Errorf("ffmpeg failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return path, func() { _ = os.Remove(path) }, nil
}

func cleanWhisperOutput(s string) string {
	lines := strings.Split(s, "\n")
	out := []string{}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "whisper_") || strings.HasPrefix(line, "main:") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if idx := strings.Index(line, "]"); idx >= 0 && idx+1 < len(line) {
				line = strings.TrimSpace(line[idx+1:])
			}
		}
		if line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, " ")
}
