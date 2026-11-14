// The tests' model of the runner's command parsing (runner-commands.mjs) catches what the runner would run.
import assert from 'node:assert/strict';
import { test } from 'node:test';

import { commands } from './runner-commands.mjs';

const names = (out) => commands(out).map((c) => c.name);

test('both forms, after a lone CR or leading spaces, are commands', () => {
  assert.deepEqual(names('::warning::a\n\t::error::b\rx ##[add-mask]c\r\n::not-a-command::d\n##[group]e'),
    ['warning', 'error', 'add-mask', 'group']);
});

test('nothing runs while commands are stopped, until the token resumes them', () => {
  assert.deepEqual(names('::stop-commands::tok\n::warning::a\nx ##[add-mask]b\n::tok::\n::warning::c'),
    ['stop-commands', 'resume', 'warning']);
  assert.deepEqual(names('x ##[stop-commands]zz\n::warning::swallowed'), ['stop-commands']);
});

test("leading whitespace is .NET's TrimStart: U+0085 goes, U+FEFF stays", () => {
  assert.deepEqual(names('\u0085::warning::a'), ['warning']);
  assert.deepEqual(names('\u0085  ::warning::a'), ['warning']);
  assert.deepEqual(names('﻿::warning::a'), []);
});
