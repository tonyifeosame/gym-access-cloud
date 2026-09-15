#!/usr/bin/env node
/*
 * Validate apidocs/openapi.yaml.
 *
 * Two layers. First the document must be valid OpenAPI 3.1 with every $ref
 * resolvable (swagger-parser). Then the house rules, which are what keep the
 * site honest rather than merely well-formed:
 *
 *   - every operation has a tag from the declared list, a summary, an
 *     operationId, a description, and at least one response with a schema;
 *   - every operation declares its security explicitly (public routes the
 *     integration credential and its x-scope; console routes the operator
 *     session; login alone is open), and public routes document 401 and 429;
 *   - every tag belongs to exactly one tag group, so nothing can appear in the
 *     sidebar outside "Public API" or "Console API";
 *   - the error-code list is complete (every code in the guide's tables and
 *     examples is registered) and every doc_url points at this site;
 *   - the document mentions nothing that must never be published: device
 *     credentials, site provisioning keys, biometric templates as fields, or
 *     a claim that fingerprints replicate between terminals.
 *
 * Route existence is not checked here -- that is openapi_docs_test.go in the
 * Go module, which has the router itself to compare against.
 */
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import SwaggerParser from '@apidevtools/swagger-parser'
import YAML from 'yaml'

const SOURCE = resolve(import.meta.dirname, '..', 'openapi.yaml')
const text = readFileSync(SOURCE, 'utf8')
const doc = YAML.parse(text)
const failures = []
const fail = (msg) => failures.push(msg)

// --- 1. structural validity -------------------------------------------------
try {
  await SwaggerParser.validate(structuredClone(doc))
} catch (error) {
  fail(`OpenAPI validation: ${error.message}`)
}

// --- 2. house rules ----------------------------------------------------------
const METHODS = ['get', 'post', 'put', 'patch', 'delete']
const declaredTags = new Set((doc.tags ?? []).map((t) => t.name))
const groups = doc['x-tagGroups'] ?? []
const groupedTags = new Map()
for (const g of groups) for (const t of g.tags) groupedTags.set(t, (groupedTags.get(t) ?? 0) + 1)
for (const t of declaredTags) {
  if ((groupedTags.get(t) ?? 0) !== 1) fail(`tag "${t}" must belong to exactly one x-tagGroup`)
}
for (const [t] of groupedTags) if (!declaredTags.has(t)) fail(`x-tagGroups names undeclared tag "${t}"`)

const consoleTags = new Set(groups.find((g) => g.name.startsWith('Console'))?.tags ?? [])
const publicTags = new Set(groups.find((g) => g.name === 'Public API')?.tags ?? [])
const scopes = new Set(['members:read', 'members:write', 'access:read', 'sites:read', 'events:read'])
const operationIds = new Set()
let operations = 0

for (const [path, item] of Object.entries(doc.paths ?? {})) {
  for (const method of METHODS) {
    const op = item[method]
    if (!op) continue
    operations += 1
    const where = `${method.toUpperCase()} ${path}`
    if (!op.operationId) fail(`${where}: operationId missing`)
    else if (operationIds.has(op.operationId)) fail(`${where}: duplicate operationId ${op.operationId}`)
    else operationIds.add(op.operationId)
    if (!op.summary) fail(`${where}: summary missing`)
    if (!op.description) fail(`${where}: description missing`)
    if (!Array.isArray(op.tags) || op.tags.length !== 1) fail(`${where}: exactly one tag required`)
    const tag = op.tags?.[0]
    if (tag && !declaredTags.has(tag)) fail(`${where}: undeclared tag "${tag}"`)
    if (!op.responses || Object.keys(op.responses).length === 0) fail(`${where}: no responses`)
    const success = Object.keys(op.responses ?? {}).find((s) => s.startsWith('2'))
    if (!success) fail(`${where}: no 2xx response`)
    if (!Array.isArray(op.security)) fail(`${where}: security must be declared explicitly (use [] for an open route)`)

    const isPublic = path.startsWith('/api/public/')
    if (isPublic) {
      if (!publicTags.has(tag)) fail(`${where}: public route must use a Public API tag`)
      const schemes = (op.security ?? []).flatMap((s) => Object.keys(s))
      if (!schemes.includes('IntegrationCredential')) fail(`${where}: public route must require IntegrationCredential`)
      if (!scopes.has(op['x-scope'])) fail(`${where}: x-scope must be one served scope (got ${op['x-scope']})`)
      for (const status of ['401', '403', '429', '503']) {
        if (!op.responses?.[status]) fail(`${where}: public route must document ${status}`)
      }
      if (!op.description.includes(op['x-scope'])) fail(`${where}: description must name its scope ${op['x-scope']}`)
    } else {
      if (!consoleTags.has(tag)) fail(`${where}: non-public route must use a Console API tag`)
      const open = path === '/api/v1/auth/login'
      const schemes = (op.security ?? []).flatMap((s) => Object.keys(s))
      if (!open && !schemes.includes('OperatorSession')) fail(`${where}: console route must require OperatorSession`)
      if (open && schemes.length !== 0) fail(`${where}: login must be an open route`)
      if (!open && method !== 'get') {
        const csrf = (op.parameters ?? []).some((p) => p.name === 'X-CSRF-Token' && p.in === 'header' && p.required)
        if (!csrf) fail(`${where}: console write must require the X-CSRF-Token header`)
      }
    }
  }
}

