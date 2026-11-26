import { savedPromptText } from './bridge.ts';
import assert from 'node:assert/strict';
const note = process.env.PI_IMAGE_NOTE;
assert.equal(savedPromptText([{ type: 'text', text: '\n\n' + note }, { type: 'image' }]), '');
assert.equal(savedPromptText([{ type: 'text', text: 'look at this\n\n' + note }, { type: 'image' }]), 'look at this');
assert.equal(savedPromptText([{ type: 'text', text: 'two\n\n' + note + '\n\n' + note }]), 'two');
assert.equal(savedPromptText('plain text'), 'plain text');
assert.equal(savedPromptText([{ type: 'text', text: 'an [Image: reference] I typed' }]), 'an [Image: reference] I typed');
