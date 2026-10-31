import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

// Minimal wiring test for the chat message feedback buttons: the component
// must post to the message feedback endpoint through the shared request
// helper, submit useful ratings directly, and gate useless ratings behind a
// multi-select reason panel (fact_error / outdated / incomplete / off_topic /
// other) with an optional comment.

const componentSource = readFileSync(new URL('./ChatMessageFeedback.vue', import.meta.url), 'utf8')
const apiSource = readFileSync(new URL('../api/chat-history.ts', import.meta.url), 'utf8')

test('feedback buttons submit through the shared api module and the messages feedback endpoint', () => {
  assert.match(componentSource, /submitMessageFeedback\(/)
  assert.match(componentSource, /from '@\/api\/chat-history'/)
  assert.match(apiSource, /post\(`\/api\/v1\/messages\/\$\{encodeURIComponent\(messageId\)\}\/feedback`/)
})

test('useful submits immediately with only the rating; useless sends reason codes and comment when present', () => {
  assert.match(componentSource, /rating: 'useful'/)
  assert.match(componentSource, /rating: 'useless'/)
  // reason codes only when at least one is picked, comment only when non-empty
  assert.match(componentSource, /reasons\.length \? \{ reason_codes: reasons \} : \{\}/)
  assert.match(componentSource, /trimmed \? \{ comment: trimmed \} : \{\}/)
})

test('useless opens a multi-select reason panel with the five canonical codes', () => {
  assert.match(componentSource, /t-popup/)
  for (const code of ['fact_error', 'outdated', 'incomplete', 'off_topic', 'other']) {
    assert.ok(componentSource.includes(`'${code}'`), `missing reason code ${code}`)
  }
  assert.match(componentSource, /t-checkbox/)
  assert.match(componentSource, /t-textarea/)
})

test('submitted state is echoed locally via the is-active class', () => {
  assert.match(componentSource, /submitted === 'useful'/)
  assert.match(componentSource, /submitted === 'useless'/)
  assert.match(componentSource, /is-active/)
})

test('component renders nothing without a message id and exposes both button titles', () => {
  assert.match(componentSource, /v-if="messageId"/)
  assert.match(componentSource, /t\('chat\.feedback\.useful'\)/)
  assert.match(componentSource, /t\('chat\.feedback\.useless'\)/)
})
