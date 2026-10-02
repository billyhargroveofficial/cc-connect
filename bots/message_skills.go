package bots

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

const maxMessageSkills = 16

// ResolveSkills uses the selected bot's current catalog, never the combined
// account editor. This keeps references scoped to the tenant, node and harness
// that own the message. Queues call it again before dispatch or steering.
func (w *Workspace) ResolveSkills(botID string, supplied []SkillAttachment) ([]SkillAttachment, error) {
	if len(supplied) > maxMessageSkills {
		return nil, fmt.Errorf("%w: too many attached skills", ErrInvalid)
	}
	if len(supplied) == 0 {
		return nil, nil
	}
	catalog, err := w.BotSkills(botID)
	if err != nil {
		return nil, err
	}
	known := make(map[string]Skill, len(catalog))
	for _, skill := range catalog {
		known[skill.ID] = skill
	}
	resolved := make([]SkillAttachment, 0, len(supplied))
	seen := make(map[string]bool, len(supplied))
	for _, reference := range supplied {
		id := reference.ID
		if id == "" {
			id = reference.Path
		}
		if id == "" || len(id) > 4096 || !filepath.IsAbs(id) {
			return nil, fmt.Errorf("%w: attached skill must identify an installed workflow", ErrInvalid)
		}
		skill, ok := known[id]
		if !ok {
			// A managed product link and the catalog's canonical SKILL.md are
			// equivalent references. Resolution alone never grants access.
			canonical, err := filepath.EvalSymlinks(id)
			if err == nil {
				skill, ok = known[canonical]
			}
		}
		if !ok {
			return nil, fmt.Errorf("%w: attached skill is not available for this bot", ErrInvalid)
		}
		if !skill.Enabled {
			return nil, fmt.Errorf("%w: attached skill is disabled for this bot", ErrInvalid)
		}
		if !seen[skill.ID] {
			seen[skill.ID] = true
			resolved = append(resolved, SkillAttachment{ID: skill.ID, Name: skill.Name, Path: skill.Path})
		}
	}
	return resolved, nil
}

func (r *Runtime) resolveMessageSkills(botID string, request MessageRequest) (MessageRequest, error) {
	if len(request.Skills) == 0 {
		return request, nil
	}
	if len(request.Skills) > maxMessageSkills {
		return MessageRequest{}, fmt.Errorf("%w: too many attached skills", ErrInvalid)
	}
	if r.cfg.ResolveSkills == nil {
		return MessageRequest{}, fmt.Errorf("%w: attached skills are not supported by this node", ErrInvalid)
	}
	skills, err := r.cfg.ResolveSkills(botID, request.Skills)
	if err != nil {
		return MessageRequest{}, err
	}
	request.Skills = cloneSkillAttachments(skills)
	return request, nil
}

func cloneSkillAttachments(skills []SkillAttachment) []SkillAttachment {
	return append([]SkillAttachment{}, skills...)
}

func runtimeSkills(skills []SkillAttachment) []core.SkillAttachment {
	result := make([]core.SkillAttachment, 0, len(skills))
	for _, skill := range skills {
		result = append(result, core.SkillAttachment{Name: skill.Name, Path: skill.Path})
	}
	return result
}

// Pi RPC has no structured skill input. Its /skill:name expansion supports one
// name and can select the wrong file when installed names collide. An explicit
// reference to each validated file preserves the selected workflow's identity.
func promptWithSkillRefs(prompt string, skills []SkillAttachment) string {
	if len(skills) == 0 {
		return prompt
	}
	var result strings.Builder
	result.WriteString("The owner explicitly attached these installed skills to this message. Read each SKILL.md and follow its workflow where applicable; resolve its references relative to its folder:\n")
	for _, skill := range skills {
		fmt.Fprintf(&result, "- Skill %q: SKILL.md at %q.\n", "$"+skill.Name, skill.Path)
	}
	result.WriteString("\nOwner message:\n")
	result.WriteString(prompt)
	return result.String()
}
