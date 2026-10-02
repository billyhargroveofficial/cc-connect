import type { Skill, SkillReference } from "../../lib/types";

export interface SkillMention { start: number; end: number; query: string }
const nameCharacter = /[\p{L}\p{N}_.:-]/u;

function escaped(text: string, at: number) {
  let slashes = 0;
  while (at > 0 && text[--at] === "\\") slashes++;
  return slashes % 2 === 1;
}

function inCode(text: string, at: number) {
  let fence = "", fenceLength = 0, inlineLength = 0;
  for (let index = 0; index < at;) {
    if (index === 0 || text[index - 1] === "\n") {
      const match = /^ {0,3}(`{3,}|~{3,})[^\n]*/.exec(text.slice(index));
      if (match) {
        const marker = match[1];
        if (!fence) { fence = marker[0]; fenceLength = marker.length; inlineLength = 0; }
        else if (marker[0] === fence && marker.length >= fenceLength && /^\s*$/.test(match[0].slice(match[0].indexOf(marker) + marker.length))) fence = "";
        if (index + match[0].length >= at) return true;
        index += match[0].length;
        continue;
      }
    }
    if (!fence && text[index] === "`" && !escaped(text, index)) {
      let end = index + 1;
      while (text[end] === "`") end++;
      const length = end - index;
      if (!inlineLength) inlineLength = length;
      else if (inlineLength === length) inlineLength = 0;
      index = end;
    } else index++;
  }
  return Boolean(fence || inlineLength);
}

function inMath(text: string, start: number, caret: number) {
  const pattern = /\$\$[\s\S]*?(?:\$\$|$)|\\\([\s\S]*?(?:\\\)|$)|\\\[[\s\S]*?(?:\\\]|$)|\$(?!\$)([^\s$](?:[^$\n]*?[^\s$])?)\$(?!\d)/g;
  for (const match of text.matchAll(pattern)) {
    const index = match.index;
    if (!escaped(text, index) && start >= index && caret <= index + match[0].length) return true;
  }
  return false;
}

export function skillMentionAt(text: string, caret: number, selectionEnd = caret): SkillMention | null {
  if (caret !== selectionEnd || caret < 0 || caret > text.length) return null;
  const prefix = text.slice(0, caret);
  const match = /(?:^|[\s([{])\$([\p{L}\p{N}_.:-]*)$/u.exec(prefix);
  if (!match || /^-?\d/.test(match[1])) return null;
  const start = caret - match[1].length - 1;
  if (escaped(text, start) || inCode(text, start) || inMath(text, start, caret)) return null;
  let end = caret;
  while (end < text.length && nameCharacter.test(text[end])) end++;
  return { start, end, query: match[1] };
}

export function removeSkillMention(text: string, mention: SkillMention) {
  const before = text.slice(0, mention.start);
  let after = text.slice(mention.end);
  if ((!before || before.endsWith(" ")) && after.startsWith(" ")) after = after.slice(1);
  return { text: before + after, caret: before.length };
}

export function filterSkills(skills: Skill[], query: string, selected: SkillReference[]) {
  const needle = query.toLocaleLowerCase();
  const ids = new Set(selected.map(skill => skill.id));
  return skills.filter(skill => skill.enabled && !ids.has(skill.id)
    && `${skill.name} ${skill.description}`.toLocaleLowerCase().includes(needle))
    .sort((left, right) => Number(!left.name.toLocaleLowerCase().startsWith(needle)) - Number(!right.name.toLocaleLowerCase().startsWith(needle))
      || left.name.localeCompare(right.name));
}

export function skillMentionText(text: string, skills: SkillReference[]) {
  if (!skills.length) return text;
  return `${skills.map(skill => `$${skill.name}`).join(" ")}${text ? `\n${text}` : ""}`;
}
