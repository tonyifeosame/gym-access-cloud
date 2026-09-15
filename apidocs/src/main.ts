import { createApiReference } from '@scalar/api-reference'
import '@scalar/api-reference/style.css'
import './theme.css'

/*
 * https://accesslink.store/docs
 *
 * One OpenAPI document, rendered by Scalar, bundled from npm so the page
 * makes no request to any third party: no CDN script, no Scalar fonts, and no
 * request proxy. The interactive "Try it" client is off on purpose -- it would
 * send a reader's real atp_live_ key through proxy.scalar.com or need the API
 * to allow this origin for CORS -- so what the page offers is the generated
 * request samples with a placeholder credential, which is what an integrator
 * pastes into their own tooling anyway.
 */
createApiReference('#app', {
  // Vite's BASE_URL is /docs/ (vite.config.mjs), so this resolves wherever the
  // build is mounted.
  url: `${import.meta.env.BASE_URL}openapi.yaml`,
  theme: 'default',
  layout: 'modern',
  showSidebar: true,
  hideSearch: false,
  searchHotKey: 'k',
  hideModels: false,
  hideDownloadButton: false,
  documentDownloadType: 'both',
  hideDarkModeToggle: false,
  withDefaultFonts: false,
  // No proxy and no in-page client: see the note above. The Scalar "Agent"
  // chat and telemetry are off for the same reason -- both talk to
  // scalar.com, and the agent switches itself on for localhost-like hosts.
  proxyUrl: '',
  hideClientButton: true,
  hideTestRequestButton: true,
  persistAuth: false,
  telemetry: false,
  agent: { disabled: true, hideAddApi: true },
  // "Generate MCP" would hand the document to a Scalar-hosted service; this
  // site publishes a reference, not a transport. The developer toolbar is a
  // localhost convenience that must never show on the public site.
  mcp: { disabled: true },
  showDeveloperTools: 'never',
  // What the request samples default to; every language is still one click away.
  defaultHttpClient: { targetKey: 'shell', clientKey: 'curl' },
  // Keep the sidebar in the order the specification declares (tag groups),
  // and operations in the order they are written, which is the reading order.
  tagsSorter: undefined,
  operationsSorter: undefined,
  // Every operation listed in the sidebar from the start: the reference is
  // fifteen operations, not five hundred, and a reader should see them all.
  defaultOpenAllTags: true,
  expandAllModelSections: false,
  orderRequiredPropertiesFirst: true,
  // The key shown in every generated sample. Never a real one.
  authentication: {
    preferredSecurityScheme: 'IntegrationCredential',
    securitySchemes: {
      IntegrationCredential: { token: '<ACCESSLINK_API_KEY>' },
    },
  },
  metaData: {
    title: 'AccessLink API reference',
    description:
      'AccessLink API reference: members, access rules, sites and events for fingerprint access control.',
  },
})
