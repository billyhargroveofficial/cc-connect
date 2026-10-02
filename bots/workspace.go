package bots

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/chenhg5/cc-connect/core"
	"gopkg.in/yaml.v3"
)

const maxWorkspaceFile = 1 << 20
const maxUploadBytes = 25 << 20

var skillNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)
var attachmentIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

type WorkspaceConfig struct {
	FlovURL string
	// NativeSkillDirs are shared between harnesses; BackendSkillDirs are
	// advertised only for that harness (or in the combined global editor).
	NativeSkillDirs  []string
	BackendSkillDirs map[string][]string
	SystemSkillDirs  []string
	HTTPClient       *http.Client
	ConvertAudio     func(context.Context, []byte, string) ([]byte, error)
}

// Workspace exposes only managed instruction files, discovered skills, and
// server-created uploads. Client paths never become arbitrary filesystem paths.
type Workspace struct {
	store  *Store
	mu     sync.Mutex
	config WorkspaceConfig
}

func NewWorkspace(store *Store) *Workspace {
	home, _ := os.UserHomeDir()
	w := &Workspace{store: store, config: WorkspaceConfig{
		FlovURL:         "http://127.0.0.1:17432/v1/audio/transcriptions",
		NativeSkillDirs: []string{filepath.Join(home, ".agents", "skills")},
		BackendSkillDirs: map[string][]string{
			"codex": {filepath.Join(home, ".codex", "skills")},
			"pi":    {filepath.Join(home, ".pi", "agent", "skills"), filepath.Join(home, ".pi", "skills")},
		},
		SystemSkillDirs: []string{filepath.Join(home, ".codex", "skills", ".system")},
		HTTPClient:      &http.Client{}, ConvertAudio: core.ConvertAudioToWAV,
	}}
	return w
}

func (w *Workspace) Configure(config WorkspaceConfig) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if config.FlovURL != "" {
		w.config.FlovURL = config.FlovURL
	}
	if config.NativeSkillDirs != nil {
		w.config.NativeSkillDirs = append([]string(nil), config.NativeSkillDirs...)
	}
	if config.BackendSkillDirs != nil {
		w.config.BackendSkillDirs = make(map[string][]string, len(config.BackendSkillDirs))
		for backend, dirs := range config.BackendSkillDirs {
			w.config.BackendSkillDirs[backend] = append([]string(nil), dirs...)
		}
	}
	if config.SystemSkillDirs != nil {
		w.config.SystemSkillDirs = append([]string(nil), config.SystemSkillDirs...)
	}
	if config.HTTPClient != nil {
		w.config.HTTPClient = config.HTTPClient
	}
	if config.ConvertAudio != nil {
		w.config.ConvertAudio = config.ConvertAudio
	}
}

func (w *Workspace) settings() WorkspaceConfig {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.config
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// noSymlinkPath checks every existing component, including the destination.
// The product state root and managed files are owner-only; keeping links out of
// write paths prevents an agent-created symlink from redirecting a UI edit.
func noSymlinkPath(root, path string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return err
	}
	if !pathWithin(root, path) {
		return errors.New("path is outside its workspace")
	}
	// Check ancestors as well, so a symlink in the configured root is rejected.
	current := string(filepath.Separator)
	for _, component := range strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, statErr := os.Lstat(current)
		if os.IsNotExist(statErr) {
			continue
		}
		if statErr != nil {
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("managed paths must not contain symbolic links")
		}
	}
	return nil
}

func writeWorkspaceAtomic(root, path string, content []byte) error {
	if err := noSymlinkPath(root, path); err != nil {
		return err
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer rootHandle.Close()
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return err
	}
	if err := rootHandle.MkdirAll(filepath.Dir(rel), 0700); err != nil {
		return err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(rel), ".connect-write-"+hex.EncodeToString(nonce[:]))
	f, err := rootHandle.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer rootHandle.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(content)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := noSymlinkPath(root, path); err != nil {
		return err
	}
	return rootHandle.Rename(tmp, rel)
}

