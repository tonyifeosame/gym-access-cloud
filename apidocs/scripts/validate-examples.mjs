#!/usr/bin/env node
/*
 * Validate the examples.
 *
 * Two kinds. First, every `example` / `examples` object in the specification
 * -- request bodies, responses, parameters, schemas -- must validate against
 * the schema it sits under (Ajv, JSON Schema 2020-12, which is what OpenAPI
 * 3.1 uses). Second, the guide (info.description) carries copy-and-paste curl
 * requests and the responses they produce; those fences are TAGGED so they
 * can be checked rather than trusted:
 *
 *   ```bash {op=createMember}            a curl request for that operation
 *   ```json {op=createMember status=201} the response body for that status
 *
 * A tagged curl must name the real production origin, use the operation's
 * method at the operation's path, carry `Authorization: Bearer
 * <ACCESSLINK_API_KEY>` (public routes) or no credential-shaped value at all,
 * send only documented query parameters, and -- when it has a body -- send
 * JSON that validates against the operation's request schema. A tagged JSON
 * response must validate against the operation's response schema for that
 * status. openapi_docs_test.go then replays the same tagged curls through the
 * real router, so an example here is one that actually runs.
 *
 * Exported for the Go side by convention only: the tag grammar is the small
 * contract both share.
 */
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import SwaggerParser from '@apidevtools/swagger-parser'
import Ajv2020 from 'ajv/dist/2020.js'
import addFormats from 'ajv-formats'
import YAML from 'yaml'

const SOURCE = resolve(import.meta.dirname, '..', 'openapi.yaml')
const raw = YAML.parse(readFileSync(SOURCE, 'utf8'))
const doc = await SwaggerParser.dereference(structuredClone(raw))
const failures = []
const fail = (msg) => failures.push(msg)

const ajv = new Ajv2020({ strict: false, allErrors: true, allowUnionTypes: true })
addFormats(ajv)

function validateAgainst(schema, value, where) {
  if (!schema) return fail(`${where}: no schema to validate against`)
  let check
  try {
    check = ajv.compile(schema)
  } catch (error) {
    return fail(`${where}: schema does not compile: ${error.message}`)
  }
  if (!check(value)) {
    const detail = (check.errors ?? []).map((e) => `${e.instancePath || '/'} ${e.message}`).join('; ')
    fail(`${where}: example does not match its schema: ${detail}`)
  }
}

const METHODS = ['get', 'post', 'put', 'patch', 'delete']
let specExamples = 0

// --- 1. examples embedded in the specification ------------------------------
function checkMediaType(media, where) {
  if (!media) return
  if (media.example !== undefined) {
    specExamples++
    validateAgainst(media.schema, media.example, where)
  }
  for (const [name, ex] of Object.entries(media.examples ?? {})) {
    specExamples++
    validateAgainst(media.schema, ex.value, `${where} examples.${name}`)
  }
}
for (const [name, schema] of Object.entries(doc.components?.schemas ?? {})) {
  if (schema.example !== undefined) {
    specExamples++
    validateAgainst(schema, schema.example, `components.schemas.${name}.example`)
  }
}
for (const [name, param] of Object.entries(doc.components?.parameters ?? {})) {
  if (param.example !== undefined) {
    specExamples++
    validateAgainst(param.schema, param.example, `components.parameters.${name}.example`)
  }
}
for (const [name, response] of Object.entries(doc.components?.responses ?? {})) {
  checkMediaType(response.content?.['application/json'], `components.responses.${name}`)
}
for (const [path, item] of Object.entries(doc.paths)) {
  for (const method of METHODS) {
    const op = item[method]
    if (!op) continue
    const where = `${method.toUpperCase()} ${path}`
    checkMediaType(op.requestBody?.content?.['application/json'], `${where} requestBody`)
    for (const [status, response] of Object.entries(op.responses ?? {})) {
      checkMediaType(response.content?.['application/json'], `${where} ${status}`)
    }
    for (const p of op.parameters ?? []) {
      if (p.example !== undefined) {
        specExamples++
        validateAgainst(p.schema, p.example, `${where} parameter ${p.name}.example`)
      }
    }
  }
}

// --- 2. the guide's tagged fences -------------------------------------------
const byOperationId = new Map()
for (const [path, item] of Object.entries(doc.paths)) {
  for (const method of METHODS) {
    if (item[method]) byOperationId.set(item[method].operationId, { path, method, op: item[method], item })
  }
}

export function parseTag(info) {
  // "bash {op=createMember}" / "json {op=createMember status=201}"
  const m = info.match(/^(\w+)\s*\{([^}]*)\}\s*$/)
  if (!m) return null
  const attrs = Object.fromEntries(m[2].trim().split(/\s+/).filter(Boolean).map((kv) => kv.split('=')))
  return { lang: m[1], ...attrs }
}

export function extractFences(markdown) {
  const out = []
  const re = /^([ \t]*)```([^\n]*)\n([\s\S]*?)^\1```[ \t]*$/gm
  let m
  while ((m = re.exec(markdown))) {
    const indent = m[1]
    const body = m[3].split('\n').map((l) => (l.startsWith(indent) ? l.slice(indent.length) : l)).join('\n')
    out.push({ info: m[2].trim(), body: body.replace(/\n$/, '') })
  }
  return out
}

