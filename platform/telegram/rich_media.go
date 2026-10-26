package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"
)

// Local images in a rich reply.
//
// An agent can only reference a screenshot it produced by its local path —
// ![alt](/abs/path.png) — which Telegram cannot fetch. With
// rich_local_media_dirs set, such images are uploaded with the message
// (InputRichMessage.media + attach://) and rendered inline as photo blocks, so
// the screenshot arrives inside the one reply. Only regular files under the
// listed directories are sent; any other local path is replaced by its alt
// text, so this can never turn into a way to post arbitrary server files.

var richLocalImageRe = regexp.MustCompile(`!\[([^\]]*)\]\((?:file://)?(/[^)\s]+)\)`)

const (
	richLocalMediaMax     = 10
	richLocalMediaMaxSize = 10 << 20
	richLocalMediaRetries = 3
)

type richLocalMedia struct {
	id, path string
}

func parseRichLocalMediaDirs(opts map[string]any) []string {
	raw, _ := opts["rich_local_media_dirs"].(string)
	var dirs []string
	for _, d := range strings.Split(raw, ",") {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		if real, err := filepath.EvalSymlinks(d); err == nil {
			d = real
		}
		dirs = append(dirs, filepath.Clean(d))
	}
	return dirs
}

func richLocalMediaAllowed(dirs []string, p string) (string, bool) {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
	default:
		return "", false
	}
	real, err := filepath.EvalSymlinks(filepath.Clean(p))
	if err != nil {
		return "", false
	}
	st, err := os.Stat(real)
	if err != nil || !st.Mode().IsRegular() || st.Size() == 0 || st.Size() > richLocalMediaMaxSize {
		return "", false
	}
	for _, d := range dirs {
		if strings.HasPrefix(real, d+string(os.PathSeparator)) {
			return real, true
		}
	}
	return "", false
}

// extractRichLocalMedia rewrites local image references. With upload=false
// (message edits, which cannot carry uploads) every local image becomes its
// alt text.
func (r *richPlatform) extractRichLocalMedia(md string, upload bool) (string, []richLocalMedia) {
	var media []richLocalMedia
	out := richLocalImageRe.ReplaceAllStringFunc(md, func(m string) string {
		sub := richLocalImageRe.FindStringSubmatch(m)
		alt := strings.TrimSpace(sub[1])
		if upload && len(media) < richLocalMediaMax {
			if real, ok := richLocalMediaAllowed(r.localMediaDirs, sub[2]); ok {
				id := "m" + strconv.Itoa(len(media)+1)
				media = append(media, richLocalMedia{id: id, path: real})
				return "![" + alt + "](tg://photo?id=" + id + ")"
			}
		}
		if alt == "" {
			alt = "изображение"
		}
		return "_" + alt + "_"
	})
	return out, media
}

// sendRichWithMedia posts sendRichMessage as multipart with the images attached.
func (r *richPlatform) sendRichWithMedia(ctx context.Context, rc replyContext, md string, media []richLocalMedia, replyToMessageID int) (*models.Message, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("chat_id", strconv.FormatInt(rc.chatID, 10))
	if rc.threadID != 0 {
		_ = w.WriteField("message_thread_id", strconv.Itoa(rc.threadID))
	}
	if replyToMessageID != 0 {
		rp, _ := json.Marshal(map[string]any{"message_id": replyToMessageID})
		_ = w.WriteField("reply_parameters", string(rp))
	}
	type inputMedia struct {
		ID    string            `json:"id"`
		Media map[string]string `json:"media"`
	}
	rm := struct {
		Markdown string       `json:"markdown"`
		Media    []inputMedia `json:"media"`
	}{Markdown: md}
	for i, m := range media {
		field := "f" + strconv.Itoa(i+1)
		rm.Media = append(rm.Media, inputMedia{ID: m.id, Media: map[string]string{"type": "photo", "media": "attach://" + field}})
		f, err := os.Open(m.path)
		if err != nil {
			return nil, err
		}
		part, err := w.CreateFormFile(field, filepath.Base(m.path))
		if err == nil {
			_, err = io.Copy(part, io.LimitReader(f, richLocalMediaMaxSize))
		}
		f.Close()
		if err != nil {
			return nil, err
		}
	}
	rmJSON, _ := json.Marshal(rm)
	_ = w.WriteField("rich_message", string(rmJSON))
	if err := w.Close(); err != nil {
		return nil, err
	}
	client := r.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	// Uploads of several MB break on short network blips; retry transport
	// errors so the images are not dropped from the reply.
	var resp *http.Response
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+r.token+"/sendRichMessage", bytes.NewReader(buf.Bytes()))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", w.FormDataContentType())
		resp, err = client.Do(req)
		if err == nil {
			break
		}
		if attempt >= richLocalMediaRetries || ctx.Err() != nil {
			return nil, fmt.Errorf("sendRichMessage with media: %s", strings.ReplaceAll(err.Error(), r.token, "***"))
		}
		slog.Warn("telegram-rich: media upload failed, retrying", "attempt", attempt+1, "error", strings.ReplaceAll(err.Error(), r.token, "***"))
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 15 * time.Second):
		}
	}
	defer resp.Body.Close()
	var res struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      *models.Message `json:"result"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&res); err != nil {
		return nil, err
	}
	if !res.OK || res.Result == nil {
		return nil, fmt.Errorf("sendRichMessage with media: %s", res.Description)
	}
	return res.Result, nil
}