func readWorkspaceFile(path string) (string, error) {
	// Open through a directory capability as well as checking paths. This keeps
	// a symlink replacement between validation and opening inside the same tree.
	parent := filepath.Dir(filepath.Dir(path))
	root, err := os.OpenRoot(parent)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer root.Close()
	f, err := root.Open(filepath.Join(filepath.Base(filepath.Dir(path)), filepath.Base(path)))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("expected a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxWorkspaceFile+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxWorkspaceFile {
		return "", errors.New("instruction or skill exceeds 1 MiB")
	}
	return string(data), nil
}

func (w *Workspace) Instructions(botID string) (string, string, error) {
	dir, err := w.store.BotDir(botID)
	if err != nil {
		return "", "", err
	}
	path := filepath.Join(dir, "AGENTS.md")
	if err := noSymlinkPath(w.store.Root(), path); err != nil {
		return "", "", err
	}
	content, err := readWorkspaceFile(path)
	return content, path, err
}

func (w *Workspace) UserInstructions() (string, string, error) {
	path := filepath.Join(w.store.UserDir(), "AGENTS.md")
	if err := noSymlinkPath(w.store.Root(), path); err != nil {
		return "", "", err
	}
	content, err := readWorkspaceFile(path)
	return content, path, err
}

func (w *Workspace) SetInstructions(botID, content string) error {
	if len(content) > maxWorkspaceFile {
		return errors.New("instructions exceed 1 MiB")
	}
	var path string
	if botID == "" {
		path = filepath.Join(w.store.UserDir(), "AGENTS.md")
	} else {
		dir, err := w.store.BotDir(botID)
		if err != nil {
			return err
		}
		path = filepath.Join(dir, "AGENTS.md")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return writeWorkspaceAtomic(w.store.Root(), path, []byte(content))
}

// AgentInstructions adds the dedicated product-user layer. Native user and
// project instructions continue to be discovered by each underlying harness.
func (w *Workspace) AgentInstructions(botID string) (string, error) {
	if _, err := w.store.GetBot(botID); err != nil {
		return "", err
	}
	content, _, err := w.UserInstructions()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(content) == "" {
		return "", nil
	}
	return "Connect Bots shared user instructions:\n" + content, nil
}

type skillMetadata struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

func parseSkill(content string) (skillMetadata, error) {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	if !strings.HasPrefix(content, "---\n") {
		return skillMetadata{}, errors.New("SKILL.md must begin with YAML frontmatter")
	}
	end := strings.Index(content[4:], "\n---")
	if end < 0 {
		return skillMetadata{}, errors.New("SKILL.md frontmatter is not closed")
	}
	var metadata skillMetadata
	if err := yaml.Unmarshal([]byte(content[4:4+end]), &metadata); err != nil {
		return metadata, fmt.Errorf("invalid skill frontmatter: %w", err)
	}
	if strings.TrimSpace(metadata.Name) == "" || strings.TrimSpace(metadata.Description) == "" {
		return metadata, errors.New("skill name and description are required")
	}
	return metadata, nil
}

type skillRoot struct {
	path, scope string
	editable    bool
}

func (w *Workspace) skillRoots(botID string) ([]skillRoot, error) {
	config := w.settings()
	backend := ""
	if botID != "" {
		bot, err := w.store.GetBot(botID)
		if err != nil {
			return nil, err
		}
		backend = bot.Backend
	}
	roots := []skillRoot{}
	for _, dir := range config.NativeSkillDirs {
		roots = append(roots, skillRoot{dir, "nativeUser", true})
	}
	for _, nativeBackend := range []string{"codex", "pi"} {
		if backend == "" || backend == nativeBackend {
			for _, dir := range config.BackendSkillDirs[nativeBackend] {
				roots = append(roots, skillRoot{dir, "nativeUser", true})
			}
		}
	}
	if backend == "" || backend == "codex" {
		for _, dir := range config.SystemSkillDirs {
			roots = append(roots, skillRoot{dir, "system", false})
		}
	}
	roots = append(roots, skillRoot{filepath.Join(w.store.UserDir(), "skills"), "productUser", true})
	if botID != "" {
		dir, err := w.store.BotDir(botID)
		if err != nil {
			return nil, err
		}
		roots = append(roots, skillRoot{filepath.Join(dir, ".agents", "skills"), "project", true})
	}
	return roots, nil
}

func resolvedAllowed(path string, roots []skillRoot) (string, skillRoot, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", skillRoot{}, err
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", skillRoot{}, err
	}
	// A system subtree wins over its enclosing native-user root.
	ordered := append([]skillRoot(nil), roots...)
	sort.SliceStable(ordered, func(i, j int) bool { return len(ordered[i].path) > len(ordered[j].path) })
	for _, root := range ordered {
		rootPath, err := filepath.EvalSymlinks(root.path)
		if err != nil {
			continue
		}
		rootPath, err = filepath.Abs(rootPath)
		if err == nil && pathWithin(rootPath, resolved) {
			return resolved, root, nil
		}
	}
	return "", skillRoot{}, errors.New("skill path is outside configured skill roots")
}

