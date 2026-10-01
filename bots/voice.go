package bots

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

// Transcribe proxies voice to Flov after losslessly decoding it to PCM WAV.
// The returned text is a draft: transcribing never submits a bot message.
func (w *Workspace) Transcribe(ctx context.Context, audio []byte, format string) (string, error) {
	if len(audio) == 0 || len(audio) > maxUploadBytes {
		return "", errors.New("audio must contain 1 byte to 25 MiB")
	}
	config := w.settings()
	endpoint, err := url.Parse(config.FlovURL)
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" {
		return "", errors.New("Flov transcription endpoint is invalid")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	wav, err := config.ConvertAudio(ctx, audio, format)
	if err != nil {
		return "", fmt.Errorf("decode audio for Flov: %w", err)
	}
	if len(wav) > 100<<20 {
		return "", errors.New("decoded recording is too large; use a shorter recording")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "audio.wav")
	if err != nil {
		return "", err
	}
	if _, err := part.Write(wav); err != nil {
		return "", err
	}
	if err := writer.WriteField("response_format", "json"); err != nil {
		return "", err
	}
	if err := writer.WriteField("model", "whisper-1"); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := config.HTTPClient.Do(req)
	if err != nil {
		return "", errors.New("Flov transcription service is unavailable")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxWorkspaceFile+1))
	if err != nil {
		return "", errors.New("could not read Flov response")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("Flov transcription service returned HTTP %d", resp.StatusCode)
	}
	if len(data) > maxWorkspaceFile {
		return "", errors.New("Flov response exceeds 1 MiB")
	}
	var result struct {
		Text *string `json:"text"`
	}
	if json.Unmarshal(data, &result) == nil {
		if result.Text == nil {
			return "", errors.New("Flov response did not contain a transcription")
		}
		return strings.TrimSpace(*result.Text), nil
	}
	if strings.Contains(resp.Header.Get("Content-Type"), "json") {
		return "", errors.New("Flov returned an invalid transcription response")
	}
	return strings.TrimSpace(string(data)), nil
}

func (w *Workspace) handleTranscribe(out http.ResponseWriter, r *http.Request) {
	audio, name, mimeType, err := workspaceMultipart(out, r)
	if err != nil {
		workspaceError(out, err)
		return
	}
	format := strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".")
	if format == "" {
		format = mimeType
	}
	text, err := w.Transcribe(r.Context(), audio, format)
	if err != nil {
		writeError(out, http.StatusBadGateway, err)
		return
	}
	writeJSON(out, http.StatusOK, map[string]string{"text": text})
}
