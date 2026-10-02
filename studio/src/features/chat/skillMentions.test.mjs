import assert from 'node:assert/strict';
import test from 'node:test';
import { filterSkills, removeSkillMention, skillMentionAt, skillMentionText } from './skillMentions.ts';

test('dollar mentions follow the caret and preserve the surrounding draft on selection', () => {
  for (const text of ['$', 'Find $re', 'Use ($plugin:review', 'Try\n$проверка']) {
    assert.ok(skillMentionAt(text, text.length), text);
  }
  const text = 'Please $review the files.';
  const caret = text.indexOf('review') + 2;
  const mention = skillMentionAt(text, caret);
  assert.deepEqual(mention, { start: 7, end: 14, query: 're' });
  assert.deepEqual(removeSkillMention(text, mention), { text: 'Please the files.', caret: 7 });
  const start = '$review Keep this suffix';
  assert.deepEqual(removeSkillMention(start, skillMentionAt(start, 7)), { text: 'Keep this suffix', caret: 0 });
  assert.equal(skillMentionAt(text, caret, caret + 1), null, 'selected text is not an autocomplete trigger');
});

test('currency, escaped dollars, prose identifiers, code and math never open skill suggestions', () => {
  for (const text of ['Cost $200', 'Loss $-200', 'Cost $20.99', 'email$review', String.raw`escaped \$review`,
    '`$review', '``$review``', '```sh\n$review', '~~~text\n$review', String.raw`\($review`, String.raw`\[$review`, '$$x + $review']) {
    assert.equal(skillMentionAt(text, text.includes('``$review``') ? text.length - 2 : text.length), null, text);
  }
  const math = 'Inline $review$ formula';
  assert.equal(skillMentionAt(math, math.indexOf('review') + 6), null, 'matched single-dollar math is not a skill');
  for (const text of ['`literal` $review', '```sh\nliteral\n```\n$review', '$x+1$ $review', 'Use $research and $review']) {
    assert.ok(skillMentionAt(text, text.length), text);
  }
});

test('skill filtering excludes disabled and attached IDs while retaining distinct same-name scopes', () => {
  const base = { path: '/skills/SKILL.md', enabled: true, editable: true, scope: 'project' };
  const skills = [
    { ...base, id: 'description', name: 'writer', description: 'Review source documents' },
    { ...base, id: 'shared', name: 'review', scope: 'productUser', description: 'Shared review' },
    { ...base, id: 'project', name: 'review', description: 'Bot review' },
    { ...base, id: 'disabled', name: 'review-disabled', enabled: false, description: 'Disabled' },
  ];
  assert.deepEqual(filterSkills(skills, 'REV', [{ id: 'shared' }]).map(skill => skill.id), ['project', 'description']);
  assert.deepEqual(filterSkills(skills, '', []).map(skill => skill.id), ['shared', 'project', 'description']);
  assert.equal(skills[0].id, 'description', 'sorting never mutates the cached catalog');
});

test('legacy skill fallback changes only the outgoing text and accepts skill-only messages', () => {
  const skills = [{ id: 'one', name: 'review', path: '/private/review/SKILL.md' }, { id: 'two', name: 'writer', path: '/private/writer/SKILL.md' }];
  assert.equal(skillMentionText('Check this.', skills), '$review $writer\nCheck this.');
  assert.equal(skillMentionText('', skills), '$review $writer');
  assert.equal(skillMentionText('Original', []), 'Original');
});