func (w *Workspace) listSkills(botID string) ([]Skill, error) {
	roots, err := w.skillRoots(botID)
	if err != nil {
		return nil, err
	}
	disabled := map[string]bool{}
	if botID != "" {
		bot, err := w.store.GetBot(botID)
		if err != nil {
			return nil, err
		}
		for _, path := range bot.DisabledSkills {
			disabled[path] = true
		}
	}
	seen := map[string]bool{}
	skills := []Skill{}
	for _, root := range roots {
		entries, err := os.ReadDir(root.path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			path := filepath.Join(root.path, entry.Name(), "SKILL.md")
			resolved, actualRoot, err := resolvedAllowed(path, roots)
			if err != nil {
				continue
			}
			if seen[resolved] {
				continue
			}
			content, err := readWorkspaceFile(resolved)
			if err != nil {
				return nil, err
			}
			metadata, err := parseSkill(content)
			if err != nil {
				continue
			}
			seen[resolved] = true
			skills = append(skills, Skill{ID: resolved, Name: metadata.Name, Description: metadata.Description, Path: resolved, Scope: actualRoot.scope, Enabled: !disabled[resolved], Editable: actualRoot.editable})
		}
	}
	sort.Slice(skills, func(i, j int) bool {
		if skills[i].Scope != skills[j].Scope {
			return skills[i].Scope < skills[j].Scope
		}
		return skills[i].Name < skills[j].Name
	})
	return skills, nil
}

func (w *Workspace) BotSkills(botID string) ([]Skill, error) { return w.listSkills(botID) }
func (w *Workspace) UserSkills() ([]Skill, error)            { return w.listSkills("") }

func (w *Workspace) knownSkill(path string) (Skill, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return Skill{}, err
	}
	all, err := w.UserSkills()
	if err != nil {
		return Skill{}, err
	}
	for _, bot := range w.store.ListBots() {
		skills, err := w.BotSkills(bot.ID)
		if err != nil {
			return Skill{}, err
		}
		all = append(all, skills...)
	}
	for _, skill := range all {
		if skill.Path == path {
			return skill, nil
		}
	}
	return Skill{}, errors.New("unknown skill path")
}

func (w *Workspace) SkillContent(path string) (string, error) {
	skill, err := w.knownSkill(path)
	if err != nil {
		return "", err
	}
	return readWorkspaceFile(skill.Path)
}

func (w *Workspace) SetSkillContent(path, content string) error {
	if len(content) > maxWorkspaceFile {
		return errors.New("skill exceeds 1 MiB")
	}
	if _, err := parseSkill(content); err != nil {
		return err
	}
	skill, err := w.knownSkill(path)
	if err != nil {
		return err
	}
	if !skill.Editable {
		return errors.New("system skills are read-only")
	}
	// Resolution above maps legitimate user-skill links to their configured root;
	// write only the canonical regular path, never the link itself.
	w.mu.Lock()
	defer w.mu.Unlock()
	return writeWorkspaceAtomic(filepath.Dir(filepath.Dir(skill.Path)), skill.Path, []byte(content))
}

