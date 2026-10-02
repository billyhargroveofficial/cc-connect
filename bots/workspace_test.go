package bots

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/chenhg5/cc-connect/core"
)

func workspaceFixture(t *testing.T) (*Store, *Workspace, Bot) {
	t.Helper()
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	workspace := NewWorkspace(store)
	workspace.Configure(WorkspaceConfig{NativeSkillDirs: []string{}, BackendSkillDirs: map[string][]string{}, SystemSkillDirs: []string{}})
	bot, err := store.CreateBot(Bot{Name: "Research"})
	if err != nil {
		t.Fatal(err)
	}
	return store, workspace, bot
}

func TestWorkspaceInstructionsRejectSymlinkWrite(t *testing.T) {
	_, workspace, bot := workspaceFixture(t)
	outside := filepath.Join(t.TempDir(), "important.md")
	if err := os.WriteFile(outside, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(bot.WorkDir, "AGENTS.md")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if _, _, err := workspace.Instructions(bot.ID); err == nil {
		t.Fatal("read followed a managed symlink")
	}
	if err := workspace.SetInstructions(bot.ID, "overwrite"); err == nil {
		t.Fatal("write followed a managed symlink")
	}
	content, _ := os.ReadFile(outside)
	if string(content) != "preserve" {
		t.Fatal("outside file was modified")
	}
}

func TestWorkspaceSkillScopesAndPerBotEnablement(t *testing.T) {
	store, workspace, bot := workspaceFixture(t)
	other, err := store.CreateBot(Bot{Name: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	shared, err := workspace.CreateSkill("", "review", "---\nname: review\ndescription: Review code changes.\n---\n\nCheck correctness.\n")
	if err != nil {
		t.Fatal(err)
	}
	project, err := workspace.CreateSkill(bot.ID, "research", "---\nname: research\ndescription: Find primary sources.\n---\n\nVerify sources.\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.CreateSkill(bot.ID, "../escape", ""); err == nil {
		t.Fatal("traversal skill accepted")
	}
	if _, err := workspace.SetDisabledSkills(bot.ID, []string{shared.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.SetDisabledSkills(bot.ID, []string{"/etc/passwd"}); err == nil {
		t.Fatal("unknown skill accepted")
	}
	skills, err := workspace.BotSkills(bot.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 2 {
		t.Fatalf("skills=%+v", skills)
	}
	for _, skill := range skills {
		if skill.ID == shared.ID && (skill.Enabled || skill.Scope != "productUser") {
			t.Fatalf("shared skill=%+v", skill)
		}
		if skill.ID == project.ID && (!skill.Enabled || skill.Scope != "project") {
			t.Fatalf("project skill=%+v", skill)
		}
	}
	otherSkills, err := workspace.BotSkills(other.ID)
	if err != nil || len(otherSkills) != 1 || !otherSkills[0].Enabled {
		t.Fatalf("other skills=%+v, %v", otherSkills, err)
	}
	opts, err := workspace.SkillSessionOptions(bot.ID)
	if err != nil {
		t.Fatal(err)
	}
	disabled := opts["app_server_config"].(map[string]any)["skills.config"].([]map[string]any)
	if len(disabled) != 1 || disabled[0]["path"] != shared.Path || disabled[0]["enabled"] != false {
		t.Fatalf("disabled=%+v", disabled)
	}
	if _, err := workspace.SkillSessionOptions(other.ID); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(other.WorkDir, ".agents", "skills", "connect-user-review")
	target, err := os.Readlink(link)
	if err != nil || target != filepath.Dir(shared.Path) {
		t.Fatalf("shared link %q %v", target, err)
	}
	if _, err := workspace.SetDisabledSkills(other.ID, []string{shared.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.SkillSessionOptions(other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatalf("disabled shared link remains: %v", err)
	}
}

func TestWorkspaceNativeSystemSkillsAreReadOnlyAndExternalLinksIgnored(t *testing.T) {
	_, workspace, bot := workspaceFixture(t)
	native := t.TempDir()
	system := filepath.Join(native, ".system")
	path := filepath.Join(system, "built-in", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: built-in\ndescription: A system workflow.\n---\n\nSteps.\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "SKILL.md"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(native, "escape")); err != nil {
		t.Fatal(err)
	}
	workspace.Configure(WorkspaceConfig{NativeSkillDirs: []string{native}, SystemSkillDirs: []string{system}})
	skills, err := workspace.BotSkills(bot.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 1 || skills[0].Scope != "system" || skills[0].Editable {
		t.Fatalf("skills=%+v", skills)
	}
	if err := workspace.SetSkillContent(path, content+"Edit"); err == nil {
		t.Fatal("system skill edit accepted")
	}
	if _, err := workspace.SkillContent(filepath.Join(outside, "SKILL.md")); err == nil {
		t.Fatal("outside skill was readable")
	}
}

func TestWorkspaceNativeSkillsFollowBotBackendAndKeepGlobalEditing(t *testing.T) {
	store, workspace, codexBot := workspaceFixture(t)
	piBot, err := store.CreateBot(Bot{Name: "Pi", Backend: "pi"})
	if err != nil {
		t.Fatal(err)
	}
	sharedRoot, codexRoot, piRoot := t.TempDir(), t.TempDir(), t.TempDir()
	systemRoot := filepath.Join(codexRoot, ".system")
	writeSkill := func(root, name string) string {
		t.Helper()
		path := filepath.Join(root, name, "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		content := "---\nname: " + name + "\ndescription: A native workflow.\n---\n\nSteps.\n"
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	sharedNative := writeSkill(sharedRoot, "native-shared")
	codexOnly := writeSkill(codexRoot, "codex-only")
	piOnly := writeSkill(piRoot, "pi-only")
	writeSkill(systemRoot, "codex-system")
	workspace.Configure(WorkspaceConfig{
		NativeSkillDirs: []string{sharedRoot}, BackendSkillDirs: map[string][]string{"codex": {codexRoot}, "pi": {piRoot}},
		SystemSkillDirs: []string{systemRoot},
	})
	product, err := workspace.CreateSkill("", "product-shared", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, bot := range []Bot{codexBot, piBot} {
		if _, err := workspace.CreateSkill(bot.ID, "project-"+bot.Backend, ""); err != nil {
			t.Fatal(err)
		}
	}
	assertSkills := func(bot Bot, expected []string) {
		t.Helper()
		skills, err := workspace.BotSkills(bot.ID)
		if err != nil {
			t.Fatal(err)
		}
		actual := map[string]Skill{}
		for _, skill := range skills {
			actual[skill.Name] = skill
			if !skill.Enabled {
				t.Fatalf("fresh %s skill is disabled: %+v", bot.Backend, skill)
			}
		}
		if len(actual) != len(expected) {
			t.Fatalf("%s skills=%+v, expected %v", bot.Backend, skills, expected)
		}
		for _, name := range expected {
			if _, ok := actual[name]; !ok {
				t.Fatalf("%s is missing %s: %+v", bot.Backend, name, skills)
			}
		}
	}
	assertSkills(codexBot, []string{"native-shared", "codex-only", "codex-system", "product-shared", "project-codex"})
	assertSkills(piBot, []string{"native-shared", "pi-only", "product-shared", "project-pi"})
	global, err := workspace.UserSkills()
	if err != nil || len(global) != 5 {
		t.Fatalf("global catalog lost native scopes: %+v, %v", global, err)
	}
	for _, path := range []string{codexOnly, piOnly} {
		content, err := workspace.SkillContent(path)
		if err != nil || workspace.SetSkillContent(path, content+"Edited globally.\n") != nil {
			t.Fatalf("global native skill is no longer editable: %s, %v", path, err)
		}
	}
	opts, err := workspace.SkillSessionOptions(piBot.ID)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(opts["cli_args"].([]string), "\n")
	for _, path := range []string{sharedNative, piOnly, product.Path} {
		if !strings.Contains(args, path) {
			t.Fatalf("Pi omitted its advertised skill %s: %s", path, args)
		}
	}
	if strings.Contains(args, codexOnly) || strings.Contains(args, systemRoot) {
		t.Fatalf("Pi received Codex-only skills: %s", args)
	}
	if _, err := workspace.SetDisabledSkills(codexBot.ID, []string{codexOnly}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PatchBot(codexBot.ID, map[string]json.RawMessage{"backend": json.RawMessage(`"pi"`)}); err != nil {
		t.Fatal(err)
	}
	// The hidden Codex choice travels through the UI's existing disabled list.
	if _, err := workspace.SetDisabledSkills(codexBot.ID, []string{codexOnly, piOnly}); err != nil {
		t.Fatalf("saving Pi choices rejected the retained Codex choice: %v", err)
	}
	if _, err := store.PatchBot(codexBot.ID, map[string]json.RawMessage{"backend": json.RawMessage(`"codex"`)}); err != nil {
		t.Fatal(err)
	}
	skills, err := workspace.BotSkills(codexBot.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, skill := range skills {
		if skill.ID == codexOnly && skill.Enabled {
			t.Fatal("Codex choice was lost during the backend round trip")
		}
		if skill.ID == piOnly {
			t.Fatal("Pi-only skill reappeared in the Codex catalog")
		}
	}
}

func TestWorkspaceProjectSkillPrefixDoesNotBecomeManagedLink(t *testing.T) {
	_, workspace, bot := workspaceFixture(t)
	skill, err := workspace.CreateSkill(bot.ID, "connect-user-project", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.SkillSessionOptions(bot.ID); err != nil {
		t.Fatal("ordinary project skill mistaken for a managed link", err)
	}
	if _, err := os.Stat(skill.Path); err != nil {
		t.Fatal("project skill removed", err)
	}
}

func TestWorkspaceAttachmentsResolveOpaqueIDsOnly(t *testing.T) {
	store, workspace, bot := workspaceFixture(t)
	attachment, err := workspace.StoreUpload(bot.ID, "../../report.txt", "text/plain", []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if attachment.Name != "report.txt" || attachment.Path != "" || attachment.ID == "" {
		t.Fatalf("public attachment=%+v", attachment)
	}
	resolved, err := workspace.ResolveAttachments(bot.ID, []Attachment{{ID: attachment.ID, Path: "/etc/passwd", Name: "passwd"}})
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(resolved[0].Path)
	if err != nil || string(content) != "hello" || !pathWithin(bot.WorkDir, resolved[0].Path) {
		t.Fatalf("resolved=%+v %v", resolved, err)
	}
	other, err := store.CreateBot(Bot{Name: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.ResolveAttachments(other.ID, []Attachment{attachment}); err == nil {
		t.Fatal("cross-bot attachment accepted")
	}
	if _, err := workspace.ResolveAttachments(bot.ID, []Attachment{{ID: "../../etc/passwd"}}); err == nil {
		t.Fatal("attachment traversal accepted")
	}
	outside := filepath.Join(t.TempDir(), "private.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(resolved[0].Path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, resolved[0].Path); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.ResolveAttachments(bot.ID, []Attachment{attachment}); err == nil {
		t.Fatal("attachment symlink accepted")
	}
}

func TestWorkspaceUploadMultipartAndDownload(t *testing.T) {
	_, workspace, bot := workspaceFixture(t)
	mux := http.NewServeMux()
	workspace.RegisterHTTP(mux, nil)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", ".attachment.json")
	if err != nil {
		t.Fatal(err)
	}
	part.Write([]byte("actual file bytes"))
	writer.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/studio/bots/"+bot.ID+"/uploads", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	out := httptest.NewRecorder()
	mux.ServeHTTP(out, req)
	if out.Code != http.StatusCreated {
		t.Fatalf("upload %d %s", out.Code, out.Body.String())
	}
	var attachment Attachment
	if err := json.Unmarshal(out.Body.Bytes(), &attachment); err != nil {
		t.Fatal(err)
	}
	out = httptest.NewRecorder()
	mux.ServeHTTP(out, httptest.NewRequest(http.MethodGet, attachment.URL, nil))
	if out.Code != http.StatusOK || out.Body.String() != "actual file bytes" {
		t.Fatalf("download %d %s", out.Code, out.Body.String())
	}
	if !strings.HasPrefix(out.Header().Get("Content-Disposition"), "attachment") {
		t.Fatal("download may execute active content inline")
	}
}

func TestWorkspacePublishFilesPreservesFormatsAndDurableCopies(t *testing.T) {
	_, workspace, bot := workspaceFixture(t)
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	var zipData bytes.Buffer
	archive := zip.NewWriter(&zipData)
	entry, err := archive.Create("README.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("An archive artifact.")); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	fixtures := []struct {
		name, mime string
		data       []byte
	}{
		{"notes.txt", "text/plain", []byte("A document sent by the bot.")},
		{"plot.bin", "image/png", pngData.Bytes()},
		{"report.pdf", "application/pdf", []byte("%PDF-1.7\n1 0 obj\n<< /Type /Catalog >>\nendobj\n%%EOF\n")},
		{"bundle.zip", "application/zip", zipData.Bytes()},
		{"empty.txt", "text/plain", []byte{}},
	}
	paths := []string{}
	for index, fixture := range fixtures {
		path := filepath.Join(bot.WorkDir, "results", fixture.name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, fixture.data, 0600); err != nil {
			t.Fatal(err)
		}
		if index%2 == 0 {
			paths = append(paths, path)
		} else {
			paths = append(paths, filepath.Join("results", fixture.name))
		}
	}
	attachments, err := workspace.PublishFiles(bot.ID, paths)
	if err != nil {
		t.Fatal(err)
	}
	if len(attachments) != len(fixtures) {
		t.Fatalf("attachments=%+v", attachments)
	}
	for index, attachment := range attachments {
		fixture := fixtures[index]
		if attachment.Name != fixture.name || attachment.MimeType != fixture.mime || attachment.Path != "" || attachment.ID == "" || attachment.URL == "" {
			t.Fatalf("public artifact=%+v", attachment)
		}
		if _, err := os.Stat(filepath.Join(bot.WorkDir, "results", fixture.name)); err != nil {
			t.Fatal("publishing removed the original", err)
		}
	}
	if err := os.RemoveAll(filepath.Join(bot.WorkDir, "results")); err != nil {
		t.Fatal(err)
	}
	resolved, err := workspace.ResolveAttachments(bot.ID, attachments)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	workspace.RegisterHTTP(mux, nil)
	for index, attachment := range resolved {
		if !pathWithin(filepath.Join(bot.WorkDir, "uploads"), attachment.Path) {
			t.Fatal("published copy is outside durable upload storage")
		}
		out := httptest.NewRecorder()
		mux.ServeHTTP(out, httptest.NewRequest(http.MethodGet, attachment.URL, nil))
		if out.Code != http.StatusOK || !bytes.Equal(out.Body.Bytes(), fixtures[index].data) {
			t.Fatalf("artifact download=%d %q", out.Code, out.Body.Bytes())
		}
	}
}

func TestWorkspacePublishFilesRejectsOutsideAndSymlinkEscapeBeforeCopying(t *testing.T) {
	store, workspace, bot := workspaceFixture(t)
	other, err := store.CreateBot(Bot{Name: "Other workspace"})
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "private.txt")
	if err := os.WriteFile(outside, []byte("outside data must remain private"), 0600); err != nil {
		t.Fatal(err)
	}
	otherFile := filepath.Join(other.WorkDir, "result.txt")
	if err := os.WriteFile(otherFile, []byte("another bot's result"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(bot.WorkDir, "outside-link.txt")); err != nil {
		t.Fatal(err)
	}
	valid := filepath.Join(bot.WorkDir, "valid.txt")
	if err := os.WriteFile(valid, []byte("valid artifact"), 0600); err != nil {
		t.Fatal(err)
	}
	cases := []string{outside, otherFile, filepath.Join(store.UserDir(), "AGENTS.md"), "../../user/AGENTS.md", "outside-link.txt", bot.WorkDir}
	for _, path := range cases {
		if attachments, err := workspace.PublishFiles(bot.ID, []string{valid, path}); err == nil || len(attachments) != 0 {
			t.Fatalf("outside batch accepted: %q %+v %v", path, attachments, err)
		}
	}
	files, err := os.ReadDir(filepath.Join(bot.WorkDir, "uploads"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatal("invalid batch left partial durable artifacts")
	}
	content, err := os.ReadFile(outside)
	if err != nil || string(content) != "outside data must remain private" {
		t.Fatal("outside file was modified")
	}
}

func TestWorkspacePublishFilesAllowsInternalLinkAndKeepsCopiesIndependent(t *testing.T) {
	_, workspace, bot := workspaceFixture(t)
	original := filepath.Join(bot.WorkDir, "original.txt")
	if err := os.WriteFile(original, []byte("original snapshot"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(original, filepath.Join(bot.WorkDir, "latest.txt")); err != nil {
		t.Fatal(err)
	}
	attachments, err := workspace.PublishFiles(bot.ID, []string{"latest.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if attachments[0].Name != "latest.txt" {
		t.Fatalf("link name lost: %+v", attachments[0])
	}
	if err := os.WriteFile(original, []byte("modified source"), 0600); err != nil {
		t.Fatal(err)
	}
	resolved, err := workspace.ResolveAttachments(bot.ID, attachments)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(resolved[0].Path)
	if err != nil || string(data) != "original snapshot" {
		t.Fatal("published copy changed with its source")
	}
}

func TestWorkspacePublishFilesRejectsOversizeAndBatchLimits(t *testing.T) {
	_, workspace, bot := workspaceFixture(t)
	path := filepath.Join(bot.WorkDir, "large.bin")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxUploadBytes + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	file.Close()
	if _, err := workspace.PublishFiles(bot.ID, []string{"large.bin"}); err == nil {
		t.Fatal("oversized artifact accepted")
	}
	if _, err := workspace.PublishFiles(bot.ID, nil); err == nil {
		t.Fatal("empty artifact batch accepted")
	}
	if _, err := workspace.PublishFiles(bot.ID, make([]string, 11)); err == nil {
		t.Fatal("oversized artifact batch accepted")
	}
}

func TestWorkspacePublishFilesRejectsNamedPipeWithoutOpeningIt(t *testing.T) {
	_, workspace, bot := workspaceFixture(t)
	mkfifo, err := exec.LookPath("mkfifo")
	if err != nil {
		t.Skip("named-pipe fixture requires mkfifo")
	}
	path := filepath.Join(bot.WorkDir, "output.pipe")
	if err := exec.Command(mkfifo, path).Run(); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { _, err := workspace.PublishFiles(bot.ID, []string{"output.pipe"}); result <- err }()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("special file accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("publishing a named pipe blocked instead of rejecting it")
	}
}

func TestWorkspacePublishLongUnicodeFilenameKeepsResolvableMetadata(t *testing.T) {
	_, workspace, bot := workspaceFixture(t)
	name := strings.Repeat("ф", 100) + ".py"
	if err := os.WriteFile(filepath.Join(bot.WorkDir, name), []byte("print('artifact')\n"), 0600); err != nil {
		t.Fatal(err)
	}
	attachments, err := workspace.PublishFiles(bot.ID, []string{name})
	if err != nil {
		t.Fatal(err)
	}
	if len(attachments[0].Name) > 180 || !utf8.ValidString(attachments[0].Name) || !strings.HasSuffix(attachments[0].Name, ".py") {
		t.Fatalf("invalid public name=%q", attachments[0].Name)
	}
	resolved, err := workspace.ResolveAttachments(bot.ID, attachments)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(resolved[0].Path)
	if err != nil || string(data) != "print('artifact')\n" {
		t.Fatal("Unicode metadata no longer resolves its published copy")
	}
}

func TestWorkspaceFlovUsesDecodedWAVMultipart(t *testing.T) {
	_, workspace, _ := workspaceFixture(t)
	flov := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" {
			t.Errorf("path=%s", r.URL.Path)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
			out.WriteHeader(400)
			return
		}
		defer r.MultipartForm.RemoveAll()
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Error(err)
			out.WriteHeader(400)
			return
		}
		defer file.Close()
		data, _ := io.ReadAll(file)
		if header.Filename != "audio.wav" || string(data) != "decoded WAV" {
			t.Errorf("file=%s %q", header.Filename, data)
		}
		if r.FormValue("response_format") != "json" {
			t.Error("response format missing")
		}
		out.Header().Set("Content-Type", "application/json")
		io.WriteString(out, `{"text":"  Привет миру.  "}`)
	}))
	defer flov.Close()
	converted := false
	workspace.Configure(WorkspaceConfig{FlovURL: flov.URL + "/v1/audio/transcriptions", ConvertAudio: func(ctx context.Context, data []byte, format string) ([]byte, error) {
		converted = true
		if string(data) != "webm bytes" || format != "webm" {
			t.Errorf("conversion data=%q format=%s", data, format)
		}
		return []byte("decoded WAV"), nil
	}})
	text, err := workspace.Transcribe(context.Background(), []byte("webm bytes"), "webm")
	if err != nil || text != "Привет миру." || !converted {
		t.Fatalf("transcribe %q %v", text, err)
	}
}

func TestWorkspaceFlovFailureDoesNotExposeUpstreamBody(t *testing.T) {
	_, workspace, _ := workspaceFixture(t)
	flov := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		out.WriteHeader(503)
		io.WriteString(out, "token=private-secret")
	}))
	defer flov.Close()
	workspace.Configure(WorkspaceConfig{FlovURL: flov.URL, ConvertAudio: func(context.Context, []byte, string) ([]byte, error) { return []byte("wav"), nil }})
	_, err := workspace.Transcribe(context.Background(), []byte("audio"), "wav")
	if err == nil || strings.Contains(err.Error(), "private-secret") {
		t.Fatalf("err=%v", err)
	}
}

func TestMaintenanceDeletesOnlyOldTemporaryFilesAndRunsOnceDaily(t *testing.T) {
	store, _, bot := workspaceFixture(t)
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	old := filepath.Join(bot.WorkDir, "tmp", "old.txt")
	fresh := filepath.Join(bot.WorkDir, "tmp", "fresh.txt")
	durable := filepath.Join(bot.WorkDir, "result.txt")
	outside := filepath.Join(t.TempDir(), "keep.txt")
	for _, path := range []string{old, fresh, durable, outside} {
		if err := os.WriteFile(path, []byte("keep or remove"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{old, durable, outside} {
		if err := os.Chtimes(path, now.Add(-48*time.Hour), now.Add(-48*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(fresh, now, now); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(bot.WorkDir, "tmp", "external")); err != nil {
		t.Fatal(err)
	}
	maintenance := NewMaintenance(store, nil)
	defer maintenance.Close()
	maintenance.now = func() time.Time { return now }
	calls := 0
	maintenance.runner = func(ctx context.Context, settings MaintenanceSettings, dir, manifest string) (string, error) {
		calls++
		if dir != filepath.Join(bot.WorkDir, "tmp") || (calls == 1 && !strings.Contains(manifest, "old.txt")) || strings.Contains(manifest, "keep.txt") {
			t.Errorf("inventory escaped temp: %s", manifest)
		}
		if settings.Model != "gpt-6-luna" {
			t.Errorf("model=%s", settings.Model)
		}
		return "One old temporary file can be cleaned.", nil
	}
	if err := maintenance.Run(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("old file remains %v", err)
	}
	for _, path := range []string{fresh, durable, outside} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("safe file removed: %s: %v", path, err)
		}
	}
	if err := maintenance.Run(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("daily agent called %d times", calls)
	}
	reloaded := NewMaintenance(store, nil)
	defer reloaded.Close()
	reloaded.now = maintenance.now
	reloaded.runner = maintenance.runner
	if err := reloaded.Run(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("restart repeated successful inventory")
	}
	now = now.Add(24 * time.Hour)
	if err := reloaded.Run(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("next day inventory did not run")
	}
}

func TestMaintenanceFailureRetainsFilesAndBacksOff(t *testing.T) {
	store, _, bot := workspaceFixture(t)
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(bot.WorkDir, "tmp", "old.txt")
	if err := os.WriteFile(path, []byte("preserve until inventory succeeds"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, now.Add(-48*time.Hour), now.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	maintenance := NewMaintenance(store, nil)
	defer maintenance.Close()
	maintenance.now = func() time.Time { return now }
	calls := 0
	maintenance.runner = func(context.Context, MaintenanceSettings, string, string) (string, error) {
		calls++
		return "", errors.New("model unavailable")
	}
	if err := maintenance.Run(context.Background(), false); err == nil {
		t.Fatal("failure was ignored")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("failed inventory deleted a file")
	}
	if err := maintenance.Run(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("failed inventory retried without backoff")
	}
	now = now.Add(2 * time.Hour)
	maintenance.runner = func(context.Context, MaintenanceSettings, string, string) (string, error) {
		calls++
		return "Inventory complete.", nil
	}
	if err := maintenance.Run(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("failed inventory was not retried")
	}
}

func TestMaintenanceMissingTemporaryDirectoryIsHarmless(t *testing.T) {
	store, _, bot := workspaceFixture(t)
	if err := os.RemoveAll(filepath.Join(bot.WorkDir, "tmp")); err != nil {
		t.Fatal(err)
	}
	maintenance := NewMaintenance(store, nil)
	defer maintenance.Close()
	if err := maintenance.Run(context.Background(), false); err != nil {
		t.Fatal(err)
	}
}

func TestMaintenanceDefersCleanupWhenUserStartsTurnDuringInventory(t *testing.T) {
	store, _, bot := workspaceFixture(t)
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(bot.WorkDir, "tmp", "old.txt")
	if err := os.WriteFile(path, []byte("working file"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, now.Add(-48*time.Hour), now.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	session := &maintenanceTestSession{events: make(chan core.Event, 8), sent: make(chan struct{})}
	session.alive.Store(true)
	runtime := NewRuntime(store, RuntimeConfig{AgentOptions: runtimeFakeAgentOptions(), AgentFactory: func(string, map[string]any) (core.Agent, error) { return &maintenanceTestAgent{session: session}, nil }})
	defer runtime.Close()
	maintenance := NewMaintenance(store, runtime)
	defer maintenance.Close()
	maintenance.now = func() time.Time { return now }
	started, release := make(chan struct{}), make(chan struct{})
	maintenance.runner = func(ctx context.Context, _ MaintenanceSettings, _, _ string) (string, error) {
		close(started)
		select {
		case <-release:
			return "Inventory complete", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	finished := make(chan error, 1)
	go func() { finished <- maintenance.Run(context.Background(), false) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("inventory did not start")
	}
	if _, err := runtime.SendMessage(context.Background(), bot.ID, MessageRequest{Text: "Work on my temporary files."}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-session.sent:
	case <-time.After(5 * time.Second):
		t.Fatal("user turn did not start")
	}
	close(release)
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup blocked on an active user turn")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("maintenance removed a file while its bot was working")
	}
	disk, _, err := maintenance.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, report := range disk.Reports {
		if report.BotID == bot.ID {
			found = true
			if report.Status != "deferred" || report.RemovedFiles != 0 {
				t.Fatalf("report=%+v", report)
			}
		}
	}
	if !found {
		t.Fatal("deferred report missing")
	}
}

func TestMaintenancePreservesFilesReplacedWhileJuniorRuns(t *testing.T) {
	store, _, bot := workspaceFixture(t)
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	old := now.Add(-48 * time.Hour)
	path := filepath.Join(bot.WorkDir, "tmp", "old.txt")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	maintenance := NewMaintenance(store, nil)
	defer maintenance.Close()
	maintenance.now = func() time.Time { return now }
	maintenance.runner = func(context.Context, MaintenanceSettings, string, string) (string, error) {
		replacement := filepath.Join(bot.WorkDir, "tmp", "replacement.txt")
		if err := os.WriteFile(replacement, []byte("new important work"), 0600); err != nil {
			return "", err
		}
		if err := os.Chtimes(replacement, old, old); err != nil {
			return "", err
		}
		if err := os.Rename(replacement, path); err != nil {
			return "", err
		}
		return "Inventory complete", nil
	}
	if err := maintenance.Run(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "new important work" {
		t.Fatalf("replacement lost: %q %v", data, err)
	}
}

func TestMaintenanceCleanupPreservesIdleConversationAdapter(t *testing.T) {
	store, _, bot := workspaceFixture(t)
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(bot.WorkDir, "tmp", "old.txt")
	if err := os.WriteFile(path, []byte("temporary"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, now.Add(-48*time.Hour), now.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	session := &maintenanceTestSession{events: make(chan core.Event, 8), sent: make(chan struct{})}
	session.alive.Store(true)
	runtime := NewRuntime(store, RuntimeConfig{AgentOptions: runtimeFakeAgentOptions(), AgentFactory: func(string, map[string]any) (core.Agent, error) { return &maintenanceTestAgent{session: session}, nil }})
	defer runtime.Close()
	turn, err := runtime.SendMessage(context.Background(), bot.ID, MessageRequest{Text: "Hello."})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-session.sent:
	case <-time.After(5 * time.Second):
		t.Fatal("conversation did not start")
	}
	session.events <- core.Event{Type: core.EventResult, Content: "Done.", SessionID: session.CurrentSessionID()}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := runtime.WaitTurn(ctx, bot.ID, turn); err != nil {
		t.Fatal(err)
	}
	maintenance := NewMaintenance(store, runtime)
	defer maintenance.Close()
	maintenance.now = func() time.Time { return now }
	maintenance.runner = func(context.Context, MaintenanceSettings, string, string) (string, error) {
		return "Inventory complete.", nil
	}
	if err := maintenance.Run(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if !session.Alive() {
		t.Fatal("temporary cleanup invalidated the conversation adapter and could pause its native goal")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("old temporary file was not cleaned")
	}
}

func TestMaintenanceRejectsTempRootSymlinkSwapDuringInventory(t *testing.T) {
	store, _, bot := workspaceFixture(t)
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	old := now.Add(-48 * time.Hour)
	root := filepath.Join(bot.WorkDir, "tmp")
	path := filepath.Join(root, "old.txt")
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "old.txt")
	for _, p := range []string{path, outsideFile} {
		if err := os.WriteFile(p, []byte("preserve"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	maintenance := NewMaintenance(store, nil)
	defer maintenance.Close()
	maintenance.now = func() time.Time { return now }
	maintenance.runner = func(context.Context, MaintenanceSettings, string, string) (string, error) {
		if err := os.Rename(root, root+"-original"); err != nil {
			return "", err
		}
		if err := os.Symlink(outside, root); err != nil {
			return "", err
		}
		return "Inventory complete", nil
	}
	if err := maintenance.Run(context.Background(), false); err == nil {
		t.Fatal("temp-root symlink swap was accepted")
	}
	if _, err := os.Stat(outsideFile); err != nil {
		t.Fatal("outside file removed after temp-root swap")
	}
}

func TestMaintenanceValidatesBoundedEffortsForBackend(t *testing.T) {
	settings := MaintenanceSettings{Enabled: true, Backend: "pi", Model: "deepseek/deepseek-flash", Effort: "off", RetentionHours: 24}
	if err := validateMaintenanceSettings(settings); err != nil {
		t.Fatal(err)
	}
	settings.Backend = "codex"
	if err := validateMaintenanceSettings(settings); err == nil {
		t.Fatal("Codex off accepted")
	}
	settings.Effort = "ultra"
	if err := validateMaintenanceSettings(settings); err == nil {
		t.Fatal("unbounded ultra inventory accepted")
	}
}

func TestMaintenanceUsesDedicatedCodexConnectionWithFreshInventoryContext(t *testing.T) {
	store, _, bot := workspaceFixture(t)
	if err := os.WriteFile(filepath.Join(bot.WorkDir, "tmp", "draft.txt"), []byte("keep this draft"), 0600); err != nil {
		t.Fatal(err)
	}
	connection := map[string]any{
		"app_server_url":         "unix:///private/connect-bots/codex.sock",
		"cmd":                    "private-codex --transport-option",
		"codex_home":             "/private/native-codex",
		"env":                    map[string]string{"CODEX_HOME": "/private/native-codex", "OPENAI_API_KEY": "fixture-auth"},
		"model":                  "persistent-bot-model",
		"work_dir":               "/persistent/bot",
		"developer_instructions": "Coordinate bots and create goals.",
		"system_prompt":          "Persistent bot system prompt.",
		"append_system_prompt":   "Persistent bot appended prompt.",
		"cli_args":               []string{"--extension", "/persistent/coordinator"},
		"dynamic_tools":          []map[string]any{{"name": "bots_send"}},
		"app_server_config":      map[string]any{"sandbox_mode": "danger-full-access", "approval_policy": "always", "skills.config": []any{"persistent-bot-skills"}},
	}
	factory := &runtimeFakeFactory{sends: make(chan runtimeFakeSend, 1)}
	runtime := NewRuntime(store, RuntimeConfig{
		AgentOptions: map[string]map[string]any{"codex": connection},
		AgentFactory: func(backend string, opts map[string]any) (core.Agent, error) {
			agent, err := factory.create(backend, opts)
			if err == nil {
				agent.(*runtimeFakeAgent).session.emit(core.Event{Type: core.EventResult, Content: "Retain the fresh draft.", Done: true})
			}
			return agent, err
		},
		Instructions: func(string) (string, error) { t.Fatal("inventory inherited bot instructions"); return "", nil },
		SessionOptions: func(string) (map[string]any, error) {
			t.Fatal("inventory inherited bot session options")
			return nil, nil
		},
	})
	defer runtime.Close()
	maintenance := NewMaintenance(store, runtime)
	defer maintenance.Close()
	if err := maintenance.Run(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if len(factory.created) != 1 {
		t.Fatalf("inventory agents created=%d", len(factory.created))
	}
	agent := factory.created[0]
	opts := agent.opts
	for _, key := range []string{"app_server_url", "cmd", "codex_home"} {
		if opts[key] != connection[key] {
			t.Fatalf("inventory lost connection field %s", key)
		}
	}
	env := runtimeEnv(opts["env"])
	if env["CODEX_HOME"] != "/private/native-codex" || env["OPENAI_API_KEY"] != "fixture-auth" {
		t.Fatal("inventory lost native home or connection authentication")
	}
	if opts["backend"] != "app-server" || opts["model"] != "gpt-6-luna" || opts["reasoning_effort"] != "max" || opts["work_dir"] != filepath.Join(bot.WorkDir, "tmp") {
		t.Fatal("inventory did not use its own model, backend, and temporary directory")
	}
	for _, key := range []string{"system_prompt", "append_system_prompt", "cli_args", "dynamic_tools"} {
		if _, present := opts[key]; present {
			t.Fatalf("inventory inherited persistent bot field %s", key)
		}
	}
	if instructions, _ := opts["developer_instructions"].(string); !strings.Contains(instructions, "Do not coordinate bots, create goals") || strings.Contains(instructions, "Coordinate bots and create goals.") {
		t.Fatal("inventory did not isolate its bounded instructions")
	}
	config := opts["app_server_config"].(map[string]any)
	if len(config) != 2 || config["sandbox_mode"] != "read-only" || config["approval_policy"] != "never" {
		t.Fatal("inventory inherited persistent bot configuration")
	}
	if agent.resume != "" || agent.observer != nil || len(agent.tools) != 0 {
		t.Fatal("inventory reused a persistent thread or orchestration callbacks")
	}
	if connection["app_server_config"].(map[string]any)["sandbox_mode"] != "danger-full-access" {
		t.Fatal("inventory mutated runtime connection configuration")
	}
	events, err := store.Events(bot.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type == "message" || event.Type == "turn" {
			t.Fatal("inventory leaked into the user's conversation")
		}
	}
}

func TestMaintenanceRejectsMissingCodexConnectionInsteadOfSharedFallback(t *testing.T) {
	for _, endpoint := range []string{"no-runtime", "no-endpoint", "", "managed://"} {
		t.Run(endpoint, func(t *testing.T) {
			store, _, bot := workspaceFixture(t)
			path := filepath.Join(bot.WorkDir, "tmp", "old.txt")
			if err := os.WriteFile(path, []byte("preserve until inventory connects"), 0600); err != nil {
				t.Fatal(err)
			}
			old := time.Now().Add(-48 * time.Hour)
			if err := os.Chtimes(path, old, old); err != nil {
				t.Fatal(err)
			}
			var runtime *Runtime
			calls := 0
			if endpoint != "no-runtime" {
				opts := map[string]any{}
				if endpoint != "no-endpoint" {
					opts["app_server_url"] = endpoint
				}
				runtime = NewRuntime(store, RuntimeConfig{
					AgentOptions: map[string]map[string]any{"codex": opts},
					AgentFactory: func(string, map[string]any) (core.Agent, error) {
						calls++
						return nil, errors.New("unexpected shared inventory connection")
					},
				})
				defer runtime.Close()
			}
			maintenance := NewMaintenance(store, runtime)
			defer maintenance.Close()
			if err := maintenance.Run(context.Background(), false); err == nil {
				t.Fatal("inventory accepted a missing dedicated connection")
			}
			if calls != 0 {
				t.Fatal("inventory attempted to construct a fallback agent")
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatal("inventory without a dedicated connection removed a temporary file")
			}
		})
	}
}

func TestMaintenancePiRemainsIsolatedFromPersistentBotExtensions(t *testing.T) {
	store, _, bot := workspaceFixture(t)
	if err := os.WriteFile(filepath.Join(bot.WorkDir, "tmp", "draft.txt"), []byte("fresh"), 0600); err != nil {
		t.Fatal(err)
	}
	factory := &runtimeFakeFactory{sends: make(chan runtimeFakeSend, 1)}
	runtime := NewRuntime(store, RuntimeConfig{
		AgentOptions: map[string]map[string]any{
			"codex": {"app_server_url": "unix:///private/connect-bots/codex.sock"},
			"pi":    {"cli_args": []string{"--extension", "/persistent/coordinator"}, "env": map[string]string{"CONNECT_BOTS_INTERNAL_TOKEN": "fixture-internal"}},
		},
		AgentFactory: func(backend string, opts map[string]any) (core.Agent, error) {
			agent, err := factory.create(backend, opts)
			if err == nil {
				agent.(*runtimeFakeAgent).session.emit(core.Event{Type: core.EventResult, Content: "Retain the fresh draft.", Done: true})
			}
			return agent, err
		},
	})
	defer runtime.Close()
	maintenance := NewMaintenance(store, runtime)
	defer maintenance.Close()
	maintenance.disk.Backend, maintenance.disk.Model, maintenance.disk.Effort = "pi", "deepseek/deepseek-flash", "off"
	if err := maintenance.Run(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if len(factory.created) != 1 || factory.created[0].backend != "pi" {
		t.Fatal("inventory did not use its chosen Pi backend")
	}
	opts := factory.created[0].opts
	if _, present := opts["app_server_url"]; present {
		t.Fatal("Pi inventory inherited the Codex connection")
	}
	if runtimeEnv(opts["env"])["CONNECT_BOTS_INTERNAL_TOKEN"] != "" {
		t.Fatal("Pi inventory inherited bot orchestration credentials")
	}
	args, err := runtimeArgs(opts["cli_args"])
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, flag := range []string{"--no-tools", "--no-context-files", "--no-prompt-templates", "--no-extensions", "--no-skills", "--no-session", "--append-system-prompt"} {
		if !strings.Contains(joined, flag) {
			t.Fatalf("Pi inventory lost isolation flag %s", flag)
		}
	}
	if strings.Contains(joined, "/persistent/coordinator") || !strings.Contains(joined, "Do not coordinate bots, create goals") {
		t.Fatal("Pi inventory inherited persistent extensions or lost bounded instructions")
	}
	if opts["session_dir"] != filepath.Join(bot.WorkDir, "state", "maintenance", "pi") || opts["thinking"] != "off" {
		t.Fatal("Pi inventory lost isolated state or selected effort")
	}
}

type maintenanceTestAgent struct{ session *maintenanceTestSession }

func (*maintenanceTestAgent) Name() string { return "codex" }
func (a *maintenanceTestAgent) StartSession(context.Context, string) (core.AgentSession, error) {
	return a.session, nil
}
func (*maintenanceTestAgent) ListSessions(context.Context) ([]core.AgentSessionInfo, error) {
	return nil, nil
}
func (*maintenanceTestAgent) Stop() error { return nil }

type maintenanceTestSession struct {
	events    chan core.Event
	sent      chan struct{}
	alive     atomic.Bool
	sendOnce  sync.Once
	closeOnce sync.Once
}

func (s *maintenanceTestSession) Send(string, string, []core.ImageAttachment, []core.FileAttachment) error {
	s.sendOnce.Do(func() { close(s.sent) })
	return nil
}
func (*maintenanceTestSession) RespondPermission(string, core.PermissionResult) error { return nil }
func (s *maintenanceTestSession) Events() <-chan core.Event                           { return s.events }
func (*maintenanceTestSession) CurrentSessionID() string                              { return "maintenance-test-thread" }
func (s *maintenanceTestSession) Alive() bool                                         { return s.alive.Load() }
func (s *maintenanceTestSession) Close() error {
	s.closeOnce.Do(func() { s.alive.Store(false); close(s.events) })
	return nil
}
