import js from '@eslint/js'
import globals from 'globals'
import reactHooks from 'eslint-plugin-react-hooks'
import tseslint from 'typescript-eslint'

export default tseslint.config(
  { ignores: ['dist', 'coverage', 'node_modules'] },
  {
    extends: [js.configs.recommended, ...tseslint.configs.recommended],
    files: ['**/*.{ts,tsx}'],
    languageOptions: {
      ecmaVersion: 2022,
      globals: globals.browser,
    },
    plugins: {
      'react-hooks': reactHooks,
    },
    rules: {
      ...reactHooks.configs.recommended.rules,

      // Omitting a property by destructuring it away is the idiomatic way to
      // drop one, and an underscore prefix is the convention for "deliberately
      // unused". Neither is a mistake worth failing a build over.
      '@typescript-eslint/no-unused-vars': [
        'error',
        { argsIgnorePattern: '^_', varsIgnorePattern: '^_', ignoreRestSiblings: true },
      ],

      // The site API key is the provisioning secret. It registers terminals and
      // rotates their credentials, and it must never be held by, sent from, or
      // stored in a browser. This rule makes reintroducing it a lint failure
      // rather than something a reviewer has to notice.
      'no-restricted-syntax': [
        'error',
        {
          selector: "Literal[value=/X-API-Key/i]",
          message:
            'The site API key must never be sent from the browser. Operator requests authenticate with the session cookie.',
        },
        {
          selector: "Literal[value=/fingerprint_template/]",
          message:
            'Biometric credentials are an API abstraction. Use biometric_enrolled; never parse or display a template or locator.',
        },
      ],
    },
  },
  {
    files: ['**/*.test.{ts,tsx}', 'src/test/**/*.{ts,tsx}'],
    rules: {
      // Test fixtures assert that these strings are absent, so they have to be
      // able to mention them.
      'no-restricted-syntax': 'off',
    },
  },
)