func (w *Workspace) CreateSkill(botID, name, content string) (Skill, error) {
	if !skillNamePattern.MatchString(name) {
		return Skill{}, errors.New("skill name must contain 1–64 letters, digits, hyphens, or underscores")
	}
	var root string
	if botID == "" {
		root = filepath.Join(w.store.UserDir(), "skills")
	} else {
		dir, err := w.store.BotDir(botID)
		if err != nil {
			return Skill{}, err
		}
		root = filepath.Join(dir, ".agents", "skills")
	}
	if content == "" {
		content = fmt.Sprintf("---\nname: %s\ndescription: Describe when this skill should be used.\n---\n\n# %s\n\nDescribe the workflow and its checks.\n", name, name)
	}
	if len(content) > maxWorkspaceFile {
		return Skill{}, errors.New("skill exceeds 1 MiB")
	}
	metadata, err := parseSkill(content)
	if err != nil {
		return Skill{}, err
	}
	if metadata.Name != name {
		return Skill{}, errors.New("frontmatter name must match the skill name")
	}
	path := filepath.Join(root, name, "SKILL.md")
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := noSymlinkPath(w.store.Root(), path); err != nil {
		return Skill{}, err
	}
	if _, err := os.Lstat(filepath.Dir(path)); err == nil {
		return Skill{}, errors.New("skill already exists")
	} else if !os.IsNotExist(err) {
		return Skill{}, err
	}
	if err := writeWorkspaceAtomic(w.store.Root(), path, []byte(content)); err != nil {
		return Skill{}, err
	}
	scope := "project"
	if botID == "" {
		scope = "productUser"
	}
	return Skill{ID: path, Name: name, Description: metadata.Description, Path: path, Scope: scope, Enabled: true, Editable: true}, nil
}

func (w *Workspace) SetDisabledSkills(botID string, paths []string) (Bot, error) {
	skills, err := w.BotSkills(botID)
	if err != nil {
		return Bot{}, err
	}
	// Keep the other harness's choices when the owner changes backends. They
	// are not advertised or applied to this session, but remain valid IDs in
	// the global editor and should not break saving the visible skill list.
	nativeSkills, err := w.UserSkills()
	if err != nil {
		return Bot{}, err
	}
	skills = append(skills, nativeSkills...)
	known := map[string]bool{}
	for _, skill := range skills {
		known[skill.ID] = true
	}
	seen := map[string]bool{}
	clean := []string{}
	for _, path := range paths {
		if !known[path] {
			return Bot{}, errors.New("disabled skills must refer to installed skills")
		}
		if !seen[path] {
			clean = append(clean, path)
			seen[path] = true
		}
	}
	return w.store.UpdateBot(botID, func(bot *Bot) error { bot.DisabledSkills = clean; return nil })
}

// SkillSessionOptions scopes disabled skills to this thread. Shared product
// skills are exposed through managed project links so Codex's actual discovery
// sees their full workflow and assets; native global config is never rewritten.
func (w *Workspace) SkillSessionOptions(botID string) (map[string]any, error) {
	bot, err := w.store.GetBot(botID)
	if err != nil {
		return nil, err
	}
	skills, err := w.BotSkills(botID)
	if err != nil {
		return nil, err
	}
	digest := sha256.New()
	for _, skill := range skills {
		content, err := readWorkspaceFile(skill.Path)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(digest, "%s\x00%t\x00%s\x00", skill.Path, skill.Enabled, content)
	}
	skillDigest := hex.EncodeToString(digest.Sum(nil))
	if bot.Backend == "pi" {
		args := []string{"--no-skills"}
		for _, skill := range skills {
			if skill.Enabled {
				args = append(args, "--skill", skill.Path)
			}
		}
		return map[string]any{"cli_args": args, "product_skill_digest": skillDigest}, nil
	}
	if err := w.syncSharedSkillLinks(botID, skills); err != nil {
		return nil, err
	}
	disabled := []map[string]any{}
	for _, skill := range skills {
		if !skill.Enabled {
			disabled = append(disabled, map[string]any{"path": skill.Path, "enabled": false})
		}
	}
	return map[string]any{"app_server_config": map[string]any{"skills.config": disabled}, "product_skill_digest": skillDigest}, nil
}