/** A minimal curl parser for the shapes the guide uses. */
export function parseCurl(text) {
  const joined = text.replace(/\\\r?\n\s*/g, ' ').trim()
  if (!joined.startsWith('curl ')) return null
  const tokens = []
  const re = /"((?:[^"\\]|\\.)*)"|'((?:[^'\\]|\\.)*)'|(\S+)/g
  let m
  while ((m = re.exec(joined.slice(5)))) tokens.push(m[1] ?? m[2] ?? m[3])
  const req = { method: 'GET', url: null, headers: {}, body: null }
  for (let i = 0; i < tokens.length; i++) {
    const t = tokens[i]
    if (t === '-X' || t === '--request') req.method = tokens[++i].toUpperCase()
    else if (t === '-H' || t === '--header') {
      const [name, ...rest] = tokens[++i].split(':')
      req.headers[name.trim().toLowerCase()] = rest.join(':').trim()
    } else if (t === '-d' || t === '--data' || t === '--data-raw' || t === '--data-binary') {
      req.body = tokens[++i]
      if (req.method === 'GET') req.method = 'POST'
    } else if (t.startsWith('-')) {
      fail(`curl option ${t} is not one the guide's examples may use`)
    } else if (!req.url) req.url = t
  }
  return req
}

const ORIGIN = 'https://api.accesslink.store'
const KEY_PLACEHOLDER = '<ACCESSLINK_API_KEY>'

function pathMatches(template, actual) {
  const a = template.split('/'), b = actual.split('/')
  if (a.length !== b.length) return false
  return a.every((seg, i) => (seg.startsWith('{') && seg.endsWith('}') ? b[i].length > 0 : seg === b[i]))
}

const fences = extractFences(doc.info.description ?? '')
let curls = 0, responses = 0
const seenOps = new Set()
for (const fence of fences) {
  const tag = parseTag(fence.info)
  if (!tag) continue
  if (!tag.op) { fail(`tagged fence "${fence.info}" names no op`); continue }
  const target = byOperationId.get(tag.op)
  if (!target) { fail(`fence "${fence.info}": no operation ${tag.op}`); continue }
  const where = `guide example for ${tag.op}`

  if (tag.lang === 'bash') {
    curls++
    seenOps.add(tag.op)
    const req = parseCurl(fence.body)
    if (!req || !req.url) { fail(`${where}: not a curl request`); continue }
    if (req.method !== target.method.toUpperCase()) fail(`${where}: method ${req.method}, operation is ${target.method.toUpperCase()}`)
    let url
    try { url = new URL(req.url) } catch { fail(`${where}: bad URL ${req.url}`); continue }
    if (url.origin !== ORIGIN) fail(`${where}: origin ${url.origin} is not ${ORIGIN}`)
    if (!pathMatches(target.path, url.pathname)) fail(`${where}: path ${url.pathname} does not match ${target.path}`)
    const documented = new Set((target.op.parameters ?? []).concat(target.item.parameters ?? []).filter((p) => p.in === 'query').map((p) => p.name))
    for (const q of url.searchParams.keys()) if (!documented.has(q)) fail(`${where}: query parameter ${q} is not documented`)
    // The authorization server's routes are how a caller obtains a credential,
    // so an example for one carries no API key -- and must not, or the example
    // would be teaching the wrong thing. Everything else on the public tree
    // carries the placeholder, never a real key.
    if (target.path.startsWith('/api/public/') && !target.path.startsWith('/api/public/v1/oauth/')) {
      if (req.headers.authorization !== `Bearer ${KEY_PLACEHOLDER}`) fail(`${where}: Authorization must be "Bearer ${KEY_PLACEHOLDER}"`)
    }
    for (const value of Object.values(req.headers)) {
      if (/atp_(live|test)_[0-9a-f]{8}/.test(value)) fail(`${where}: a credential-shaped value in a header`)
    }
    const bodySchema = target.op.requestBody?.content?.['application/json']?.schema
    if (req.body !== null) {
      if ((req.headers['content-type'] ?? '') !== 'application/json') fail(`${where}: a body needs Content-Type: application/json`)
      let parsed
      try { parsed = JSON.parse(req.body) } catch { fail(`${where}: body is not JSON`); continue }
      validateAgainst(bodySchema, parsed, `${where} request body`)
    } else if (target.op.requestBody?.required) {
      fail(`${where}: operation requires a body and the example sends none`)
    }
    if (req.headers['idempotency-key'] !== undefined) {
      const declared = (target.op.parameters ?? []).some((p) => p.in === 'header' && p.name.toLowerCase() === 'idempotency-key')
      if (!declared) fail(`${where}: sends Idempotency-Key but the operation does not document it`)
    }
  } else if (tag.lang === 'json') {
    responses++
    const status = tag.status
    if (!status) { fail(`${where}: response fence names no status`); continue }
    const response = target.op.responses?.[status]
    if (!response) { fail(`${where}: operation documents no ${status} response`); continue }
    let parsed
    try { parsed = JSON.parse(fence.body) } catch { fail(`${where} ${status}: response is not JSON`); continue }
    validateAgainst(response.content?.['application/json']?.schema, parsed, `${where} ${status} response`)
    if (parsed?.error?.doc_url && !String(parsed.error.doc_url).startsWith('https://accesslink.store/docs/errors/')) {
      fail(`${where} ${status}: doc_url must be under https://accesslink.store/docs/errors/`)
    }
  } else {
    fail(`fence "${fence.info}": tagged fences are bash (request) or json (response)`)
  }
}
// Every core public operation the guide promises a working example for.
for (const op of ['listMembers', 'createMember', 'getMemberAccess', 'listEvents']) {
  if (!seenOps.has(op)) fail(`the guide has no tagged curl example for ${op}`)
}

if (failures.length > 0) {
  console.error(`examples: ${failures.length} problem(s)`)
  for (const f of failures) console.error(`  - ${f}`)
  process.exit(1)
}
console.log(`examples are valid: ${specExamples} in the specification, ${curls} guide requests and ${responses} guide responses checked against their schemas`)