// --- 3. error codes ----------------------------------------------------------
const codes = doc['x-accesslink-error-codes'] ?? []
const codeSet = new Set(codes.map((c) => c.code))
if (codeSet.size !== codes.length) fail('x-accesslink-error-codes has a duplicate')
for (const c of codes) {
  if (!/^[a-z_]+$/.test(c.code)) fail(`error code "${c.code}" is not snake_case`)
  if (!Number.isInteger(c.status)) fail(`error code ${c.code} has no status`)
  if (!c.type || !c.message) fail(`error code ${c.code} needs type and message`)
}
// Every code the guide or an example names must be registered, and every
// doc_url in the document must be this site's own error page for that code.
for (const m of text.matchAll(/accesslink\.store\/docs\/errors\/([a-z_]+)/g)) {
  if (!codeSet.has(m[1])) fail(`doc_url names unregistered error code "${m[1]}"`)
}
for (const m of text.matchAll(/code: ([a-z_]+)$/gm)) {
  if (!codeSet.has(m[1])) fail(`example uses unregistered error code "${m[1]}"`)
}
const errorEnum = doc.components?.schemas?.Error?.properties?.error?.properties?.type?.enum ?? []
for (const c of codes) if (!errorEnum.includes(c.type)) fail(`error type "${c.type}" (from ${c.code}) is not in the Error schema enum`)

// --- 4. nothing that must not be published ---------------------------------
const forbidden = [
  [/atd_[0-9a-f]{8}/, 'a device credential'],
  [/ats_[0-9a-f]{8}/, 'a site provisioning key'],
  [/atp_(live|test)_[0-9a-f]{16}/, 'an integration credential value'],
  [/fingerprint_template:/, 'a biometric field'],
  [/\breplicat(e|es|ed|ion)\b/i, 'a replication claim'],
  [/-----BEGIN/, 'key material'],
  [/localhost:8080/, 'a development server URL'],
  [/docs\.accesslink\.store|onrender\.com/, 'a hosting hostname (the site is https://accesslink.store/docs)'],
  [/atp_live_<your-key>/, 'the old placeholder (use <ACCESSLINK_API_KEY>)'],
]
for (const [re, what] of forbidden) {
  const m = text.match(re)
  if (m) fail(`document contains ${what}: "${m[0]}"`)
}
if (!doc.servers?.some((s) => s.url === 'https://api.accesslink.store')) fail('servers must name https://api.accesslink.store')
// The guide's section order is the product's: what a developer needs first
// comes first. Enforced by heading order so a rewrite cannot quietly bury the
// quick start under reference material.
const wantOrder = ['Quick start', 'Authentication', 'Integration flow', 'Core operations', 'Fingerprint authentication', 'Errors', 'Rate limits', 'Full API reference']
const headings = [...(doc.info.description ?? '').matchAll(/^## (.+)$/gm)].map((m) => m[1].trim())
const positions = wantOrder.map((h) => headings.indexOf(h))
if (positions.some((p) => p < 0)) fail(`guide is missing sections: ${wantOrder.filter((_, i) => positions[i] < 0).join(', ')}`)
else if (positions.some((p, i) => i > 0 && p < positions[i - 1])) fail(`guide sections are out of order: ${headings.join(' > ')}`)
if (/(transaction|sync job|actor role|SHA-256|XChaCha|shared store|bigserial|middleware)/i.test(doc.info.description ?? '')) {
  fail('the guide uses internal implementation language')
}

// --- report ------------------------------------------------------------------
if (failures.length > 0) {
  console.error(`openapi.yaml: ${failures.length} problem(s)`)
  for (const f of failures) console.error(`  - ${f}`)
  process.exit(1)
}
console.log(
  `openapi.yaml is valid: ${operations} operations across ${Object.keys(doc.paths).length} paths, ` +
    `${declaredTags.size} tags in ${groups.length} groups, ${codes.length} error codes`,
)
