import js from '@eslint/js';
import reactHooks from 'eslint-plugin-react-hooks';
import globals from 'globals';
import tseslint from 'typescript-eslint';

// Every visible string comes from the translation files (src/i18n), so this flags words written into JSX: text
// between tags, and the attributes people read or hear.
const SPOKEN = new Set(['title', 'alt', 'placeholder', 'aria-label', 'aria-description', 'label']);
const words = /\p{L}/u;
const strings = {
  rules: {
    'no-hard-coded-strings': {
      meta: { type: 'problem', messages: { text: 'Words in the interface come from src/i18n/en.json: {{text}}' } },
      create(context) {
        const report = (node, text) => context.report({ node, messageId: 'text', data: { text: text.trim().slice(0, 40) } });
        return {
          JSXText(node) {
            if (words.test(node.value)) report(node, node.value);
          },
          JSXAttribute(node) {
            const v = node.value;
            if (SPOKEN.has(node.name.name) && v && v.type === 'Literal' && typeof v.value === 'string' && words.test(v.value)) report(node, v.value);
          },
        };
      },
    },
  },
};

export default tseslint.config(
  { ignores: ['dist', 'node_modules', 'test-results', 'playwright-report'] },
  js.configs.recommended,
  ...tseslint.configs.strict,
  {
    files: ['src/**/*.{ts,tsx}'],
    languageOptions: { globals: globals.browser },
    plugins: { 'react-hooks': reactHooks, strings },
    rules: {
      ...reactHooks.configs.recommended.rules,
      'strings/no-hard-coded-strings': 'error',
    },
  },
  {
    files: ['e2e/**/*.ts', 'scripts/**/*.mjs', '*.config.{ts,js}'],
    languageOptions: { globals: globals.node },
  },
);