func (w *Workspace) syncSharedSkillLinks(botID string, skills []Skill) error {
	dir, err := w.store.BotDir(botID)
	if err != nil {
		return err
	}
	root := filepath.Join(dir, ".agents", "skills")
	if err := noSymlinkPath(w.store.Root(), root); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	productRoot, err := os.OpenRoot(w.store.Root())
	if err != nil {
		return err
	}
	defer productRoot.Close()
	rel, err := filepath.Rel(w.store.Root(), root)
	if err != nil {
		return err
	}
	if err := productRoot.MkdirAll(rel, 0700); err != nil {
		return err
	}
	skillRoot, err := productRoot.OpenRoot(rel)
	if err != nil {
		return err
	}
	defer skillRoot.Close()
	wanted := map[string]string{}
	for _, skill := range skills {
		if skill.Scope == "productUser" && skill.Enabled {
			// A private prefix keeps application links separate from user projects.
			name := "connect-user-" + filepath.Base(filepath.Dir(skill.Path))
			wanted[name] = filepath.Dir(skill.Path)
		}
	}
	directory, err := skillRoot.Open(".")
	if err != nil {
		return err
	}
	entries, err := directory.ReadDir(-1)
	directory.Close()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "connect-user-") {
			continue
		}
		info, err := skillRoot.Lstat(entry.Name())
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			if _, needed := wanted[entry.Name()]; needed {
				return errors.New("managed shared-skill link collides with a project file")
			}
			continue
		}
		target, err := skillRoot.Readlink(entry.Name())
		if err != nil {
			return err
		}
		if wanted[entry.Name()] == target {
			delete(wanted, entry.Name())
			continue
		}
		if _, needed := wanted[entry.Name()]; !needed && !pathWithin(filepath.Join(w.store.UserDir(), "skills"), target) {
			continue
		}
		if err := skillRoot.Remove(entry.Name()); err != nil {
			return err
		}
	}
	for name, target := range wanted {
		if err := skillRoot.Symlink(target, name); err != nil {
			return err
		}
	}
	return nil
}

func uploadSafeName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, name)
	if name == "" || name == "." || name == ".." {
		return "attachment"
	}
	if name == ".attachment.json" {
		return "attachment.json"
	}
	if len(name) > 180 {
		name = name[len(name)-180:]
		for len(name) > 0 && !utf8.RuneStart(name[0]) {
			name = name[1:]
		}
	}
	return name
}

func (w *Workspace) StoreUpload(botID, name, mimeType string, data []byte) (Attachment, error) {
	if len(data) == 0 {
		return Attachment{}, errors.New("upload is empty")
	}
	return w.storeUpload(botID, name, mimeType, data)
}

func (w *Workspace) storeUpload(botID, name, mimeType string, data []byte) (Attachment, error) {
	if len(data) > maxUploadBytes {
		return Attachment{}, errors.New("upload exceeds 25 MiB")
	}
	dir, err := w.store.BotDir(botID)
	if err != nil {
		return Attachment{}, err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return Attachment{}, err
	}
	id := hex.EncodeToString(nonce[:])
	name = uploadSafeName(name)
	if mimeType == "" {
		mimeType = mime.TypeByExtension(filepath.Ext(name))
	}
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}
	if parsed, _, err := mime.ParseMediaType(mimeType); err == nil {
		mimeType = parsed
	} else {
		mimeType = "application/octet-stream"
	}
	root := filepath.Join(dir, "uploads", id)
	path := filepath.Join(root, name)
	attachment := Attachment{ID: id, Name: name, MimeType: mimeType, Path: path, URL: "/api/studio/bots/" + botID + "/files/" + id}
	metadata, err := json.Marshal(attachment)
	if err != nil {
		return Attachment{}, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := writeWorkspaceAtomic(w.store.Root(), path, data); err != nil {
		return Attachment{}, err
	}
	if err := writeWorkspaceAtomic(w.store.Root(), filepath.Join(root, ".attachment.json"), metadata); err != nil {
		return Attachment{}, err
	}
	public := attachment
	public.Path = ""
	return public, nil
}

