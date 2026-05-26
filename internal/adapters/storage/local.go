package storage

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ChristianSch/agentbridge/internal/core"
)

type LocalAttachmentStore struct {
	root    string
	maxSize int64
	allowed map[string]bool
	mu      sync.RWMutex
	items   map[string]core.Attachment
}

func NewLocalAttachmentStore(root string, maxSize int64, allowed []string) (*LocalAttachmentStore, error) {
	if root == "" {
		root = defaultRoot()
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	s := &LocalAttachmentStore{root: root, maxSize: maxSize, allowed: map[string]bool{}, items: map[string]core.Attachment{}}
	for _, mt := range allowed {
		s.allowed[strings.ToLower(mt)] = true
	}
	s.loadIndex()
	return s, nil
}

func (s *LocalAttachmentStore) Save(ctx context.Context, r io.Reader, meta core.AttachmentMeta) (core.Attachment, error) {
	if s.maxSize <= 0 {
		s.maxSize = 25 * 1000 * 1000
	}
	limited := io.LimitReader(r, s.maxSize+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return core.Attachment{}, err
	}
	if int64(len(data)) > s.maxSize {
		return core.Attachment{}, fmt.Errorf("attachment too large (max %d bytes)", s.maxSize)
	}
	detectedMime := strings.ToLower(strings.Split(http.DetectContentType(data), ";")[0])
	mimeType := strings.ToLower(strings.Split(meta.MimeType, ";")[0])
	if mimeType == "" || mimeType == "application/octet-stream" {
		mimeType = detectedMime
	}
	if mimeType == "application/pdf" && detectedMime != "application/pdf" {
		return core.Attachment{}, fmt.Errorf("uploaded file is not a PDF")
	}
	if len(s.allowed) > 0 && !s.allowed[mimeType] {
		return core.Attachment{}, fmt.Errorf("unsupported attachment type %q", mimeType)
	}
	id := "att_" + randomHex(12)
	owner := safeSegment(core.OwnerID(ctx))
	if owner == "" {
		owner = "default"
	}
	session := safeSegment(meta.SessionID)
	if session == "" {
		session = "unscoped"
	}
	dir := filepath.Join(s.root, owner, session)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return core.Attachment{}, err
	}
	name := safeFileName(meta.FileName)
	if name == "" {
		name = id
	}
	path := filepath.Join(dir, id+"-"+name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return core.Attachment{}, err
	}
	att := core.Attachment{ID: id, Kind: kindForMime(mimeType), FileName: name, MimeType: mimeType, Size: int64(len(data)), OwnerID: owner, SessionID: session, Path: path}
	if att.Kind == core.AttachmentImage {
		att.Preview = "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data)
	}
	if att.Kind == core.AttachmentText && len(data) <= 512*1024 {
		att.ExtractedText = string(data)
	}
	if att.MimeType == "application/pdf" && detectedMime == "application/pdf" {
		att.ExtractedText = extractPDFText(path)
	}
	s.mu.Lock()
	s.items[id] = att
	s.mu.Unlock()
	s.saveIndex()
	return att, nil
}

func (s *LocalAttachmentStore) Get(ctx context.Context, id string) (core.Attachment, error) {
	s.mu.RLock()
	att, ok := s.items[id]
	s.mu.RUnlock()
	if !ok {
		return core.Attachment{}, fmt.Errorf("attachment not found")
	}
	if !attachmentVisible(ctx, att) {
		return core.Attachment{}, fmt.Errorf("attachment not found")
	}
	return att, nil
}

func (s *LocalAttachmentStore) Open(ctx context.Context, id string) (io.ReadCloser, error) {
	att, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return os.Open(att.Path)
}

func (s *LocalAttachmentStore) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	att, ok := s.items[id]
	if ok && !attachmentVisible(ctx, att) {
		s.mu.Unlock()
		return fmt.Errorf("attachment not found")
	}
	if ok {
		delete(s.items, id)
	}
	s.mu.Unlock()
	if ok {
		_ = os.Remove(att.Path)
		s.saveIndex()
	}
	return nil
}

func (s *LocalAttachmentStore) loadIndex() {
	b, err := os.ReadFile(filepath.Join(s.root, "index.json"))
	if err != nil {
		return
	}
	_ = json.Unmarshal(b, &s.items)
}

func (s *LocalAttachmentStore) saveIndex() {
	s.mu.RLock()
	b, _ := json.MarshalIndent(s.items, "", "  ")
	s.mu.RUnlock()
	_ = os.WriteFile(filepath.Join(s.root, "index.json"), b, 0o600)
}

func extractPDFText(path string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "pdftotext", "-layout", path, "-")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return ""
	}
	text := strings.TrimSpace(stdout.String())
	const max = 256 * 1024
	if len(text) > max {
		text = text[:max] + "\n\n[truncated]"
	}
	return text
}

func kindForMime(mt string) core.AttachmentKind {
	switch {
	case strings.HasPrefix(mt, "image/"):
		return core.AttachmentImage
	case strings.HasPrefix(mt, "audio/"):
		return core.AttachmentAudio
	case strings.HasPrefix(mt, "text/"):
		return core.AttachmentText
	default:
		return core.AttachmentFile
	}
}

func randomHex(n int) string { b := make([]byte, n); _, _ = rand.Read(b); return hex.EncodeToString(b) }
func safeSegment(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '-' || r == '_' || r == '.' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			return r
		}
		return '_'
	}, s)
}
func attachmentVisible(ctx context.Context, att core.Attachment) bool {
	owner := safeSegment(core.OwnerID(ctx))
	return att.OwnerID == "" || owner == "" || att.OwnerID == owner
}

func safeFileName(s string) string { return safeSegment(filepath.Base(s)) }
func defaultRoot() string {
	if base := os.Getenv("AGENTBRIDGE_STATE_DIR"); base != "" {
		return filepath.Join(base, "attachments")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "state", "agentbridge", "attachments")
	}
	return filepath.Join(".", "attachments")
}