// PublishFiles snapshots artifacts from this bot's own workspace. Published
// copies are durable uploads with opaque URLs, so deleting the temporary source
// never breaks an attachment already sent to a conversation.
func (w *Workspace) PublishFiles(botID string, paths []string) ([]Attachment, error) {
	if len(paths) == 0 || len(paths) > 10 {
		return nil, errors.New("publish 1 to 10 files at a time")
	}
	dir, err := w.store.BotDir(botID)
	if err != nil {
		return nil, err
	}
	if err := noSymlinkPath(w.store.Root(), dir); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	type pendingArtifact struct {
		name, mime string
		data       []byte
	}
	pending := make([]pendingArtifact, 0, len(paths))
	for _, input := range paths {
		if input == "" {
			return nil, errors.New("artifact path is required")
		}
		path := input
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		path = filepath.Clean(path)
		if !pathWithin(dir, path) {
			return nil, errors.New("artifacts must belong to this bot's workspace")
		}
		canonical, err := filepath.EvalSymlinks(path)
		if err != nil {
			return nil, fmt.Errorf("resolve artifact: %w", err)
		}
		if !pathWithin(dir, canonical) {
			return nil, errors.New("artifact symbolic link leaves this bot's workspace")
		}
		rel, err := filepath.Rel(dir, canonical)
		if err != nil {
			return nil, err
		}
		info, err := root.Lstat(rel)
		if err != nil {
			return nil, fmt.Errorf("read artifact metadata: %w", err)
		}
		if !info.Mode().IsRegular() {
			return nil, errors.New("only regular files can be published")
		}
		if info.Size() > maxUploadBytes {
			return nil, errors.New("artifact exceeds 25 MiB")
		}
		file, err := root.Open(rel)
		if err != nil {
			return nil, fmt.Errorf("open artifact: %w", err)
		}
		opened, statErr := file.Stat()
		if statErr != nil || !opened.Mode().IsRegular() {
			file.Close()
			return nil, errors.New("artifact is no longer a regular file")
		}
		data, readErr := io.ReadAll(io.LimitReader(file, maxUploadBytes+1))
		closeErr := file.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read artifact: %w", readErr)
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if len(data) > maxUploadBytes {
			return nil, errors.New("artifact exceeds 25 MiB")
		}
		pending = append(pending, pendingArtifact{filepath.Base(path), artifactMIME(path, data), data})
	}
	attachments := make([]Attachment, 0, len(pending))
	for _, artifact := range pending {
		attachment, err := w.storeUpload(botID, artifact.name, artifact.mime, artifact.data)
		if err != nil {
			// A failed batch has not been journaled. Remove only newly created,
			// unreferenced copies; original workspace artifacts remain untouched.
			for _, created := range attachments {
				if removeErr := root.RemoveAll(filepath.Join("uploads", created.ID)); removeErr != nil {
					return nil, fmt.Errorf("publish artifact: %w; rollback: %v", err, removeErr)
				}
			}
			return nil, err
		}
		attachments = append(attachments, attachment)
	}
	return attachments, nil
}

func artifactMIME(path string, data []byte) string {
	contentType := http.DetectContentType(data)
	extensionType := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
	// Extensions distinguish text formats and ZIP-based documents; image,
	// PDF, and media signatures take precedence over a misleading filename.
	if extensionType != "" && (strings.HasPrefix(contentType, "text/plain") || contentType == "application/octet-stream" || contentType == "application/zip") {
		return extensionType
	}
	return contentType
}

func (w *Workspace) ResolveAttachments(botID string, attachments []Attachment) ([]Attachment, error) {
	dir, err := w.store.BotDir(botID)
	if err != nil {
		return nil, err
	}
	resolved := make([]Attachment, 0, len(attachments))
	for _, supplied := range attachments {
		if !attachmentIDPattern.MatchString(supplied.ID) {
			return nil, errors.New("unknown attachment ID")
		}
		root := filepath.Join(dir, "uploads", supplied.ID)
		metadataPath := filepath.Join(root, ".attachment.json")
		if err := noSymlinkPath(w.store.Root(), metadataPath); err != nil {
			return nil, err
		}
		content, err := readWorkspaceFile(metadataPath)
		if err != nil {
			return nil, err
		}
		var attachment Attachment
		if json.Unmarshal([]byte(content), &attachment) != nil || attachment.ID != supplied.ID {
			return nil, errors.New("unknown attachment ID")
		}
		// Do not trust stored paths either; rebuild from the opaque ID and name.
		if attachment.Name != uploadSafeName(attachment.Name) {
			return nil, errors.New("invalid stored attachment")
		}
		attachment.Path = filepath.Join(root, attachment.Name)
		attachment.URL = "/api/studio/bots/" + botID + "/files/" + attachment.ID
		if err := noSymlinkPath(w.store.Root(), attachment.Path); err != nil {
			return nil, err
		}
		info, err := os.Stat(attachment.Path)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || info.Size() > maxUploadBytes {
			return nil, errors.New("invalid stored attachment")
		}
		resolved = append(resolved, attachment)
	}
	return resolved, nil
}
